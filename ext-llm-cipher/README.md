# `cipherer` — The Cipher / Decipher Dispatcher Service

> **In one line:** a FastAPI micro-service that sits behind the `ext-llm-gateway`'s `cipher_req` and `decipher_resp` worker stages, NER-tags personal names in free text, replaces them with deterministic opaque tokens (and back), and persists a per-chat bidirectional mapping to Redis — never seeing any credentials beyond the per-message JIT ACL user handed to it in the request body.

---

## Table of Contents

1. [What this service is](#1-what-this-service-is)
2. [Why it exists — the design motivations](#2-why-it-exists--the-design-motivations)
3. [Key architectural properties](#3-key-architectural-properties)
   - 3.1 Pluggability (swap the tagger, swap the storage, swap the transport)
   - 3.2 Robustness (idempotency, caching, fuzzy match, credential isolation)
   - 3.3 Asynchrony (stateless FastAPI + gateway's stream orchestration)
   - 3.4 JIT least-privilege security (no stored Redis credentials)
4. [Repository layout](#4-repository-layout)
5. [High-level flow (end-to-end)](#5-high-level-flow-end-to-end)
6. [Deep dive: the three abstractions you can plug into](#6-deep-dive-the-three-abstractions-you-can-plug-into)
   - 6.1 Adding a new **Tagger** (entity recognizer)
   - 6.2 Adding a new **Storage backend** (replace Redis)
   - 6.3 Adding a new **Transport** (non-HTTP handler)
7. [File-by-file walkthrough](#7-file-by-file-walkthrough)
8. [Data model reference (`PersonDataModel`)](#8-data-model-reference)
9. [Schemas & wire contracts](#9-schemas--wire-contracts)
10. [Runtime internals: NER pipeline, fuzzy matching, regex replacement](#10-runtime-internals-ner-pipeline-fuzzy-matching-regex-replacement)
11. [Cache lifecycle & consistency](#11-cache-lifecycle--consistency)
12. [Docker, build & deploy](#12-docker-build--deploy)
13. [Tests & manual exercises](#13-tests--manual-exercises)
14. [Observability (the logger)](#14-observability-the-logger)
15. [Istio / multi-pod routing note](#15-istio--multi-pod-routing-note)
16. [Extending — worked examples](#16-extending--worked-examples)
17. [Glossary](#17-glossary)

---

## 1. What this service is

`cipherer` is the **cipher/decipher dispatcher** invoked by the `ext-llm-gateway` pipeline. It exposes two HTTP endpoints — `POST /cipher` and `POST /decipher` — and is called once per message per direction of travel through the gateway's anonymizer pipeline.

- On **`/cipher`** it receives a ciphering request whose body is a `ReqRedisAccessPermissions` JSON payload (the JIT credentials block minted by the gateway). It reads the raw user prompt from the datasource, detects person names via a two-model NER stack (spaCy + Flair), replaces each detected name with an opaque deterministic token (e.g. `PER_q9rBOL6Q0g4pfi4`), and writes both the anonymized text and the updated person-mapping back to the datasource.
- On **`/decipher`** it receives the equivalent permissions block for the return journey, reads the LLM's (still-tokenized) response from the datasource, and substitutes every token back to the original name using the per-chat mapping.

The service is **stateless** (apart from an in-memory TTL cache of the per-chat `PersonDataModel`), **horizontally scalable** (any pod can handle any request), and **never holds persistent credentials** — each request carries its own short-lived Redis user/password in the body.

The shipped implementation is an **anonymizer**. But the plug points (the `PersonTagger` module, the `RedisCipherManager` methods, the HTTP façade) are small enough that you can trivially retarget the same binary to cipher anything else: credit-card numbers, medical record IDs, addresses, URLs, or a custom vocabulary.

### Where this fits in the gateway

```
            gateway's cipher_req worker                             gateway's decipher_resp worker
                        │                                                          │
                        │ POST /cipher                                             │ POST /decipher
                        │ X-User-ID: <chat_id>                                     │ X-User-ID: <chat_id>
                        │ body = ReqRedisAccessPermissions                         │ body = ReqRedisAccessPermissions
                        ▼                                                          ▼
             ┌──────────────────────────────────────────────────────────────────────────────┐
             │                             cipherer  (this service)                          │
             │                                                                                │
             │   FastAPI   →   RedisCipherManager   →   PersonTagger   →   PersonDataModel    │
             │                                                                                │
             └──────────────────────────────────────────────────────────────────────────────┘
                        │                                                          │
                        │ connect as JIT user                                      │ connect as JIT user
                        ▼                                                          ▼
                                               Redis datasource
                    (read raw text, read/write meta map, write tokenized or detokenized result)
```

---

## 2. Why it exists — the design motivations

Reading the code (and the inline comments the author left across `redisacl.py`, `cipher_text/person_tag_cipher_name.py`, and `person_model/schemas_person.py`) you can reconstruct the motivations:

1. **Anonymization before LLM hand-off is a hard problem that belongs in its own process.** Person name detection requires heavyweight ML models (spaCy's transformer pipeline + Flair's NER), long cold-starts, and a lot of memory. Putting that inside the gateway would bloat every gateway pod. Separating it into its own deployable lets the gateway stay small and lets the cipherer scale on its own resource profile (memory-heavy, GPU-optional).

2. **Persistent tokens must be stable across turns in a conversation.** If the user writes "John Carter" on turn 1 and the LLM replies referencing him, turn 2 of the conversation must produce the same `PER_xxx` token for "John" or "Mr. Carter" as turn 1 produced for "John Carter" — otherwise deciphering breaks. Hence the persistent `PersonDataModel`, the alias expansion in `_get_word_combinations`, and the fuzzy-match fallback for lightly-misspelled repeat mentions.

3. **The process must not hold long-lived credentials.** This dispatcher is invoked by many different gateway pods, for many different chats, each with its own temporary Redis ACL user. Building a pool of persistent credentials would defeat the gateway's JIT-ACL security model. Hence `redisacl.py` creates a fresh Redis client *per request*, authenticating with the user/password that arrived in the request body, and discards the connection when the call returns.

4. **Re-hydrating the full per-chat tagger state from Redis on every message is wasteful.** A conversation has many turns; the tagger state (the `PersonDataModel` with all its lookup maps and fuzzy buckets) only grows monotonically. An in-process TTL cache keyed on the meta key amortizes the Redis fetch. When the tagger mutates the state, we invalidate by writing the fresh copy back to both the cache and Redis in the same transaction logic (see §11 for the consistency notes).

5. **Multi-word, capitalization-insensitive, and fuzzy-tolerant matching — without false positives on ORGs/LOCs/GPEs.** Cipherer does not just run one NER model; it cross-checks **spaCy** (which tends to err on the "is a person" side) against **Flair** (which is better at recognizing ORG/LOC). Anything spaCy labels PERSON but Flair labels ORG/LOC/GPE (and where the two spans overlap on a multi-word text) is suppressed. That's why you see both models loaded in `PersonTagger.__init__`.

---

## 3. Key architectural properties

### 3.1 Robustness

The system is robust across many independent failure modes. Each one is worth calling out because anonymization correctness is load-bearing for the whole gateway — a single missed name means sensitive data leaks to the LLM, a single mismapped token means the user sees the wrong name back:

| Failure mode                                                         | Mitigation                                                                                                          | Where |
|----------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------|-------|
| **NER false-positive on ORGs / locations / products**               | Cross-reference spaCy PERSON entities against Flair ORG/LOC/GPE spans in the same sentence; if a PERSON span's text appears inside a Flair ORG/LOC/GPE span, suppress it. | `person_tag_cipher_name.py#tag_file_persons` |
| **Same person written differently across turns (`John Carter` → `John` → `Mr. Carter`)** | On first sight, pre-compute every partial-word combination (`_get_word_combinations`) and register each as an alias pointing at the same cipher code. Second-and-later sentences resolving a single-word form use `_find_fit_occurance` to pick the *closest prior* occurrence (sequence+position distance metric). | `person_tag_cipher_name.py#_cipher_name`, `_get_word_combinations`, `_find_fit_occurance` |
| **Lightly misspelled / transliterated repeat mention**               | Maintain a per-word-count bucket list (`fuzzy_check_array[0..3]` for 1..4 word names). When an unknown name arrives, run `rapidfuzz` against the correct bucket; accept any match scoring > 80. | `person_tag_cipher_name.py#_find_fuzzy_match` |
| **Token leaks between chats**                                        | The hash seed passed into `_hash_name` is the `chat_id` (see `RedisCipherManager.cipher(..., chat_id)`), so two different chats producing "John Smith" get two different `PER_xxx` tokens. | `person_tag_cipher_name.py#_hash_name`, `redisacl.py#cipher` |
| **NER drops newlines and concatenates sentences**                    | `_clean_text` normalizes whitespace and curly quotes before tagging; `_fix_sentence_newlines` re-inserts newlines after sentence terminators so the LLM receives prose that still reads like prose. | `person_tag_cipher_name.py#_clean_text`, `_fix_sentence_newlines` |
| **Name capitalization flip-flop across turns**                        | When the same cipher code would map to two different source strings (e.g. `"john"` on turn 2 vs. `"John Carter"` on turn 1), `_score_value` prefers the longer, more-capitalized form for the final deciphered output. | `person_tag_cipher_name.py#tag_file_persons`, `_score_value` |
| **Stale in-memory cache after another pod updated Redis**            | The cache is `TTLCache(maxsize=10000, ttl=5 minutes)`. Short enough that cross-pod drift self-heals; long enough that multi-turn conversations within a single pod get amortized lookups. | `redisacl.py#RedisCipherManager.__init__` |
| **Missing / malformed JSON meta on first turn**                      | `PersonDataModel.from_json_string` catches every exception path (JSONDecodeError, ValueError, generic Exception) and returns a fresh empty model. The first turn of any chat thus starts with a known-empty state, no special-casing required in the caller. | `schemas_person.py#from_json_string` |
| **Possessive forms (`Acme Corp's`, `John's`)**                       | `_cipher_name` strips a trailing `'s` before lookup so possessives resolve to the same token as the base name. | `person_tag_cipher_name.py#_cipher_name` |
| **Empty read keys / empty write keys in permissions payload**        | Both `cipher` and `decipher` short-circuit with a clean `(False, "Missing read/write keys in permissions")` — no Redis traffic is attempted. The gateway's worker will then not ACK the message and the pipeline's reclaim logic takes over. | `redisacl.py#cipher`, `decipher` |
| **Invalid Bearer permissions lead to partial writes**                | Both `cipher` and `decipher` wrap their Redis interaction in a single `try/except`; on *any* exception the function returns `(False, str(e))`. Partial writes are possible in principle (e.g. write succeeded but cache update failed) but the gateway treats the whole thing as a retryable failure, and the NER result is idempotent so re-running is safe. | `redisacl.py#cipher`, `decipher` |

The net effect: **idempotent, side-effect-local processing** that the gateway can safely retry without fear of double-writing conflicting names into the `PersonDataModel`.

### 3.2 Asynchrony

The cipherer itself is *not* an orchestrator — the gateway owns the pipeline. But the service is nonetheless structured for asynchronous use:

1. **FastAPI with async route handlers** (`async def cipher`, `async def decipher`). The handlers are declared async even though the underlying Redis client and NER calls are synchronous; this lets Uvicorn free the worker thread while waiting on I/O inside downstream ASGI middleware, and makes it trivial to later swap in an async Redis client (`redis.asyncio`).
2. **No blocking on the gateway.** Every call is a single-shot POST. The gateway's worker calls the cipherer with Heimdall-backed retry/backoff; the cipherer simply returns 200 or non-200, and the gateway handles the state machine (ACK, publish-to-next-stream, or leave-pending-for-reclaim).
3. **No cross-request state that survives a crash.** If a pod dies mid-request, the gateway's Redis stream will still carry the pending message, another cipherer pod will pick up the retry, and because the tagging is deterministic (same seed + same name ⇒ same token), the re-run produces the same token for any already-known person.
4. **Per-request Redis connections.** `_get_client` constructs and returns a fresh `redis.Redis.from_url(...)` with the JIT credentials. There is no connection pool that could outlive the request (and therefore no stale-credential problem when the gateway rotates ACL users).

### 3.3 JIT least-privilege security

This service holds **zero** persistent credentials. It does not know the admin password for Redis, it does not know the adapter's password, and it cannot connect to any key outside the ones listed in the request's permissions payload.

The sequence of trust is:

1. Gateway's worker mints a fresh Redis ACL user scoped to exactly the keys it wants the cipherer to touch (read = previous sequence content, read+write = meta map, write = next sequence slot).
2. Gateway POSTs `ReqRedisAccessPermissions` to `/cipher` or `/decipher`.
3. Cipherer connects to Redis as that temporary user, reads the keys it was given, writes to the keys it was given, returns.
4. Gateway's cleanup loop eventually destroys the temporary user (`ACL DELUSER` + `CLIENT KILL USER`).

If the cipherer were compromised, the blast radius is limited to whichever chats have active credentials in-flight at that moment — not the whole datasource.

---

## 4. Repository layout

```
cipherer/
└── app/
    ├── __init__.py
    ├── main.py                                # FastAPI app: /cipher and /decipher routes
    ├── schemas.py                             # Wire schemas (Pydantic): APIRequest, APIResponse, ReqRedisAccessPermissions, Content, CipherResponse
    ├── process_req.py                         # Legacy / no-op passthrough handler (kept for reference)
    ├── redisacl.py                            # RedisCipherManager: the I/O layer between HTTP and the tagger
    ├── test_tag_cipher_name.py                # CLI driver: cipher a file against a cipher map
    ├── utils/
    │   ├── __init__.py
    │   └── logger.py                          # Module-level logger with UTC timestamps
    ├── cipher_text/
    │   ├── __init__.py                        # Re-exports PersonTagger
    │   ├── person_tag_cipher_name.py          # The live tagger — spaCy + Flair + fuzzy matching
    │   └── person_tag_cipher_name_orig.py     # Prior version, fully commented out, kept for diffing / rollback reference
    └── person_model/
        ├── __init__.py                        # Re-exports PersonDataModel, PersonEntry, Occurrence, RelatedOrig
        └── schemas_person.py                  # The PersonDataModel + its nested types
```

There are **only two public endpoints** and a very small surface: the whole service is ~900 lines including the superseded legacy file. Everything lives under `app/`. No config file is required beyond environment variables understood by FastAPI/Uvicorn; all per-request data arrives in the request body.

---

## 5. High-level flow (end-to-end)

### Step 1 — Cipher request arrives

`POST /cipher` with body:
```json
{
  "user_id":  "anonimyzer_0_ACL_<chat_id>",
  "pwd":      "<uuid>",
  "redis_url": "redis:6379",
  "read_content_meta":  "meta_workergroup_anonimyzer_reqid_<chat_id>",
  "write_content_meta": "meta_workergroup_anonimyzer_reqid_<chat_id>",
  "keys_allowed_read_access":  ["seq_00_workergroup_anonimyzer_wrkind_00_reqid_<chat_id>"],
  "keys_allowed_write_access": ["seq_00_workergroup_anonimyzer_wrkind_01_reqid_<chat_id>"]
}
```
and header `X-User-ID: <chat_id>`.

In `main.cipher`:
1. Extract `chat_id` from the `X-User-ID` header. If absent → HTTP 400.
2. Delegate to `cipher_manager.cipher(request, chat_id)`.
3. On success → `{"message": "chat_id: <id> - Cipher completed successfully"}` with HTTP 200.
4. On failure → HTTP 500 with the error message embedded in the body.

### Step 2 — Read, tag, write (cipher path)

In `RedisCipherManager.cipher`:
1. Validate that read and write key lists are non-empty.
2. Open a fresh Redis client as the JIT user.
3. `GET` the first read key — that's the raw user prompt, wrapped in a `Content` JSON (`{"input_text": "...", "attachments": []}`).
4. Look up `write_content_meta` in the local `TTLCache`. Cache hit → use cached `PersonDataModel`. Cache miss → `GET` the meta key from Redis, parse via `PersonDataModel.from_json_string` (handles empty / absent / malformed gracefully), populate cache.
5. Derive the `seq_num` by slicing characters 4–6 of the read key (`seq_00_...` → `0`). This is used as the `sequence` parameter for the tagger so that each turn's replacements get correctly ordered in `Occurrence` records.
6. Call `self.tagger.tag_file_persons(content.input_text, data_model, seq_num, chat_id)`. Returns `(is_data_model_changed, ann_text)`.
7. Write the anonymized `Content` back at the **last** write-access key (convention: `keys_allowed_write_access[-1]`).
8. If the model was mutated, persist it: write the fresh JSON to Redis at the meta key, and update the cache.
9. Return `(True, "")`.

### Step 3 — Tagger pass

In `PersonTagger.tag_file_persons`:
1. Clean the text (curly quotes → straight, collapse all whitespace into single spaces).
2. Run spaCy's transformer-backed NER over the full document.
3. Per-sentence, build Flair's sentence-level NER prediction in one batched call (`mini_batch_size=32` — modest, because sentences per single prompt are typically < 32 and most sentences contain 0 entities).
4. For each sentence, collect Flair's ORG/LOC/GPE spans into a set for later cross-checking.
5. Walk spaCy's PERSON entities left-to-right. For each:
   - Reject if the span has less than 50% PROPN POS tags (`_is_valid_person`) — filters out things like "the manager".
   - Reject if the text is a multi-word phrase that is a substring of one of Flair's ORG/LOC/GPE spans.
   - Otherwise, call `_cipher_name` to obtain (or mint) the cipher code, record `(start_char, end_char, cipher_code)` for replacement.
6. Build the anonymized text by applying replacements right-to-left (so earlier character offsets are not invalidated by earlier substitutions).
7. Post-process with `_fix_sentence_newlines` to restore sentence breaks that were collapsed by cleaning.
8. Return the mutated flag and the annotated text.

### Step 4 — Cipher code resolution (`_cipher_name`)

Three branches, in order:

**Branch A — known name**: the exact lowercased name is already in `person_key_map`. Take the first code; if the name is multi-word, just use that code; if single-word, resolve via `_find_fit_occurance` which picks the *closest prior occurrence* (by `sequence*100 + position_in_sequance` distance) so that an ambiguous "John" in turn 2 resolves to whichever earlier "John X" was closest.

**Branch B — fuzzy match**: the exact name is unknown, but `_find_fuzzy_match` finds a RapidFuzz ratio > 80 against the same-word-count bucket. Take that fuzzy match's code, record a new occurrence on it under the current spelling.

**Branch C — brand-new name**: hash `seed + lower(name)` with SHA-256, take the first 15 URL-safe base64 chars, prefix with `PER_`. Register the name in `person_key_map`, add it to the right fuzzy bucket, create a `PersonEntry` in `key_person_all_permu_map`, and — critically — register every alias from `_get_word_combinations` so that later turns can match "John", "John Carter", "Carter", "Mr. Carter" all back to the same `PER_xxx`.

### Step 5 — LLM round-trip (the gateway does this)

The anonymized prompt flows through the gateway's `send_to_gemini` stage and back out to the `decipher_resp` worker, which invokes this service's other endpoint.

### Step 6 — Decipher request arrives

`POST /decipher` with a similarly-shaped permissions body, `X-User-ID: <chat_id>`.

In `RedisCipherManager.decipher`:
1. Validate read/write key lists.
2. Open the JIT Redis client.
3. `GET` the first read key — the LLM's response (still tokenized).
4. Load the `PersonDataModel` from the cache or Redis (using `read_content_meta`).
5. Call `_replace_keywords(content.input_text, data_model.chat_decipher)` to substitute every `PER_xxx` back to the original-case original-length name using the word-boundary-safe regex.
6. Write the deciphered `Content` to the last write-access key.
7. Return `(True, "")`.

The `chat_decipher` dict is the only thing we need for this direction — it's the inverse of the person_key_map, maintained during cipher calls.

### Step 7 — Response

The gateway's worker sees HTTP 200 and runs its atomic XACK + XADD to promote the message to the finalizer stage.

---

## 6. Deep dive: the three abstractions you can plug into

All three live in distinct modules with a narrow contract. There is no Python ABC, but the shape of each is simple enough that it reads almost like one.

### 6.1 Adding a new Tagger (entity recognizer)

Current shape (from `person_tag_cipher_name.py`):
```python
class PersonTagger:
    def __init__(self): ...
    def tag_file_persons(self, text: str, data_model: PersonDataModel, sequence: int, seed: str) -> tuple[bool, str]: ...
```

**What you need to do:**
1. Create a new module (e.g. `app/cipher_text/credit_card_tagger.py`) with a class that exposes the same `tag_file_persons(text, data_model, sequence, seed)` signature. Return `(was_model_changed, annotated_text)`.
2. Your tagger is free to use a different detector (a regex for card numbers, a phone-number library, a medical-record regex, spaCy with a custom `entity_ruler`, a local LLM) and a different token prefix (`CC_`, `MRN_`, etc.). Just keep the return contract.
3. Update `app/cipher_text/__init__.py` to re-export your class (or add it alongside `PersonTagger`).
4. In `redisacl.py`, either swap the `self.tagger = PersonTagger()` line or extend the manager to select a tagger at construction time:
   ```python
   def __init__(self, tagger=None, cache_ttl_minutes: int = 5):
       self.tagger = tagger or PersonTagger()
       ...
   ```
5. If you want to route differently per endpoint (e.g. `/cipher_cc` uses the credit-card tagger), instantiate two managers and register two FastAPI endpoints in `main.py`.

You do not need to touch the `PersonDataModel` — its fields (`person_key_map`, `key_person_all_permu_map`, `fuzzy_check_array`, `chat_decipher`) are generic-enough for any name-like entity. If you need a different model, simply define a new schema and pass your model instance in from the caller; the tagger signature allows `data_model` to be anything you want.

### 6.2 Adding a new Storage backend (replace Redis)

Current shape (from `redisacl.py`):
```python
class RedisCipherManager:
    def _get_client(self, permissions: ReqRedisAccessPermissions) -> redis.Redis: ...
    def cipher(self, permissions: ReqRedisAccessPermissions, chat_id: str) -> tuple[bool, str]: ...
    def decipher(self, permissions: ReqRedisAccessPermissions) -> tuple[bool, str]: ...
```

**What you need to do:**
1. Create `app/<backend>acl.py` implementing the same three public methods (`_get_client`, `cipher`, `decipher`).
2. The `ReqRedisAccessPermissions` schema (in `schemas.py`) is fairly generic — `user_id`/`pwd`/`redis_url`/`read_content_meta`/`write_content_meta`/`keys_allowed_read_access`/`keys_allowed_write_access`. If your backend has different credential semantics (e.g. Postgres with a short-lived JWT, or S3 with STS temporary credentials), either extend this schema with new optional fields (they're all `omitempty`-friendly because Pydantic treats `Optional[X] = None` fields as omittable) or introduce a parallel schema and a different Pydantic union.
3. Inside your new manager's `cipher` / `decipher`, preserve the sequence of operations: validate permissions → connect → read content → read-or-cache meta → tag → write content → persist meta. The TTL cache pattern from `redisacl.py` translates 1:1.
4. In `main.py`, either replace `RedisCipherManager()` with your new manager or instantiate both behind different route prefixes.

The key design invariant to preserve is: **no long-lived credentials**. Every call constructs its own client from the request body and discards it.

### 6.3 Adding a new Transport (non-HTTP handler)

Current shape (from `main.py`): FastAPI async routes that take a Pydantic body, an `X-User-ID` header, and delegate to the manager.

**What you need to do:**
1. If you want gRPC: write a `.proto` mirroring `ReqRedisAccessPermissions` and the two string responses. Generate stubs with `grpc_tools.protoc`. Implement a servicer whose two RPCs read the permissions message and the chat_id metadata field, call `cipher_manager.cipher(...)` / `decipher(...)`, and map the `(bool, str)` return to a status code.
2. If you want Kafka / SQS / NATS consumer: write a small consumer loop that polls the queue, deserializes the permissions JSON, synthesizes a chat_id, and calls the manager. Post the result to whatever return channel your orchestrator expects.
3. If you want a CLI (for offline batch anonymization of files): model it on `test_tag_cipher_name.py`, which already shows how to drive `PersonTagger` and `PersonDataModel` without the HTTP or Redis layers.

The manager is the hub; the transport is just the shell around it.

---

## 7. File-by-file walkthrough

### `app/main.py`

The composition root and HTTP surface.

- Instantiates one module-global `cipher_manager = RedisCipherManager()` at import time. This matters: the TTL cache is shared across every request handled by a given Uvicorn worker process, so repeated turns of the same chat reuse the cached `PersonDataModel`.
- Defines two `async def` routes:
  - **`POST /decipher`** — takes a `ReqRedisAccessPermissions` body, extracts `X-User-ID` (either via FastAPI's `Header()` parameter or falling back to `request_hd.headers.get`), calls `cipher_manager.decipher(request)`, returns `{"message": ...}` on success or a `JSONResponse(..., status_code=500)` on failure. Returns HTTP 400 if `X-User-ID` is missing.
  - **`POST /cipher`** — identical structure, delegating to `cipher_manager.cipher(request, chat_id)`. The chat_id is forwarded because the tagger uses it as the SHA-256 seed that makes per-chat hashes unique.

The two routes are nearly symmetric. Both accept the header in two ways (the typed `Annotated[str | None, Header()]` parameter and a manual fallback) — a small belt-and-braces choice to tolerate minor gateway header-casing quirks.

### `app/schemas.py`

The wire contract. Six Pydantic v2 classes:

- **`Content`** — `{input_text: str, attachments: list}`. The envelope that is written under the per-stage Redis key. `attachments` is always passed through verbatim.
- **`APIRequest`, `APIResponse`** — legacy shape referenced by `process_req.py`. No route currently uses them; they remain as an example of what a full request/response envelope *could* carry.
- **`ReqRedisAccessPermissions`** — this is **the** wire contract with the gateway. Mirror of the Go `models.ReqRedisAccessPermissions` struct in the gateway. All fields beyond `user_id`/`pwd`/`redis_url` are `Optional[...] = None` so that a dispatcher that doesn't need meta-key access (e.g. decipher) can omit them.
- **`CipherResponse`** — a single-field `{message: str}`. Currently not bound into any route signature (the routes return untyped dicts), but it documents the intended success shape.

### `app/process_req.py`

An **unused, legacy** helper that echoes `APIRequest` → `APIResponse`. Kept in the tree as a trivial reference handler — an example of what a future "no-op passthrough" route would look like. Not wired into `main.py`.

### `app/utils/logger.py`

A thin wrapper over Python's `logging` producing UTC-formatted log lines:

```
[2026-04-22 09:34:01 +0000] [12345] [DEBUG] Cipher succeeded chat_id: 4d5e... - ...
```

- Uses a `UTCFormatter` subclass that pulls `record.created` and formats it in UTC.
- Attaches a single stdout handler; guards against double-attachment on re-import with `if not logger.handlers`.
- Disables propagation so root-logger configuration (e.g. Uvicorn's default access log) doesn't double-print.

### `app/redisacl.py` (the `RedisCipherManager`)

This is the bridge between HTTP and the tagger. 234 lines, but the top half is actual code and the bottom half is a commented-out alternative `_replace_keywords` that is worth reading for how it might be extended later (see the comment in §10).

- **`__init__(cache_ttl_minutes=5)`** — builds a module-logger reference, instantiates the `PersonTagger` (triggers spaCy + Flair model loading — this is the cold-start cost, ~10-30 seconds), and creates a `TTLCache(maxsize=10000, ttl=300)` for per-chat `PersonDataModel`s keyed by the meta key string.
- **`_get_client(permissions)`** — builds and returns a fresh `redis.Redis.from_url(...)` with `decode_responses=True` (so we get `str` out of `GET`, not `bytes`). No pooling.
- **`decipher(permissions)`** — described in §5 step 6. Key details: uses `keys_allowed_read_access[0]` for the read, `keys_allowed_write_access[-1]` for the write (the `-1` is a convention that allows a sequence of stage outputs to share the same permissions payload). Uses `read_content_meta` for cache/load — note: in the decipher path the meta is read-only, so we never write it back even if we touch it.
- **`cipher(permissions, chat_id)`** — described in §5 steps 2–4. Key details: parses the sequence number via `read_key[4:6]` (slicing by column because the key pattern is fixed-width: `seq_NN_workergroup_...`). Uses `write_content_meta` for both read-then-write of the meta map.
- **`_replace_keywords(text, replacement_dict)`** — builds a single compiled regex from all keys, alternated with `|`, anchored with `\b` on the left and `(?!\w)` on the right, then `pattern.sub(substitute, text)` applies it in one pass. `re.escape` on each key so that hash outputs containing regex metacharacters (unlikely with base64-urlsafe, but safe) don't explode.

### `app/cipher_text/person_tag_cipher_name.py` (the live `PersonTagger`)

The current implementation. Roughly 240 lines. All the interesting private helpers are collocated here.

- **`__init__`** — `spacy.load("en_core_web_trf")` (the transformer-backed English model — heavy, high-accuracy) and `SequenceTagger.load("ner")` (Flair's default NER). Both downloads are baked into the Docker image. `FUZZY_THRESHOLD = 80` (RapidFuzz ratio — empirically good for western names).
- **`tag_file_persons(text, data_model, sequence, seed)`** — the public entry point. Described in §5 step 3. The `_score_value` invocation is the tie-breaker logic for when the same cipher code already has a deciphered mapping — prefers the longer / more-capitalized value.
- **`_score_value(value)`** — returns `(word_count, cap_count)`. Used to decide whether a freshly-detected mention should *overwrite* the existing `chat_decipher` value for a token. "John" doesn't displace "John Carter", "john" doesn't displace "John".
- **`_fix_sentence_newlines(text)`** — regex `(?<=[a-zA-Z0-9]{2})\.\s*` → `.\n`. Restores line breaks lost during cleaning, but only after "real" sentence terminators (two-or-more-alphanumeric-then-period) to avoid breaking on decimals / initials.
- **`_find_fuzzy_match(name_lower, cipher_map)`** — bucketed fuzzy search by word count. Looks only inside `fuzzy_check_array[word_count - 1]`. Uses `rapidfuzz.process.extractOne`.
- **`_is_valid_person(ent)`** — `>= 50%` of tokens must be PROPN. Prevents titles and role-words being tagged.
- **`_hash_name(name, seed)`** — `PER_` + first 15 chars of base64-urlsafe SHA-256(seed + name). `seed` is the chat_id, so uniqueness is per-chat.
- **`_clean_text(text)`** — curly-quote normalization + whitespace collapse.
- **`_get_word_combinations(fullname)`** — alias generator. For a 4-word name, generates 6 aliases (first, first+second, first+second+third, second+third+fourth, third+fourth, fourth). For a 3-word name, 5 aliases. For a 2-word name, just first + last. For a 1-word name, nothing.
- **`_add_person_occurrence(code, name, name_lower, cipher_map, sequence, pos)`** — appends an `Occurrence` onto `cipher_map.key_person_all_permu_map[code].occurances`.
- **`_find_fit_occurance(name_lower, cipher_map, sequence, pos)`** — disambiguates which prior occurrence of a collision-prone name (e.g. "John") the current mention refers to. Computes distance as `(sequence * 100 + pos) − (prior_seq * 100 + prior_pos)` and takes the minimum. The `* 100` is an implicit assumption that within-sequence position fits in two decimal digits — adequate for prose prompts.
- **`_get_final_cipher_code(name, name_lower, cipher_map, sequence, pos)`** — dispatches: multi-word known name → use the one registered code; single-word known name → `_find_fit_occurance`. Two commented-out lines show a previous variant that appended `_SEQ%02d_POS%02d` to the code — apparently the author found that un-suffixed codes give better decipher fidelity in practice (fewer unique tokens to replace, and sequencing is already tracked in `Occurrence`).
- **`_cipher_name(name, cipher_map, pos, sequence, seed)`** — the three-branch resolver described in §5 step 4. Strips trailing `'s` via `name.removesuffix("'s")`.

### `app/cipher_text/person_tag_cipher_name_orig.py`

**Not imported anywhere.** An older, fully-commented-out version of the live file. Useful as:
- A reference for the pre-refactor behaviour (it *did* suffix codes with `_SEQxx_POSxx`, it *did not* have the `_score_value` tie-breaker).
- A git-free rollback artifact in a container where the repo history isn't baked in.

Left here intentionally by the author; you can delete it without any runtime impact.

### `app/cipher_text/__init__.py`

One-liner: re-exports `PersonTagger` so callers can write `from app.cipher_text import PersonTagger`.

### `app/person_model/schemas_person.py`

The persistent data model. Four classes, all Pydantic:

- **`RelatedOrig`** — `{related_orig_sequance: int, related_orig_position_in_sequance: int}`. Remembers where a person was *first* seen (which was used by the older cipher-code variant; still populated for future rollback).
- **`Occurrence`** — `{sequance: int, position_in_sequance: int, value: str, value_cs: Optional[str]}`. One entry per appearance of a specific spelling of a person. `value` is lowercase; `value_cs` preserves original casing.
- **`PersonEntry`** — `{related_orig: RelatedOrig, occurances: list[Occurrence]}`. One entry per cipher code.
- **`PersonDataModel`** — the root. Has four fields:
  - `person_key_map: Dict[str, List[str]]` — name/alias (lowercased) → list of cipher codes it can resolve to. List because aliases can be ambiguous ("John" → [code1, code2]).
  - `key_person_all_permu_map: Dict[str, PersonEntry]` — cipher code → full record of where that code has been used and under what spellings.
  - `fuzzy_check_array: List[List[str]]` — four buckets by word count (1, 2, 3, 4 words). Names are added to the bucket matching their word count to bound the cost of fuzzy matching.
  - `chat_decipher: Dict[str, str]` — cipher code → canonical (best-scoring) display form. This is the only thing `decipher` needs.

Plus a classmethod **`from_json_string(json_string)`** that robustly parses or returns a fresh empty model — see §3.2 Robustness row 9.

### `app/person_model/__init__.py`

Re-exports the four classes.

### `app/test_tag_cipher_name.py`

A CLI driver: `python -m app.test_tag_cipher_name <text_file> <cipher_map_json>`. Reads the text, reads or initializes the cipher map, runs the tagger, writes anonymized output to `../anonymized_output.txt`, and prints the `chat_decipher` dict and the full model for inspection. Useful for offline tuning of the NER stack without spinning up Redis or FastAPI.

---

## 8. Data model reference

The `PersonDataModel` is the heart of the cipherer's persistent state. One instance per chat, stored in Redis under the meta key `meta_workergroup_<group>_reqid_<chat_id>`.

| Field                          | Type                           | Purpose                                                                                  |
|--------------------------------|--------------------------------|------------------------------------------------------------------------------------------|
| `person_key_map`               | `Dict[str, List[str]]`         | Forward index: lowercased name or alias → candidate cipher codes.                        |
| `key_person_all_permu_map`     | `Dict[str, PersonEntry]`       | Reverse index: cipher code → full occurrence history for disambiguation of single-word mentions. |
| `fuzzy_check_array`            | `List[List[str]]` (4 buckets)  | Word-count-bucketed name lists for RapidFuzz lookups.                                    |
| `chat_decipher`                | `Dict[str, str]`               | What `/decipher` consults: cipher code → canonical display name.                         |

**Why four separate maps?** Because each answers a different question with different time complexity requirements:

- "Have I seen this exact name before?" — `person_key_map` (O(1) hash lookup).
- "Given this code and this alias, which specific prior occurrence am I referring to?" — `key_person_all_permu_map` (O(occurrences-of-this-code), typically ≤ 3 in a short conversation).
- "Have I seen something *similar* to this name before?" — `fuzzy_check_array` (O(bucket size), bounded by word count).
- "Given these codes in the LLM response, what are their display names?" — `chat_decipher` (O(1) per code).

You could theoretically collapse these into one multi-way-indexed structure, but Pydantic + JSON serialization favours flat dicts and the redundancy is cheap.

---

## 9. Schemas & wire contracts

The cipherer's contract with the gateway is entirely captured by **`ReqRedisAccessPermissions`**. The fields and their interpretation:

| Field                          | Required on `/cipher` | Required on `/decipher` | Purpose                                               |
|--------------------------------|-----------------------|-------------------------|-------------------------------------------------------|
| `user_id`                      | ✔                     | ✔                       | JIT Redis ACL username minted by the gateway.         |
| `pwd`                          | ✔                     | ✔                       | JIT password.                                         |
| `redis_url`                    | ✔                     | ✔                       | `host:port` of the datasource Redis.                  |
| `read_content_meta`            | ✔ (used read+write)    | ✔ (used read-only)      | The `meta_workergroup_...` key where `PersonDataModel` lives. |
| `write_content_meta`           | ✔                     | —                       | Same key as `read_content_meta` in current flows; kept separate to permit future decoupling. |
| `keys_allowed_read_access`     | ✔ (index 0 is read)    | ✔ (index 0 is read)     | Input content key(s). `[0]` is the actual read target. |
| `keys_allowed_write_access`    | ✔ (index -1 is write)  | ✔ (index -1 is write)   | Output content key(s). `[-1]` is the actual write target. |

Header **`X-User-ID`** carries the `chat_id` and is **required**. The cipherer uses it:
- As the SHA-256 seed in `_hash_name`, guaranteeing that the same person in different chats produces different tokens.
- As the log-line tag for traceability.
- (If Istio sticky routing is configured at the platform layer) as the header the upstream matches on to pin a chat to a particular cipherer pod.

---

## 10. Runtime internals: NER pipeline, fuzzy matching, regex replacement

### The NER pipeline

The tagger runs **two** NER models per request:

1. **spaCy (`en_core_web_trf`)** — transformer-backed. Used as the primary `PERSON` source. Also provides POS tags (used by `_is_valid_person`) and sentence segmentation (`doc.sents`).
2. **Flair (`ner`)** — second opinion, specifically consulted for ORG/LOC/GPE labels. Batched with `mini_batch_size=32` for throughput.

The two-model approach is a classic precision-boosting trick: spaCy has higher recall on `PERSON`, Flair has (empirically, on English news-style text) tighter ORG/LOC boundaries. The cross-check catches cases where spaCy labels something like "Acme Corp" as PERSON because of the capitalization — Flair says ORG, and the heuristic `if " " in name and any(name in s for s in org_mapping)` suppresses the PERSON replacement.

**Downsides** of this approach that you should know about:
- Cold start time is the sum of both model loads. Plan for ~30 seconds.
- Per-request latency is dominated by the transformer forward pass. Rough ballpark: 200-800ms for a 2,000-character prompt on CPU, much less on GPU.
- Memory footprint is ~2 GB for the combined models.

### Fuzzy matching

`rapidfuzz` is chosen over the older `fuzzywuzzy` for C-speed. The matching strategy:

- Names are bucketed by word count (1, 2, 3, 4) into `fuzzy_check_array[i]`.
- A query is only compared against its own bucket (a 2-word name only compares to other 2-word names). This both speeds up the lookup and avoids pathological matches like "John" matching "John Carter" (score ~65) or vice-versa.
- Threshold is **80** (`FUZZY_THRESHOLD`). Below this we prefer minting a fresh code.

### Regex replacement

Used in both directions:

- **Cipher pass** — no global regex. Each entity found by NER is replaced at its `(start_char, end_char)` offsets, right-to-left, so earlier indices remain valid.
- **Decipher pass** — a single compiled regex: `\b(token1|token2|...)(?!\w)`. The negative lookahead is the interesting part: it prevents `PER_ABC` from matching inside a hypothetical `PER_ABCDEF` because there'd be a word-char immediately after the candidate match. `\b` alone is not enough because `_` is a word-char so `\b` doesn't fire between tokens — but the negative lookahead does.

There is a **commented-out alternative** `_replace_keywords` in `redisacl.py` that deduplicates keys by stripping `_SEQ..._POS...` suffixes. It is vestigial — it was useful back when cipher codes did include those suffixes. The live code path does not need it because codes are un-suffixed. Leaving it in place as documentation of the prior design is intentional.

---

## 11. Cache lifecycle & consistency

### The cache

`RedisCipherManager._model_cache = TTLCache(maxsize=10000, ttl=300)` — up to 10,000 distinct chat meta-keys, each entry expires 5 minutes after insertion.

### Writes

On **cipher**, if `tag_file_persons` reports `is_data_model_changed = True`, the manager:
1. Writes the fresh `PersonDataModel` JSON to Redis at the meta key.
2. Updates `self._model_cache[meta_key]` with the new object.

On **decipher**, the model is never mutated; the cache is only read.

### Consistency characteristics

- **Within a pod, within a 5-minute window**: strict consistency — every cipher call updates both cache and Redis.
- **Across pods**: eventually consistent, bounded by the TTL. If pod A ciphers turn 1 and pod B handles turn 2 of the same chat, pod B will miss its cache, fetch the fresh state from Redis, and proceed correctly. Pod A's cache may still hold the old state, but it doesn't matter — nothing reads from A's cache without first being routed there by the gateway's stream.
- **When a pod handles two interleaved chats** (very likely): no cross-chat contamination because the cache key is the full `meta_workergroup_<group>_reqid_<chat_id>` string.
- **When two pods simultaneously cipher the same chat** (the gateway's reclaim logic could theoretically allow this if timings are adversarial): last-write-wins on Redis. The tagger is deterministic (same input text, same seed, same model version produces the same codes) so the practical risk is only losing a handful of occurrence records, which are informational, not load-bearing for decipher.

---

## 12. Docker, build & deploy

The repo as shipped does not include a Dockerfile in the combined archive, but the service is clearly designed for containerization:

- **Base image**: `python:3.11-slim` or similar. You need enough space to pre-download `en_core_web_trf` (~440 MB) and the Flair NER model (~400 MB). Bake both into the image; do not download on first request.
- **Dependencies** (pip): `fastapi`, `uvicorn[standard]`, `pydantic>=2`, `redis>=5`, `cachetools`, `spacy`, `flair`, `rapidfuzz`.
- **Post-install**: `python -m spacy download en_core_web_trf` and a warm-up script that calls `SequenceTagger.load("ner")` once so that Flair's model is cached on disk.
- **Startup command**: `uvicorn app.main:app --host 0.0.0.0 --port 8211`. Port 8211 matches the gateway's shipped `config.json` (the `cipher_req` dispatcher POSTs to `http://host.docker.internal:8211/cipher`, and `decipher_resp` to `/decipher`).
- **Exposed port**: 8211.
- **Resource hints**: request ≥ 2 GB memory, prefer ≥ 2 vCPU. GPU optional; spaCy's transformer model uses it if `torch.cuda.is_available()`.

The service connects to Redis only via credentials that arrive in the request body, so it needs **no environment variables** for Redis — but it does need network reachability to the gateway's `redis:6379` host, which in the gateway's `restart_session_mng.sh` recipe is the linked `my-redis2` container.

---

## 13. Tests & manual exercises

### `app/test_tag_cipher_name.py`

A self-contained CLI that exercises the full tagger without HTTP or Redis:

```bash
# Given text.txt with names in it, and (optionally) cipher_map.json from a prior run:
python -m app.test_tag_cipher_name ./text.txt ./cipher_map.json

# Writes ../anonymized_output.txt and prints:
#   ===== Chat decipher =====   (the code → name map)
#   ===== cipher_map output ===== (the full PersonDataModel)
```

Useful for:
- Tuning `FUZZY_THRESHOLD` against a corpus of names.
- Checking that alias expansion produces the right combinations for your target language (the current heuristics are western-name-shaped).
- Regenerating golden `cipher_map.json` files for integration tests.

### End-to-end against the gateway

The gateway's own `tests/restapi/` contains the sample prompts ("John Carter at Acme Corp's New York office", "Emily Rodriguez from TechNova", etc.) that were explicitly chosen to exercise this service's branches — Acme Corp should **not** be ciphered (ORG), John Carter **should** be ciphered, and "John" in turn 3 should resolve back to John Carter's code via `_find_fit_occurance`.

Run the gateway's pipeline end-to-end (see the gateway README §13) and inspect the cipherer pod's logs — the `[DEBUG]` lines from `RedisCipherManager.cipher` will show cache hits/misses, the sequence numbers, and any per-request failures.

### Unit-level testing suggestions

No unit tests are shipped, but the easy seams are:
- `PersonDataModel.from_json_string` — trivially testable with valid/empty/malformed inputs.
- `PersonTagger._hash_name` — deterministic; golden-value tests with known seeds.
- `PersonTagger._get_word_combinations` — pure function; table-driven tests for 1/2/3/4 word inputs.
- `PersonTagger._fix_sentence_newlines` — pure regex.
- `RedisCipherManager._replace_keywords` — pure function; easy edge-case coverage for word-boundary cases.

---

## 14. Observability (the logger)

- Module-global, initialized in `app/utils/logger.py`.
- Level is hard-coded `DEBUG`; change by editing `logger.setLevel(...)` or by wiring in `LOG_LEVEL` env var if you want.
- Every log line includes the process ID (`%(process)d`) — useful when multiple Uvicorn workers run in one pod and you're grepping for a particular chat.
- UTC timestamps with offset (`%z`) so logs merge cleanly across pods in different timezones.
- `logger.propagate = False` — important: it stops Uvicorn's default access log configuration from doubling each line.

Typical successful cipher turn produces (at DEBUG):
```
[ts] [pid] [DEBUG] /cipher was called...
[ts] [pid] [INFO]  Connecting to Redis: redis:6379
[ts] [pid] [DEBUG] Cache miss for meta_workergroup_anonimyzer_reqid_4d5e... Fetching from Redis...
[ts] [pid] [DEBUG] Tagging person for 4d5e...
[ts] [pid] [DEBUG] Updating Redis with 4d5e... content ...
[ts] [pid] [DEBUG] Cipher succeeded chat_id: 4d5e... - ...
```

---

## 15. Istio / multi-pod routing note

Every request from the gateway carries `X-User-ID: <chat_id>`. If you want to run multiple cipherer replicas behind an Istio `VirtualService` and keep each chat pinned to the same replica (so the in-process TTL cache stays warm for multi-turn conversations), follow the same recipe documented in the gateway README:

1. Label cipherer pods with a version/subset label.
2. Create a `DestinationRule` grouping them under a subset name.
3. Create a `VirtualService` with `match.headers["X-User-ID"]` and route matching traffic to the subset.

This isn't strictly required — the cipherer is correct without sticky routing, it just pays the Redis fetch on every turn instead of amortizing via the cache. At small scale or with low per-chat turn counts, the difference is negligible.

---

## 16. Extending — worked examples

### Example A: cipher credit card numbers in addition to person names

1. Create `app/cipher_text/credit_card_tagger.py` with a class:
   ```python
   class CreditCardTagger:
       def tag_file_persons(self, text, data_model, sequence, seed):
           # use a regex like r'\b(?:\d[ -]*?){13,19}\b'
           # validate with Luhn before tokenizing
           # mint codes as CC_<hash>
           # update data_model.chat_decipher
           return is_changed, annotated
   ```
2. Add a composite tagger that invokes both `PersonTagger` and `CreditCardTagger` in sequence on the same `data_model`. You can share the `chat_decipher` dict; the token prefixes (`PER_` vs `CC_`) will never collide.
3. Swap `self.tagger = PersonTagger()` in `redisacl.py` for `self.tagger = CompositeTagger([PersonTagger(), CreditCardTagger()])`.

No transport changes needed.

### Example B: swap Redis for a gRPC-backed key-value store

1. Create `app/grpcacl.py` with a `GRPCKVCipherManager` class exposing `cipher` and `decipher` with the same signatures.
2. Inside, replace `redis.Redis.from_url(...)` with your gRPC client stub, authenticated using the same `user_id`/`pwd` fields from `ReqRedisAccessPermissions` (map those to whatever your KV service expects — a JWT, mTLS cert reference, etc.).
3. In `main.py`, swap `cipher_manager = RedisCipherManager()` for `cipher_manager = GRPCKVCipherManager()`.

Because the gateway side of the wire still sends `ReqRedisAccessPermissions` (the shape is named for Redis but the schema is generic), you only need to change the gateway's dispatcher-side configuration to point the JIT-user-minting logic at your gRPC service's ACL mechanism.

### Example C: add a gRPC transport for the cipherer itself

1. Write a `.proto` with a `Cipherer` service and two RPCs `Cipher` / `Decipher` that carry the `ReqRedisAccessPermissions` message and a `chat_id` metadata field.
2. Generate Python stubs.
3. Implement a `CiphererServicer` whose methods extract the chat_id and call the existing `cipher_manager.cipher(...)` / `decipher(...)`.
4. Run it on a different port (e.g. 50051). Reconfigure the gateway's `cipher_req` dispatcher to use the gRPC protocol instead of REST.

---

## 17. Glossary

- **Cipher code** — the opaque token that replaces a person name in anonymized text. Format: `PER_` + 15 URL-safe base64 chars of SHA-256(chat_id + lowercase_name).
- **Chat decipher** — the reverse map from cipher code to display-form name, maintained per chat in `PersonDataModel.chat_decipher`.
- **Cipherer** — this service. Also called the "cipher/decipher dispatcher" in the gateway's terminology.
- **Fuzzy bucket** — one of four lists in `fuzzy_check_array`, indexed by word count, used to bound RapidFuzz searches.
- **JIT ACL user** — a single-message Redis user minted by the gateway, scoped to the exact keys the cipherer needs for this one request. Destroyed after the chat session expires.
- **Meta key** — the Redis key under which `PersonDataModel` is stored for a chat. Format: `meta_workergroup_<group>_reqid_<chat_id>`.
- **Occurrence** — one recorded appearance of a specific spelling of a person in a specific sequence and position.
- **PersonEntry** — the reverse-lookup record for a single cipher code: its original occurrence and all later occurrences.
- **PersonDataModel** — the root persistent state per chat: four maps that together let the tagger resolve any alias/fuzzy-match/exact-match back to a stable code.
- **Sequence / position-in-sequence** — a 2-tuple locating a mention. Sequence maps roughly to conversation turn; position is the in-turn token index. The product `sequence * 100 + position` is used as a distance metric.
- **Tagger** — the NER front-end. The shipped implementation uses spaCy+Flair; the interface is a single `tag_file_persons(text, model, seq, seed)` method.

---

## Appendix: Dependency inventory

| Package           | Why                                                                                |
|-------------------|------------------------------------------------------------------------------------|
| `fastapi`         | HTTP framework for the two endpoints.                                               |
| `uvicorn`         | ASGI server.                                                                        |
| `pydantic` (v2)   | Schema validation for the wire payloads and the persistent `PersonDataModel`.       |
| `redis` (≥5)      | Redis client (sync); per-request short-lived clients.                               |
| `cachetools`      | `TTLCache` for the per-chat `PersonDataModel`.                                      |
| `spacy`           | Primary NER (`en_core_web_trf`, transformer-backed), POS tagging, sentence split.   |
| `flair`           | Secondary NER for ORG/LOC/GPE cross-check.                                          |
| `rapidfuzz`       | Bucketed fuzzy-matching for lightly-misspelled repeat mentions.                     |

---

*This README was generated by analyzing the full source tree of `cipherer` (every file marked between `==== start` and `==== End` in `combined-cipherer.txt`), including the inline comments and the fully-commented-out `person_tag_cipher_name_orig.py` which served as a historical reference for design decisions.*
