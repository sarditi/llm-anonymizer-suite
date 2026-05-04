# `ext-llm-cipher2` — extended cipher / decipher dispatcher

A backwards-compatible successor to [`ext-llm-cipher`](../ext-llm-cipher/)
that anonymises **person names** (as before) plus **credit card numbers**
and **postal addresses**, and ships with a real automated test suite.

The wire contract (`POST /cipher`, `POST /decipher`, `X-User-ID` header,
`ReqRedisAccessPermissions` body) is identical to the original service, so
it can be dropped in by retargeting the gateway dispatcher's URL.

> The original `ext-llm-cipher` directory is untouched; this is a side-by-side
> module so you can A/B the two without disrupting anything that already
> depends on the original.

---

## Table of contents

1. [What's new compared to `ext-llm-cipher`](#1-whats-new-compared-to-ext-llm-cipher)
2. [Repository layout](#2-repository-layout)
3. [Wire contract (unchanged)](#3-wire-contract-unchanged)
4. [Configuration](#4-configuration)
5. [Running locally (no Docker)](#5-running-locally-no-docker)
6. [Running with Docker](#6-running-with-docker)
7. [Running the tests](#7-running-the-tests)
8. [How each tagger works](#8-how-each-tagger-works)
9. [Extending — adding another entity type](#9-extending--adding-another-entity-type)

---

## 1. What's new compared to `ext-llm-cipher`

| Area                | `ext-llm-cipher`                                    | `ext-llm-cipher2`                                                                      |
| ------------------- | --------------------------------------------------- | -------------------------------------------------------------------------------------- |
| Entity types        | Person names only (`PER_…`)                         | Person + **Credit card (`CC_…`)** + **Address (`ADDR_…`)**                              |
| Data model          | `PersonDataModel` (4 maps at the root)              | `EntityDataModel` — composes per-entity sub-state (`person`, `credit_card`, `address`) and a shared `chat_decipher` |
| Tagger pipeline     | Single `PersonTagger`                                | `CompositeTagger` chains regex-based taggers first, then the heavy NER one             |
| Tagger selection    | Hard-coded                                          | `CIPHER_TAGGERS` env var (`credit_card,address,person` by default)                     |
| Logger              | Hard-coded `DEBUG`                                  | `LOG_LEVEL` env var (`DEBUG`/`INFO`/`WARNING`/`ERROR`)                                  |
| Cache               | Hard-coded `TTLCache(10000, 5min)`                  | `CIPHER_CACHE_MAX`, `CIPHER_CACHE_TTL_MINUTES` env vars                                |
| `/healthz` endpoint | absent                                              | present (returns `{"status": "ok"}`)                                                   |
| Tests               | None shipped                                        | 55 unit + integration tests (no model download required)                               |
| Person model load   | Eager (~30 s on import)                             | Lazy — loads only when first needed, so unit tests don't pay the cold start            |

The migration cost from a deployed `ext-llm-cipher` to `ext-llm-cipher2` is
limited to: (1) the persisted `PersonDataModel` JSON in Redis is now the
**`person` sub-object** of `EntityDataModel`, so existing meta keys need to
be re-keyed once or simply expired (the JIT TTL handles this naturally
within a few minutes of the cut-over).

## 2. Repository layout

```
ext-llm-cipher2/
├── app/
│   ├── main.py                       # FastAPI app: /cipher, /decipher, /healthz
│   ├── schemas.py                    # Wire schemas (ReqRedisAccessPermissions, Content, …)
│   ├── redisacl.py                   # RedisCipherManager — Redis IO + cache
│   ├── data_model/
│   │   └── schemas.py                # EntityDataModel + per-entity sub-state
│   ├── taggers/
│   │   ├── base.py                   # Tagger Protocol
│   │   ├── composite.py              # CompositeTagger (env-driven)
│   │   ├── credit_card_tagger.py     # NEW — regex + Luhn
│   │   ├── address_tagger.py         # NEW — street + ZIP regex
│   │   └── person_tagger.py          # spaCy + Flair (lazy-loaded)
│   └── utils/logger.py
├── tests/
│   ├── conftest.py                   # fixtures: fresh_model, seed, pure_composite
│   ├── test_credit_card_tagger.py    # Luhn + tokenisation
│   ├── test_address_tagger.py        # street/ZIP regex
│   ├── test_composite_tagger.py      # pipeline + env-driven selection
│   ├── test_data_model.py            # serialise / round-trip / robust parsing
│   ├── test_redisacl.py              # uses fakeredis + a stub tagger
│   ├── test_main_endpoints.py        # FastAPI surface via TestClient
│   └── test_person_tagger_pure_helpers.py  # hash, word combos, etc. (no models)
├── Dockerfile
├── Makefile
├── pytest.ini
├── requirements.txt
└── restart_session_mng.sh
```

## 3. Wire contract (unchanged)

Both endpoints accept a `ReqRedisAccessPermissions` JSON body and require
header `X-User-ID: <chat_id>`.

```http
POST /cipher
Content-Type: application/json
X-User-ID: 4d5e0f00-1234-5678-9abc-def012345678

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

Successful responses:

```json
{ "message": "chat_id: 4d5e0f00... - Cipher completed successfully" }
{ "message": "chat_id: 4d5e0f00... - Decipher completed successfully" }
```

Failures are surfaced with HTTP 4xx (missing `X-User-ID`, missing keys) or
HTTP 500 with the underlying error message in the body.

## 4. Configuration

All knobs are environment variables — no config file is required:

| Variable                    | Default                                | Purpose                                                                                                                       |
| --------------------------- | -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `CIPHER_TAGGERS`            | `credit_card,address,person`           | Comma-separated tagger pipeline. Unknown names are skipped with a warning. Order matters (regex ones first is recommended).  |
| `LOG_LEVEL`                 | `INFO`                                 | One of `DEBUG`, `INFO`, `WARNING`, `ERROR`.                                                                                   |
| `CIPHER_CACHE_TTL_MINUTES`  | `5`                                    | TTL for the per-chat `EntityDataModel` cache.                                                                                 |
| `CIPHER_CACHE_MAX`          | `10000`                                | Max entries in the cache.                                                                                                     |

Examples:

```bash
# Person + credit card only — skip address
CIPHER_TAGGERS=credit_card,person LOG_LEVEL=DEBUG  uvicorn app.main:app

# Run with the regex-only path (no spaCy / Flair → fast cold start, useful
# for smoke testing the deployment plumbing before NER models finish loading).
CIPHER_TAGGERS=credit_card,address  uvicorn app.main:app
```

## 5. Running locally (no Docker)

Requires Python 3.12.

```bash
cd ext-llm-cipher2

python3 -m venv .venv
source .venv/bin/activate

pip install -r requirements.txt
# Heavy models for the PersonTagger — only needed if `person` is in the
# CIPHER_TAGGERS pipeline:
python -m spacy download en_core_web_trf

# `flair` will lazily download `ner` on first call. Pre-warming it is
# optional but speeds up the first /cipher request.
python -c "from flair.models import SequenceTagger; SequenceTagger.load('ner')"

# Boot
LOG_LEVEL=DEBUG uvicorn app.main:app --host 0.0.0.0 --port 8000
```

Smoke test:

```bash
curl -fsS http://localhost:8000/healthz
# {"status":"ok","service":"ext-llm-cipher2"}
```

## 6. Running with Docker

```bash
# from ext-llm-cipher2/
docker build -t ext-llm-cipher2 .
docker run --rm -p 8000:8000 \
    -e CIPHER_TAGGERS=credit_card,address,person \
    -e LOG_LEVEL=INFO \
    ext-llm-cipher2
```

Or use the helper that mirrors the convention used by the other services:

```bash
# from ext-llm-cipher2/
./restart_session_mng.sh

# with overrides
HOST_PORT=8222 CIPHER_TAGGERS=credit_card,address ./restart_session_mng.sh
```

The image bakes both spaCy `en_core_web_trf` and Flair `ner` so the first
`/cipher` request that uses the person tagger doesn't pay the network
download cost.

## 7. Running the tests

The full suite is **55 tests** that run in well under a second on a laptop
because the heavy NER models are not loaded by default.

### 7.1 Quickest path — the `make test-fast` target

```bash
cd ext-llm-cipher2

python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt          # picks up pytest + fakeredis + httpx

make test-fast
# ============================== 55 passed in 0.18s ==============================
```

Equivalent: `pytest -m "not requires_models"`.

What gets exercised:

| Test file                                          | What it covers                                                                                          |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `tests/test_credit_card_tagger.py`                 | Luhn validator, regex matching, deterministic tokens per chat, original-spacing preservation             |
| `tests/test_address_tagger.py`                     | Street/Avenue/Boulevard variants, `Apt N` units, state+ZIP, ZIP+4, overlap resolution, no false positives|
| `tests/test_composite_tagger.py`                   | Pipeline ordering, env-driven tagger selection, decipher round-trip, alias method                        |
| `tests/test_data_model.py`                         | `EntityDataModel` round-trip + `from_json_string` robustness (None / "" / malformed / list)              |
| `tests/test_redisacl.py`                           | RedisCipherManager against `fakeredis`, with cache-warm assertion                                        |
| `tests/test_main_endpoints.py`                     | FastAPI routes via `TestClient` + a stub manager (no Redis, no models)                                   |
| `tests/test_person_tagger_pure_helpers.py`         | Hash determinism + format, alias generation, text cleaning, sentence-newline restoration                 |

### 7.2 Including the heavy NER tests

`tests/test_person_tagger_pure_helpers.py::test_full_pipeline_with_models`
is marked `@pytest.mark.requires_models`. It loads spaCy + Flair on first
call (~30 s cold start, ~2 GB memory) and validates the cross-checked
PERSON-vs-ORG behaviour end to end.

```bash
# Install the heavy models once
python -m spacy download en_core_web_trf

# Run only the model-backed tests
make test-models
# or:
pytest -m requires_models
```

To run **everything**:

```bash
make test-all
```

### 7.3 Inside the Docker image

The Dockerfile copies `tests/` into the image, so you can run the suite
inside the same environment that production uses (with the NER models
pre-baked):

```bash
docker build -t ext-llm-cipher2 .

# fast suite
docker run --rm --entrypoint pytest ext-llm-cipher2 -m "not requires_models"

# everything (slow first time)
docker run --rm --entrypoint pytest ext-llm-cipher2
```

### 7.4 Continuous integration tip

`make test-fast` returns non-zero on any failure and produces no
implicit network traffic — drop it into CI as-is. The slow `make
test-models` should only run on a job that has the NER models cached
between invocations (otherwise every CI run pays the multi-hundred-MB
download).

## 8. How each tagger works

### 8.1 `CreditCardTagger` (`CC_…`)

1. Find every digit-rich substring of 13–19 digits separated only by single
   spaces or hyphens.
2. Strip separators, validate length, validate the **Luhn** checksum.
3. Mint or reuse a per-chat token: `CC_` + first 15 base64-urlsafe chars of
   `SHA-256(chat_id + ":" + digits)`.
4. Record `chat_decipher[code] = original_spelling` so /decipher restores
   the user's exact spacing/dashes.

State lives in `EntityDataModel.credit_card.seen` (digit-only PAN → token).

### 8.2 `AddressTagger` (`ADDR_…`)

Pure regex (no models). Catches:

* Street addresses with a recognised suffix and proper-case street body:
  `^\d+\s+(?:DIRECTION\s+)?[A-Z]\w*(\s+[A-Z]\w*)*\s+SUFFIX\.?(?:\s+DIRECTION)?(?:\s+(Apt|Suite|Unit|#)\.?\s*\w+)?$`
* US state abbreviation followed by a 5- or 9-digit ZIP code (`NY 10001`,
  `CA 94103-1107`).

Bare 5-digit numbers are intentionally **not** ciphered — too many false
positives against order numbers.

State lives in `EntityDataModel.address.seen` (lower-cased, whitespace-
collapsed canonical form → token).

### 8.3 `PersonTagger` (`PER_…`)

Direct port of the original `ext-llm-cipher` tagger:

* spaCy `en_core_web_trf` for PERSON spans + POS tags + sentence segmentation.
* Flair `ner` cross-check to suppress ORG/LOC/GPE false-positives.
* RapidFuzz alias matching against word-count buckets.
* Deterministic tokenisation per chat via SHA-256.

The implementation is feature-parity with the original; the only changes
are (a) reading state out of `EntityDataModel.person` instead of the model
root, and (b) lazy model loading so the test suite doesn't pay cold-start.

## 9. Extending — adding another entity type

1. Add a sub-state to `EntityDataModel` (e.g. `medical_record`).
2. Implement `MedicalRecordTagger` with a single `tag_text(text, model,
   sequence, seed) -> (mutated, annotated)` method. Use a unique token
   prefix.
3. Add a factory entry to `_TAGGER_FACTORIES` in
   `app/taggers/composite.py`.
4. Add `medical_record` to `CIPHER_TAGGERS` in your deployment.
5. Add tests under `tests/test_medical_record_tagger.py` modelled on
   `tests/test_credit_card_tagger.py`.

No changes are needed to `RedisCipherManager`, `main.py`, or the wire
contract — the composite tagger is the only place that knows about
specific entity types.
