# `llm-gemini-mediator` — The LLM Dispatcher Service

> **In one line:** a small, stateless Go micro-service that sits behind the `ext-llm-gateway`'s `send_to_gemini` worker stage, owns the multi-turn chat session with Google Gemini (or any other LLM provider registered in the registry), and translates between the gateway's per-message JIT-ACL Redis protocol and Gemini's native `Chats` SDK — with an in-process session cache so that long conversations don't need to be replayed on every turn.

---

## Table of Contents

1. [What this service is](#1-what-this-service-is)
2. [Why it exists — the design motivations](#2-why-it-exists--the-design-motivations)
3. [Key architectural properties](#3-key-architectural-properties)
   - 3.1 Pluggability (the Provider registry)
   - 3.2 Robustness (session cache, history reconstruction, short-lived credentials)
   - 3.3 Asynchrony (stateless HTTP + gateway's stream orchestration)
   - 3.4 JIT least-privilege security (no stored Redis credentials)
4. [Repository layout](#4-repository-layout)
5. [High-level flow (end-to-end)](#5-high-level-flow-end-to-end)
6. [Deep dive: the three abstractions you can plug into](#6-deep-dive-the-three-abstractions-you-can-plug-into)
   - 6.1 Adding a new **Provider** (LLM backend)
   - 6.2 Swapping the **Session cache** (storage of live chats)
   - 6.3 Adding a new **Handler** (non-HTTP transport)
7. [File-by-file walkthrough](#7-file-by-file-walkthrough)
8. [Configuration & environment variables](#8-configuration--environment-variables)
9. [Wire contracts (what the gateway sends)](#9-wire-contracts-what-the-gateway-sends)
10. [Runtime internals: session lifecycle, history reconstruction, thought parts](#10-runtime-internals-session-lifecycle-history-reconstruction-thought-parts)
11. [Session cache lifecycle & consistency](#11-session-cache-lifecycle--consistency)
12. [Docker, build & deploy](#12-docker-build--deploy)
13. [Observability (the logger)](#14-observability-the-logger)
14. [Istio / multi-pod routing note](#15-istio--multi-pod-routing-note)
15. [Glossary](#17-glossary)

---

## 1. What this service is

`llm-gemini-mediator` is the **LLM dispatcher** invoked by the `ext-llm-gateway` pipeline. It exposes one HTTP endpoint — `POST /gemini_redis_acl` — and is called once per conversation turn by the gateway's `send_to_gemini` worker stage.

- On **`/gemini_redis_acl`** it receives a `ReqRedisAccessPermissions` JSON payload (the JIT credentials block minted by the gateway) plus an `X-User-ID` header carrying the `chat_id`. It reads the ciphered prompt from Redis, maintains or reconstructs a Gemini chat session for that `chat_id`, sends the prompt to Gemini, and writes the response back to Redis for the next worker stage (decipher) to pick up.

The service is:
- **Stateless at the process boundary** (aside from an in-memory TTL cache of live Gemini chat sessions).
- **Horizontally scalable** (any pod can handle any request; a cache miss on one pod just triggers a history reconstruction from Redis).
- **Credential-free at rest** — each request carries its own short-lived Redis username/password in the body; the only persistent secret is the `GEMINI_API_KEY` environment variable.

The shipped implementation ships two providers: **`SIMPLE_GEMINI`** (the real Google GenAI v1.42 client) and **`MOCK`** (an echo provider useful for offline testing without burning quota). Swapping provider is a one-env-var change.

### Where this fits in the gateway

```
            gateway's cipher_req worker                   gateway's send_to_gemini worker
                         │                                              │
                         │                                              │ POST /gemini_redis_acl
                         │                                              │ X-User-ID: <chat_id>
                         │                                              │ body = ReqRedisAccessPermissions
                         │                                              ▼
                         │               ┌─────────────────────────────────────────────────────────┐
                         │               │        llm-gemini-mediator  (this service)              │
                         │               │                                                         │
                         │               │   FastAPI-like Gin handler                              │
                         │               │        │                                                │
                         │               │        ▼                                                │
                         │               │   GeminiService  ───► SessionCache (TTL 10 min)         │
                         │               │        │                    │                           │
                         │               │        ▼                    ▼ (miss)                    │
                         │               │   SessionProvider ──► reconstructChat() from Redis      │
                         │               │        │                                                │
                         │               │        ▼                                                │
                         │               │   Google GenAI Chats.SendMessage()                      │
                         │               └─────────────────────────────────────────────────────────┘
                         │                                              │
                         │                                              │ connect as JIT user,
                         │                                              │ read ciphered prompt,
                         │                                              │ write LLM response
                         ▼                                              ▼
                                                Redis datasource
```

---

## 2. Why it exists — the design motivations

1. **LLM chat sessions are stateful, but the gateway's pipeline is stateless.** Google's Gemini SDK wants you to hold a `*genai.Chat` object across turns so that multi-turn context is preserved efficiently on their side. But the gateway's workers are stateless goroutines that could be reclaimed by `XAUTOCLAIM` and executed on a different pod at any time. Someone has to own the session — and that someone is this service, via an in-process TTL cache keyed on `chat_id`.

2. **The LLM backend should be pluggable.** Today it's Gemini; tomorrow it could be Claude, GPT-4, a local Llama, or a test mock. Hence the `SessionProvider` interface and the `registry/providers/` directory — a `Creator` map identical in spirit to the gateway's own registry pattern. The one-env-var switch (`GEMINI_PROVIDER=MOCK` vs `SIMPLE_GEMINI`) is how you pin which provider a deployment uses.

3. **Cache evictions must not destroy conversations.** A 10-minute TTL on the session cache is convenient — it bounds memory use and lets killed pods self-heal — but a user can absolutely send a follow-up turn 11 minutes after the previous one. When that happens, the service **reconstructs** the chat from the Redis keys it's given (`readAccessKeys[1:]` are the prior turns), replays them into a new `*genai.Chat`, and caches the reconstructed object. The conversation continues seamlessly. This is the central design decision and it's why the gateway gives the LLM worker read access to prior turns via interleaved keys (see the gateway README's "interleave" section).

4. **Gemini responses contain two kinds of content: the answer and the "thought".** The current Gemini SDK may emit `Part`s with `p.Thought = true` (explanations / chain-of-thought) alongside the actual response text. The mediator separates them into `Content.InputText` and `Content.Explanations`, so that downstream (decipher) can process only the visible answer without accidentally deciphering CoT tokens that the user may never see.

5. **No persistent Redis credentials.** This service is called from many gateway pods, for many different chats, each with its own temporary Redis ACL user. Building a pool of persistent credentials would defeat the gateway's JIT-ACL security model. Hence every call constructs a fresh `redis.NewClient(opt)` with the credentials that arrived in the request body, and `defer rdb.Close()` discards it on return.

---

## 3. Key architectural properties

### 3.1 Pluggability — the Provider registry

The core of the pluggability is a copy of the gateway's registry pattern, scoped to just one axis: the LLM provider. In `registry/registry.go`:

```go
var registry = make(map[string]Creator)

func init() {
    registry["MOCK"]          = func() interface{} { return &providers.MockProvider{} }
    registry["SIMPLE_GEMINI"] = func() interface{} { return &providers.GeminiProvider{} }
}

func CreateInstance(name string) interface{} {
    if fn, ok := registry[name]; ok {
        return fn()
    }
    return nil
}
```

In `cmd/main.go` this is used at startup:

```go
providerName := os.Getenv("GEMINI_PROVIDER")
provider     := registry.CreateInstance(providerName).(ai.SessionProvider)
svc, err     := ai.NewGeminiService(context.Background(), apiKey, geminiModel, provider)
```

The `SessionProvider` interface is the one every provider must implement:

```go
type SessionProvider interface {
    CreateSession(
        ctx context.Context,
        rdb *redis.Client,
        client *genai.Client,
        model string,
        sessionId string,
        readAccessKeys []string,
    ) (Messenger, error)
}
```

The returned `Messenger` is an even smaller interface — a single-method façade over "send a user message and get a response":

```go
type Messenger interface {
    SendMessage(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error)
}
```

`*genai.Chat` (the real Gemini session) satisfies this interface natively. The `MockGemini` struct also satisfies it. Any future provider just needs to return something that does.

**There is no hard-coded wiring in `GeminiService`** — it takes a `SessionProvider` in its constructor and uses it as an opaque factory. You can add `"SIMPLE_ANTHROPIC"`, `"LOCAL_OLLAMA"`, `"OPENAI_COMPATIBLE"` etc. without touching any existing file beyond the two-line registration.

### 3.2 Robustness

The system is robust across several independent failure modes. Each is worth calling out because the cost of getting any one of them wrong on an LLM pipeline is high (lost context mid-conversation, double-billed API calls, leaked keys, or a session that diverges from the user's actual history):

| Failure mode                                                  | Mitigation                                                                                                  | Where |
|---------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------|-------|
| **Session evicted from cache mid-conversation**               | On cache miss, the provider's `CreateSession` is called with `readAccessKeys`; if there's more than one key (i.e. the conversation has prior turns), the provider reconstructs the chat by replaying every prior turn's content in role-alternating order (`user`, `model`, `user`, `model`, …). | `registry/providers/gemini_provider.go#reconstructChat` |
| **Pod crashes mid-request**                                   | The gateway's worker did not ACK, so the message stays pending on its stream; another pod (or the same one after restart) will `XAUTOCLAIM` it, hit the cache miss on the new pod, and reconstruct. The `*genai.Chat` state lives only in-process; losing a pod loses no durable data. | `internal/ai/service.go#ProcessRedisContent` |
| **Gemini API transient failure**                              | The outer `POST /gemini_redis_acl` returns HTTP 500 with the error message in the body. The gateway's worker does **not** ACK on non-200, so the message stays pending and will be reclaimed and retried. | `internal/handlers/cipher.go#GeminiRedisAcl` |
| **Slow / stuck Gemini call**                                  | The handler wraps every call in a `context.WithTimeout(c.Request.Context(), 30*time.Second)`. On timeout, the `SendMessage` returns an error, the handler returns 500, the gateway retries. | `internal/handlers/cipher.go#GeminiRedisAcl` |
| **Missing `X-User-ID` header**                                | Rejected with HTTP 400 before any Redis or Gemini traffic.                                                   | `internal/handlers/cipher.go#GeminiRedisAcl` |
| **Empty read-or-write key list in the permissions payload**   | `ProcessRedisContent` returns `(false, "no redis keys provided")` before constructing a session or talking to Gemini. | `internal/ai/service.go#ProcessRedisContent` |
| **Malformed JSON in the Redis-stored `Content`**              | `json.Unmarshal` error surfaces as `(false, "Unmarshel raw failed ...")`; gateway retries. The unmarshal target is a generic `models.Content` so unknown fields are ignored gracefully. | `internal/ai/service.go#ProcessRedisContent` |
| **`session in cache is not a Messenger`** (type assertion failure) | Returns `(false, "session in cache does not support SendMessage")` — defensive code path for the theoretical case where something else writes into the `go-cache` under the same key. | `internal/ai/service.go#ProcessRedisContent` |
| **Chain-of-thought leakage to decipher**                      | When parsing Gemini's response, Parts with `p.Thought = true` go into `Content.Explanations` and Parts without go into `Content.InputText`. Only `InputText` is what the next stage (decipher) will operate on. | `internal/ai/service.go#ProcessRedisContent` |
| **Cached session for a different conversation's `chat_id`**   | The cache key is the full `chat_id` (the `X-User-ID` header value). Collisions would require UUID collision, which we can assume doesn't happen. | `internal/ai/service.go#ProcessRedisContent` |
| **Config missing at startup**                                 | `GEMINI_PROVIDER` resolves to `""` which `CreateInstance` returns `nil` for, and the `.(ai.SessionProvider)` type assertion panics — intentional: a misconfigured pod should fail loudly and let Kubernetes restart it. | `cmd/main.go` |

The net effect: **stateless, retryable processing** that is safe for the gateway to call at-least-once. The gateway may end up sending the same turn twice if the pod fails after calling Gemini but before returning 200; that's a real double-billing hazard, but the gateway's at-least-once semantics are a deliberate simplification for the anonymizer use case. If your provider is expensive per call, wrap `SendMessage` with an idempotency-key layer inside your provider implementation.

### 3.3 Asynchrony

The mediator itself is **not** an orchestrator — the gateway owns the pipeline. But the service is structured to cooperate with the gateway's async model:

1. **Single-shot POST per turn.** The handler returns as soon as Gemini returns and Redis `SET` succeeds. There's no long-poll, no SSE, no WebSocket.
2. **Per-request `context.WithTimeout(30s)`.** Bounds the tail latency of a single turn regardless of what Gemini is doing.
3. **Per-request Redis client.** `utils.RedisOptionsFromURL` (reused from the gateway module) constructs options with `PoolSize=2, MinIdleConns=0, ConnMaxIdleTime=1ms` — deliberately ephemeral, because the user/password in these credentials is JIT and may be destroyed minutes later.
4. **In-process session cache, not Redis-backed session store.** The assumption is that the gateway's Istio-sticky routing (via `X-User-ID`) will pin most turns of a chat to the same pod, and the 10-minute TTL + history-replay fallback handles the exceptions.
5. **No goroutines spawned by the handler.** Gin's own goroutine-per-request model is sufficient. The service runs at O(concurrent requests) parallelism.

### 3.4 JIT least-privilege security

This service holds **one** persistent credential (the `GEMINI_API_KEY`), and **zero** persistent Redis credentials. It does not know the admin password for Redis, it does not know the adapter's password, and it cannot connect to any key outside the ones listed in the request's permissions payload.

The sequence of trust is:

1. Gateway's `send_to_gemini` worker mints a fresh Redis ACL user scoped to exactly the keys it wants the mediator to touch (read = interleaved prior user prompts and model responses, write = next sequence slot for the LLM's response).
2. Gateway POSTs `ReqRedisAccessPermissions` to `/gemini_redis_acl` with `X-User-ID: <chat_id>`.
3. Mediator connects to Redis as that temporary user, reads what it was given, writes where it was given, returns.
4. Gateway's cleanup loop eventually destroys the temporary user.

The `GEMINI_API_KEY` is obviously shared across all chats — there's no way around that, since it's Google's authentication for the whole project. You should treat it the same way you treat any cloud credential: inject it via a Kubernetes Secret, not a hard-coded env var. The `restart_session_mng.sh` dev script does embed a key literally; that's a **dev convenience** only.

---

## 4. Repository layout

```
llm-gemini-mediator/
├── cmd/
│   └── main.go                          # Composition root: reads env vars, wires the provider via the registry, starts Gin
├── internal/
│   ├── ai/
│   │   ├── service.go                   # GeminiService: the session-aware dispatcher, called by the handler
│   │   ├── types.go                     # ChatRequest 
│   │   └── mock_gemini.go               # MockGemini: an in-memory echoing Messenger for offline tests
│   └── handlers/
│       └── cipher.go                    # GeminiRestAPIHandler: the Gin HTTP surface
├── registry/
│   ├── registry.go                      # Factory map: name → provider constructor
│   └── providers/
│       ├── gemini_provider.go           # SIMPLE_GEMINI: real Google GenAI client + history reconstruction
│       └── mock_provider.go             # MOCK: returns a *MockGemini regardless of inputs
├── Dockerfile                           # Multi-stage alpine build that pulls in the gateway's logger/models/utils modules
├── restart_session_mng.sh               # Dev helper: rebuild + restart container with env vars set
├── go.mod / go.mod.docker / go.sum      # go.mod.docker is what ends up inside the container
└── (one malformed file entry labelled "./:" — a leftover scratch copy of go.mod, safe to ignore)
```

The service is compact — the entire signal-carrying codebase is ~350 lines across five Go source files. The rest is dependency manifests, tests, and deploy scripts.

---

## 5. High-level flow (end-to-end)

### Step 1 — Request arrives

`POST /gemini_redis_acl` with body:
```json
{
  "user_id":  "anonimyzer_1_ACL_<chat_id>",
  "pwd":      "<uuid>",
  "redis_url": "redis:6379",
  "keys_allowed_read_access": [
    "seq_01_workergroup_anonimyzer_wrkind_01_reqid_<chat_id>",
    "seq_00_workergroup_anonimyzer_wrkind_02_reqid_<chat_id>",
    "seq_00_workergroup_anonimyzer_wrkind_01_reqid_<chat_id>"
  ],
  "keys_allowed_write_access": [
    "seq_01_workergroup_anonimyzer_wrkind_02_reqid_<chat_id>"
  ]
}
```
and header `X-User-ID: <chat_id>`.

The read-key list is interesting: `[0]` is the **current** turn's ciphered prompt; `[1..]` is the **history** — interleaved prior user prompts and prior model responses, in reverse-chronological order as built by the gateway's `RestAPIDispatcher.interleave` helper. This is what makes session reconstruction possible on a cache miss.

### Step 2 — Handler

In `GeminiRestAPIHandler.GeminiRedisAcl`:
1. `ShouldBindJSON` into a `models.ReqRedisAccessPermissions`. On failure → HTTP 400 with error.
2. Read `X-User-ID`. On empty → HTTP 400.
3. Derive a 30-second-timeout context from the request context.
4. Delegate to `gs.GeminiService.ProcessRedisContent(ctx, req, sessionId)`.
5. On `(false, msg)` → HTTP 500 with `{"error": msg}`.
6. On `(true, "")` → HTTP 200 with `{"message": "Cipher completed successfully"}` (the success message literally says "Cipher" for historical reasons — it's the same message the cipherer returns; no functional meaning).

### Step 3 — Service: open Redis, look up session

In `GeminiService.ProcessRedisContent`:
1. Build Redis options from the permissions via `utils.RedisOptionsFromURL` (imported from the sibling `ext-llm-gateway` module — note the `replace` directive in `go.mod`). Open a fresh `rdb`. `defer rdb.Close()`.
2. Validate read/write key lists are non-empty.
3. Look up `s.sessions.Get(sessionId)`. **Cache hit** → cast to `Messenger`; if cast fails, return the "does not support SendMessage" error.
4. **Cache miss** → call the configured provider's `CreateSession(ctx, rdb, client, model, sessionId, perms.ReadAccessKeys)`. This is where `SIMPLE_GEMINI` and `MOCK` diverge — see steps 3a and 3b below.
5. Touch the cache (`sessions.Set(sessionId, sess, keyTTLInMinutes*time.Minute)`) to bump the TTL on every hit.

### Step 3a — Provider: `SIMPLE_GEMINI`

In `GeminiProvider.CreateSession`:
- If `len(keys) > 1` (i.e. there is prior history), call `reconstructChat(ctx, rdb, keys[1:], client, model)`.
- Otherwise (first turn), `client.Chats.Create(ctx, model, nil, nil)` — a brand-new Gemini chat with no history.

In `reconstructChat`:
1. For each key in `readAccessKeys` (the history slice), `GET` the value from Redis, `json.Unmarshal` into `*models.Content`, and append a `*genai.Content` to a growing `sdkHistory` slice.
2. Alternate roles: index 0 = `user`, index 1 = `model`, index 2 = `user`, …
3. `return client.Chats.Create(ctx, model, nil, sdkHistory)` — Gemini's SDK accepts the prior history verbatim.

### Step 3b — Provider: `MOCK`

In `MockProvider.CreateSession`: returns a new `*MockGemini{}`. History is kept in-process only; no Redis reads.

### Step 4 — Read the current prompt

Back in `ProcessRedisContent`:
1. `readKey := perms.ReadAccessKeys[0]` — the current turn's ciphered prompt.
2. `rdb.Get(ctx, readKey).Result()` → JSON string.
3. `json.Unmarshal` into `models.Content`.

### Step 5 — Call Gemini

`s.sendToGemini(ctx, &req, sess)` — a thin wrapper that calls `chatSession.SendMessage(ctx, genai.Part{Text: req.InputText})` and returns the raw `*genai.GenerateContentResponse`.

### Step 6 — Separate thought from answer

1. Construct an empty `result := &models.Content{InputText: ""}`.
2. Iterate `resp.Candidates[*].Content.Parts[*]`.
3. For each part: if `p.Thought`, append `p.Text` to `result.Explanations`; otherwise append to `result.InputText`.

### Step 7 — Write the response back to Redis

1. `json.Marshal(result)`.
2. `writeKey := perms.WriteAccessKeys[len(perms.WriteAccessKeys)-1]` — the last write-access key (convention shared with the cipherer).
3. `rdb.Set(ctx, writeKey, payload, 0)` (TTL=0, because TTL management is the gateway's job, not this service's).
4. Return `(true, "")`.

### Step 8 — Gateway's worker continues

The gateway's `send_to_gemini` worker sees HTTP 200 and atomically `XACK`s the message from its stream while `XADD`ing it to `decipher_resp`'s stream. The decipher worker then picks it up, reads `writeKey` via its own JIT ACL user, deciphers the `InputText`, and the pipeline continues toward the finalizer.

---

## 6. Deep dive: the three abstractions you can plug into

### 6.1 Adding a new Provider (LLM backend)

The interface is narrow:

```go
type SessionProvider interface {
    CreateSession(
        ctx context.Context,
        rdb *redis.Client,
        client *genai.Client,
        model string,
        sessionId string,
        readAccessKeys []string,
    ) (Messenger, error)
}

type Messenger interface {
    SendMessage(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error)
}
```

**What you need to do to add, say, `SIMPLE_ANTHROPIC`:**

1. Create `registry/providers/anthropic_provider.go`.
2. Define `type AnthropicProvider struct { ... }`.
3. Implement `CreateSession` to construct whatever Anthropic's SDK calls a "conversation". If there is prior history in `readAccessKeys[1:]`, replay it into the Anthropic client's history format. Return something that satisfies `Messenger`. The tricky bit is that `Messenger` is typed in terms of Gemini's `genai.Part` and `genai.GenerateContentResponse` — so you'll need a thin adapter type whose `SendMessage` method translates:
   - inbound `genai.Part` → Anthropic message content
   - outbound Anthropic response → `*genai.GenerateContentResponse` (with Candidates → Parts)
   
   Note: this coupling to `genai.*` types is the **one piece of tech debt** in the interface. A cleaner v2 would define provider-neutral `Part` and `Response` types in `internal/ai/types.go`. The shipped code chose coupling-to-Gemini for speed — which is fine for a Gemini-first product but something to revisit when you add the second provider.
4. Register in `registry/registry.go`:
   ```go
   registry["SIMPLE_ANTHROPIC"] = func() interface{} { return &providers.AnthropicProvider{} }
   ```
5. Deploy with `GEMINI_PROVIDER=SIMPLE_ANTHROPIC` (you may also want to rename the env var at that point — `LLM_PROVIDER` would age better).

The `*genai.Client` passed into `CreateSession` is now unused for your provider; leave it ignored. The `*redis.Client` and `readAccessKeys` are what you actually need for history reconstruction.

You do not need to modify `GeminiService`, the handler, the registry, or `main.go` (beyond the one-line registration).

### 6.2 Swapping the Session cache

Currently: `patrickmn/go-cache` `cache.New(keyTTLInMinutes*time.Minute, cacheGCInMinutes*time.Minute)` with a 10-minute TTL and a 5-minute GC interval.

**What you need to do to swap it out** (e.g. for `freecache` or `bigcache` to cap bytes instead of items, or for a distributed cache like Redis itself):

1. Replace the `sessions *cache.Cache` field on `GeminiService` with a small interface:
   ```go
   type SessionCache interface {
       Get(sessionId string) (Messenger, bool)
       Set(sessionId string, sess Messenger, ttl time.Duration)
   }
   ```
2. Accept a `SessionCache` in `NewGeminiService(...)` instead of hard-coding `cache.New`.
3. Write a thin adapter around `go-cache` that implements this interface, and wire it up in `main.go`.

Important caveat: if you move the session cache **out of the process** (to Redis, Memcached, etc.), you lose the ability to cache live `*genai.Chat` objects because they hold non-serializable Go state (HTTP connections, mutex, etc.). A distributed cache would only make sense if you serialize just the *history* and reconstruct the `*genai.Chat` on every turn — which is what `reconstructChat` already does on a cache miss. In that case the distributed "cache" becomes a history-blob store and the mediator becomes fully stateless at the process boundary. That's a valid design, just a different one.

### 6.3 Adding a new Handler (non-HTTP transport)

Today: Gin HTTP. One route: `POST /gemini_redis_acl`.

**What you need to do** (e.g. for a gRPC variant):

1. Write a `.proto` with a `LLMDispatcher` service and one RPC `GeminiRedisAcl` carrying `ReqRedisAccessPermissions` and a `chat_id` metadata field.
2. Generate Go stubs.
3. Implement a servicer whose method mirrors `GeminiRestAPIHandler.GeminiRedisAcl`:
   - Decode the request.
   - Check the chat_id from metadata (analogous to `X-User-ID` header).
   - Build a 30-second timeout context.
   - Call `svc.ProcessRedisContent(ctx, req, chatId)`.
   - Map `(ok, msg)` to a gRPC status.
4. In `main.go`, start the gRPC server instead of (or alongside) the Gin router. The `GeminiService` is identical — the handler is the only thing that changes.

Same story for a Kafka consumer, a Pub/Sub subscription, or a CLI. `GeminiService.ProcessRedisContent` is the stable hub.

---

## 7. File-by-file walkthrough

### `cmd/main.go`

The composition root. 37 lines. Walkthrough:

1. `gin.Default()` — default middleware (logger, recovery).
2. Read three env vars: `GEMINI_API_KEY`, `GEMINI_MODEL`, `GEMINI_PROVIDER`.
3. `registry.CreateInstance(providerName).(ai.SessionProvider)` — reflection-style instantiation. Panics if the name is unknown or the provider doesn't satisfy the interface.
4. `ai.NewGeminiService(...)` — constructs the genai client, session cache, model name, and provider. Panics on client-construction failure (i.e. bad API key format or genai lib initialization issue).
5. `handlers.NewGeminiRestAPIHandler(svc)` — thin wrapper holding the service.
6. `r.POST("/gemini_redis_acl", hndlr.GeminiRedisAcl)` — the one bound route. 
7. Logs a startup message and calls `r.Run(":9111")` — blocking. The port is hard-coded here; the gateway's `config.json` points `send_to_gemini`'s URL at `http://host.docker.internal:9111/gemini_redis_acl`.

### `internal/ai/service.go`

- **Struct `GeminiService`** — holds the `*genai.Client`, the `*cache.Cache` (session store), the model name (e.g. `gemini-2.5-flash`), and the `SessionProvider`.
- **Constants** `cacheGCInMinutes = 5`, `keyTTLInMinutes = 10` — controls the session cache.
- **Interface `Messenger`** — duck-typed `SendMessage`. `*genai.Chat` satisfies it automatically; `*MockGemini` implements it explicitly.
- **Interface `SessionProvider`** — the provider factory contract (see §3.1).
- **`NewGeminiService(ctx, apiKey, geminiModel, provider)`** — constructs the genai client with the API key, creates the session cache, returns the `GeminiService`. Errors only if genai client construction fails.
- **`ProcessRedisContent(ctx, perms, sessionId)`** — the main entry. Full flow described in §5 steps 3–7.
- **`sendToGemini(ctx, req, chatSession)`** — a 6-line wrapper that defines a local `messenger` interface (unused; vestigial from a previous refactor) and calls `chatSession.SendMessage(ctx, genai.Part{Text: req.InputText})`. The previous call was `SendMessage(ctx, &genai.Part{...})` but the genai v1.42 API changed to value semantics.
- **`redisOptionsFromURL`** — a duplicate of the gateway's `utils.RedisOptionsFromURL`. The actual code path uses the gateway's version (imported); this copy is unused and safe to delete. Kept as a local fallback for when the `replace` directive on the gateway module can't be honored (e.g. standalone builds).
- **Commented-out `reconstructChat`** — was on the service before being moved into `GeminiProvider`. Kept inline for diffing convenience.

### `internal/ai/types.go`

Two types used by the historical `/chat` endpoint (`ChatRequest`, `ChatResponse`). The current `/gemini_redis_acl` endpoint uses `models.ReqRedisAccessPermissions` from the gateway module and returns inline `gin.H` maps, so these types are currently dead code but left in tree as a template for a future non-Redis-backed `/chat` route.

### `internal/ai/mock_gemini.go`

An in-memory echoing `Messenger`. On `SendMessage`:
- If `MockError` is set, return it (for testing error paths).
- Extract the latest part's `.Text`.
- Append `"User: <text>"` and `"AI: <text>"` to `m.History`.
- Return a `*genai.GenerateContentResponse` whose single candidate/part text is `"AI: <text>"`.

`*MockGemini` satisfies `Messenger` by virtue of the `SendMessage` signature.

### `internal/handlers/cipher.go`

The Gin handler — 60 lines. The filename is historical (it was once shared with cipher/decipher handlers in an earlier monorepo layout). Today it contains only the Gemini handler.

- **`GeminiRestAPIHandler` struct** — holds a pointer to `*ai.GeminiService`.
- **`NewGeminiRestAPIHandler(svc)`** — constructor.
- **`GeminiRedisAcl(c *gin.Context)`** — the one route method. Described in §5 step 2. Uses `c.ShouldBindJSON` (Gin's validating unmarshaler — will honor the gateway-defined `binding:"required"` tags on the shared `models.ReqRedisAccessPermissions` struct). Builds a 30-second timeout context with `defer cancel()`. On success returns `{"message": "Cipher completed successfully"}` (the historically-worded success response).

### `registry/registry.go`

14 lines. The factory map:

```go
var registry = make(map[string]Creator)

func init() {
    registry["MOCK"]          = func() interface{} { return &providers.MockProvider{} }
    registry["SIMPLE_GEMINI"] = func() interface{} { return &providers.GeminiProvider{} }
}
```

### `registry/providers/gemini_provider.go`

The real provider. Two methods:

- **`CreateSession(ctx, rdb, client, model, sessionId, keys)`** — branches on `len(keys)`. If there's history (`> 1`), call `reconstructChat(ctx, rdb, keys[1:], client, model)`; else create a fresh chat.
- **`reconstructChat(ctx, rdb, readAccessKeys, client, model)`** — iterates the history keys, unmarshals each into `*models.Content`, alternates `user`/`model` roles by index parity, accumulates a `[]*genai.Content` slice, and calls `client.Chats.Create(ctx, model, nil, sdkHistory)`. The returned `*genai.Chat` is the `Messenger`.

**Role alternation rationale**: the gateway's `interleave` helper produces `[user_n, model_{n-1}, user_{n-1}, model_{n-2}, …]` if you read both slots. But `readAccessKeys[0]` is always the current user prompt, so `readAccessKeys[1:]` starts with the most recent model response, then the user prompt that preceded it, and so on. Since the iteration is index-0-first, index 0 should be "model" — wait, the code says `role := "user"; if i%2 != 0 { role = "model" }` — so i=0 is user, i=1 is model, i=2 is user…

There is a subtle assumption in this code: the order and pairing of keys in `readAccessKeys[1:]` matches the role alternation starting with "user". This is a **coupling between the gateway's `interleave` output order and the mediator's role inference**. If you change either side, change both.

### `registry/providers/mock_provider.go`

Nine lines. `MockProvider.CreateSession` just returns `&ai.MockGemini{}`, ignoring all other inputs.

---

## 8. Configuration & environment variables

Three environment variables, all required at process start:

| Variable            | Purpose                                                                  | Example                            |
|---------------------|--------------------------------------------------------------------------|------------------------------------|
| `GEMINI_API_KEY`    | Google AI Studio API key. Used to construct the `*genai.Client`.         | `AIza...`                          |
| `GEMINI_MODEL`      | Model identifier passed to `client.Chats.Create(..., model, ...)`.        | `gemini-2.5-flash`                 |
| `GEMINI_PROVIDER`   | Registry key for the `SessionProvider` factory.                           | `SIMPLE_GEMINI` or `MOCK`          |

There is no YAML/JSON config file. Everything else (port 9111, 30-second timeout, 10-minute cache TTL, 5-minute GC, etc.) is hard-coded — intentional for a small dispatcher, but worth surfacing to env if you run at scale.

The dev helper `restart_session_mng.sh` sets all three via `docker run -e`.

---

## 9. Wire contracts (what the gateway sends)

The mediator's contract with the gateway is **exactly one type**: `models.ReqRedisAccessPermissions` from the gateway's module (imported via `replace ext-llm-gateway => ...` in `go.mod`).

Fields relevant to the mediator:

| Field                          | Required | Purpose                                                                                              |
|--------------------------------|----------|------------------------------------------------------------------------------------------------------|
| `user_id`                      | ✔        | JIT Redis ACL username minted by the gateway.                                                        |
| `pwd`                          | ✔        | JIT password.                                                                                        |
| `redis_url`                    | ✔        | `host:port` of the datasource Redis.                                                                 |
| `keys_allowed_read_access`     | ✔        | `[0]` = current turn's ciphered prompt key; `[1..]` = interleaved history (most recent first).        |
| `keys_allowed_write_access`    | ✔        | `[-1]` = where to write the LLM's response.                                                          |
| `read_content_meta` / `write_content_meta` | ✗ | Ignored by this service; relevant only to the cipherer stage.                                       |

Header **`X-User-ID`** carries the `chat_id` and is **required**. Used as:
- The session cache key.
- The log-tag for traceability.
- (If Istio sticky routing is configured at the platform layer) the header upstream matches on to pin a chat to a pod.

Response on success: `{"message": "Cipher completed successfully"}` with HTTP 200. Response on failure: `{"error": "<reason>"}` with HTTP 500 (or 400 for bad requests).

---

## 10. Runtime internals: session lifecycle, history reconstruction, thought parts

### Session lifecycle

1. **First turn of a chat.** `perms.ReadAccessKeys` has length 1 (just the current prompt). `s.sessions.Get(chat_id)` misses. `GeminiProvider.CreateSession` sees `len(keys) == 1`, takes the else branch, and calls `client.Chats.Create(ctx, model, nil, nil)` — a brand-new session with no history. Cached under `chat_id`. Current prompt sent, response received, written to Redis.
2. **Subsequent turn (cache hit).** Cache returns the same `*genai.Chat`. Gemini's own server-side session state still holds the prior turn's context; we just call `SendMessage` on the existing chat.
3. **Subsequent turn (cache miss, e.g. after 10 minutes or after pod restart).** `GeminiProvider.CreateSession` sees `len(keys) > 1` (the gateway always includes history in the read-access keys for non-first turns), calls `reconstructChat`, which replays every prior turn into a new `Chats.Create` call with `sdkHistory=[user_0, model_0, user_1, model_1, …]`. Gemini now has the full context again. Caller's current prompt is sent on top.

### History reconstruction order

The gateway's `RestAPIDispatcher.interleave` (documented in the gateway README) builds the read-access keys by interleaving prior user requests and prior model replies. The mediator's `reconstructChat` iterates them in order and alternates roles starting with `user` at `i=0`. **This means the gateway and the mediator must agree on the interleave order** — change one, change both.

### "Thought" parts

Google Gemini can be configured to return parts tagged with `p.Thought = true` — these are chain-of-thought or reasoning traces. The mediator separates them:

```go
for _, cand := range resp.Candidates {
    for _, p := range cand.Content.Parts {
        if p.Thought {
            result.Explanations += p.Text
        } else {
            result.InputText += p.Text
        }
    }
}
```

Only `InputText` flows to the next stage (decipher). `Explanations` is preserved in the `Content` object but isn't deciphered — useful if you want to inspect the reasoning post-hoc via the adapter layer, without leaking it to the user.

### Timeout

`context.WithTimeout(c.Request.Context(), 30*time.Second)`. The derived context is passed into `ProcessRedisContent`, which passes it into both the Redis calls (bounded) and `SendMessage` (bounded). If the total call exceeds 30 seconds, the context is canceled and the Gemini SDK returns an error. The handler returns 500. The gateway's worker does not ACK, the message stays pending, another worker will reclaim and retry. If a request is legitimately going to take longer than 30 seconds (long generations), bump this constant.

---

## 11. Session cache lifecycle & consistency

### The cache

`s.sessions = cache.New(keyTTLInMinutes*time.Minute, cacheGCInMinutes*time.Minute)` — patrickmn/go-cache, 10-minute TTL, 5-minute GC sweep.

### Eviction behavior

`go-cache` evicts entries lazily on GC sweep — so a key might persist slightly past its TTL if the GC hasn't run yet, but will never be returned by `Get` after expiry. Every successful `ProcessRedisContent` call re-sets the entry with a fresh TTL, so active chats don't expire.

### Consistency characteristics

- **Within a pod, across turns within 10 minutes**: perfect continuity. Same `*genai.Chat` object used for every turn. Gemini's server-side session is preserved.
- **Within a pod, across turns > 10 minutes**: cache miss → reconstruction. A new `*genai.Chat` is created with replayed history. From Gemini's perspective this is a new session, but the conversation continues coherently because the history was sent verbatim.
- **Across pods** (e.g. after a deployment rollout): cache miss on the new pod → reconstruction. Same outcome.
- **Two concurrent turns of the same chat hitting two different pods**: both pods would reconstruct and both would call Gemini in parallel. The gateway's sequence-key mechanism makes this impossible in practice — the gateway only dispatches a message when the prior turn's finalizer has already written the response, and only one worker will pick up the message from the stream. But if you ever bypass the gateway's sequencing, be aware: concurrent turns = concurrent billing.

### Why not persist sessions to Redis?

Because `*genai.Chat` holds non-serializable state (goroutines, mutex, HTTP client pool). The only way to "persist" a session across processes is to persist its history and reconstruct — which is exactly what `reconstructChat` does. The design just chose to keep that logic lazy (on cache miss) rather than eager (on every turn).

---

## 12. Docker, build & deploy

### `Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app

COPY ext-llm-gateway/logger ./ext-llm-gateway/logger
COPY ext-llm-gateway/models ./ext-llm-gateway/models
COPY ext-llm-gateway/utils  ./ext-llm-gateway/utils
COPY ext-llm-gateway/go.mod ./ext-llm-gateway/go.mod

COPY llm-gemini-mediator/go.sum ./
COPY llm-gemini-mediator .
COPY llm-gemini-mediator/go.mod.docker ./go.mod

RUN cat go.mod
RUN go mod download
RUN go mod tidy
RUN go build -o server ./cmd/main.go

EXPOSE 9111
CMD ["./server"]
```

Notable details:

- **Multi-module build**: the mediator depends on three packages from the gateway's source tree (`logger`, `models`, `utils`). The `Dockerfile` copies these into a sibling directory and the `go.mod.docker` file uses `replace ext-llm-gateway => ./ext-llm-gateway` to point the dependency at that local copy. This is how the `replace` directive in the dev `go.mod` (which points at an absolute host path) gets rewritten for container builds.
- **`go.mod.docker`**: identical to `go.mod` except for the `replace` target. Copied over `go.mod` during build.
- **`EXPOSE 9111`**: matches the hard-coded port in `main.go`. The gateway's `config.json` expects this.
- **`go mod tidy`** inside the build is a belt-and-braces step to resolve anything the copied-over `go.mod.docker` missed.

### `restart_session_mng.sh`

```bash
cd ..
docker stop  llm-gemini-mediator
docker container rm  llm-gemini-mediator
docker build --no-cache -t llm-gemini-mediator -f llm-gemini-mediator/Dockerfile .
docker run -e GEMINI_MODEL="gemini-2.5-flash" \
           -e "GEMINI_PROVIDER=SIMPLE_GEMINI" \
           -e GEMINI_API_KEY="AIza..." \
           -d --link my-redis2:redis \
           --name llm-gemini-mediator \
           -p 9111:9111 -p 6060:6060 \
           llm-gemini-mediator
```

Notes:
- The `-p 6060:6060` mapping is unused by the shipped code — it's reserved for Go's `net/http/pprof` when debugging. The binary doesn't start a pprof server today, so binding the port has no effect; you can remove it or wire pprof in.
- `--link my-redis2:redis` is the same dev-time link the gateway uses, so the mediator container resolves `redis` to the gateway's Redis container.
- The API key embedded in the script is a dev convenience. **Never ship this to production**; use Kubernetes Secrets + `envFrom` or equivalent.

---

## 13. Observability (the logger)

The mediator reuses the gateway's logger package (`ext-llm-gateway/logger`) via the `replace` directive. All log lines share the same format as the gateway:

```
[2026-04-22 10:34:01] [INFO]: llm-gemini-mediator listening on :9111
[2026-04-22 10:34:45] [DEBUG]: Latest ReadAccessKey is : seq_01_workergroup_anonimyzer_wrkind_01_reqid_<chat_id>
[2026-04-22 10:34:45] [DEBUG]: sendToGemini for sessionId - <chat_id> readkey seq_01_...
[2026-04-22 10:34:48] [DEBUG]: Update redis for sessionId - <chat_id> payload {"input_text":"..."}
```

There is **no** `WithWorker`-style tag here because this service is not a gateway worker — it's called by one. The `sessionId` embedded in each log line fills the same traceability role.

Level is inherited from the gateway's logger default (`DEBUG`). Change via `logger.SetLevel(...)` if needed.

---

## 14. Istio / multi-pod routing note

The gateway's `send_to_gemini` dispatcher sets `X-User-ID: <chat_id>` on every outbound POST. For multi-pod mediator deployments where cache locality matters, set up Istio header-based sticky routing identically to the gateway:

1. Label mediator pods with a subset-selector label.
2. Create a `DestinationRule` grouping them.
3. Create a `VirtualService` with `match.headers["X-User-ID"]` and route to the subset.

Sticky routing is a **performance optimization**, not a correctness requirement. A non-sticky deployment just pays the history-reconstruction cost more often (one extra Redis fetch per prior turn, one extra `Chats.Create` call per cache miss). For short conversations this is invisible; for 20-turn conversations with large prompts it's noticeable.

---

## 15. Glossary

- **Chat session** — a `*genai.Chat` object from Google's genai SDK, or any `Messenger` implementation. Holds the state necessary to `SendMessage` with multi-turn context.
- **History reconstruction** — the process of rebuilding a fresh `*genai.Chat` by replaying every prior turn's user prompt and model response, performed on session-cache miss.
- **Mediator / Dispatcher** — this service. Called "mediator" in its repo name, "dispatcher" in the gateway's terminology.
- **Messenger** — the narrow interface `SendMessage(ctx, ...Part) (*Response, error)` that every provider must return an instance of.
- **Provider** — the pluggable LLM backend factory. Implements `SessionProvider.CreateSession(...)` and registered by name in `registry/registry.go`.
- **Session cache** — the in-process `go-cache` storing `chat_id → Messenger` with 10-minute TTL.
- **Session ID** — equal to the gateway's `chat_id`. Arrives in the `X-User-ID` header. Used as the cache key and the log tag.
- **Thought part** — a `genai.Part` with `Thought == true`, i.e. a chain-of-thought reasoning trace. Routed to `Content.Explanations`, not `Content.InputText`.
- **JIT ACL user** — a single-conversation Redis user minted by the gateway, scoped to just the keys this mediator needs. Destroyed after the chat session expires at the gateway.
- **Interleaved read keys** — the gateway-built history list where odd indices are prior model responses and even indices (beyond `[0]`) are prior user prompts, in reverse-chronological order. Consumed by `reconstructChat`.

---

## Appendix: Dependency inventory

From `go.mod`:

| Package                                       | Why                                                                                  |
|-----------------------------------------------|--------------------------------------------------------------------------------------|
| `github.com/gin-gonic/gin`                    | HTTP router for `/gemini_redis_acl`.                                                 |
| `google.golang.org/genai` v1.42               | Official Google GenAI SDK. Provides `*genai.Client`, `*genai.Chat`, `genai.Part`, and `GenerateContentResponse`. |
| `github.com/patrickmn/go-cache`               | In-process session cache with TTL + background GC.                                   |
| `github.com/redis/go-redis/v9`                | Redis client (per-request, short-lived).                                             |
| `ext-llm-gateway` (via `replace`)              | Sibling module: `logger`, `models` (for `ReqRedisAccessPermissions` and `Content`), `utils` (for `RedisOptionsFromURL`). |

---

*This README was generated by analyzing the full source tree of `llm-gemini-mediator` (every file marked between `==== start` and `==== End` in `combined-gemini-mediator.txt`), including the inline comments and the commented-out blocks in `internal/ai/service.go` that document the evolution of session reconstruction.*
