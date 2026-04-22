# `ext-llm-gateway` — A Pluggable, Asynchronous, Multi-Stage LLM Gateway

> **In one line:** a horizontally scalable, crash-tolerant Go service that orchestrates a configurable pipeline of worker stages (cipher → LLM → decipher → finalize) via Redis Streams, where every stage is loaded dynamically from a registry, and where no stage is ever given more Redis privileges than it strictly needs.

---

## Table of Contents

1. [What this service is](#1-what-this-service-is)
2. [Why it exists — the design motivations](#2-why-it-exists--the-design-motivations)
3. [Key architectural properties](#3-key-architectural-properties)
   - 3.1 Pluggability (the Registry pattern)
   - 3.2 Robustness (crash-tolerance, restart logic, at-least-once)
   - 3.3 Asynchrony (streams, not blocking calls)
   - 3.4 JIT least-privilege security (Redis ACLs)
4. [Repository layout](#4-repository-layout)
5. [High-level flow (end-to-end)](#5-high-level-flow-end-to-end)
6. [Deep dive: the three abstractions you can plug into](#6-deep-dive-the-three-abstractions-you-can-plug-into)
   - 6.1 Adding a new **Handler** (inbound protocol)
   - 6.2 Adding a new **Datasource** (storage backend)
   - 6.3 Adding a new **Dispatcher** (worker stage)
7. [File-by-file walkthrough](#7-file-by-file-walkthrough)
8. [Configuration reference (`conf/config.json`)](#8-configuration-reference)
9. [Data model & Redis key conventions](#9-data-model--redis-key-conventions)
10. [Runtime internals: Lua scripts, atomicity, back-pressure](#10-runtime-internals-lua-scripts-atomicity-back-pressure)
11. [Session lifecycle, cleanup & TTL](#11-session-lifecycle-cleanup--ttl)
12. [Docker, build & deploy](#12-docker-build--deploy)
13. [Tests & manual REST API exercises](#13-tests--manual-rest-api-exercises)
14. [Observability (the logger)](#14-observability-the-logger)
15. [Istio / multi-pod routing note](#15-istio--multi-pod-routing-note)
16. [Extending — worked examples](#16-extending--worked-examples)
17. [Glossary](#17-glossary)

---

## 1. What this service is

`ext-llm-gateway` is an **external LLM gateway** that sits between a client ("adapter") and one or more LLM providers (e.g. Gemini, ChatGPT). The bundled configuration implements an **anonymizer pipeline**: a user prompt is *ciphered* (sensitive entities like names are replaced with opaque tokens such as `PER_xxx_SEQ01_POS002`), the ciphered prompt is sent to the LLM, and the LLM's response is then *deciphered* back to the original entities before being delivered to the adapter.

But that specific chain is only the shipped example. The whole system is built around three plug points:

| Plug point       | Interface (Go)   | Example impl shipped                                |
| ---------------- | ---------------- | --------------------------------------------------- |
| **Handler**      | `Handler`        | `GINRESTAPI` (Gin HTTP server)                      |
| **Datasource**   | `Datasource`     | `Redis` (Redis DB with ACL-based JIT access control) |
| **Dispatcher**   | `Dowork`         | `REDISACL_RESTAPI`, `REDISACL_FINALIZER`            |

You can add gRPC handlers, a Postgres datasource, a SQS dispatcher, a "call local function" dispatcher, etc., **without touching any existing file** — you register the new type in `registry/registry.go` and reference it by name in `conf/config.json`. This is the central design intent of the codebase.

### A note on where this gateway sits in the overall system

This gateway is a **back-end component**. It does not authenticate end-users and it is not meant to be exposed directly to browsers, CLIs, mobile apps, or email ingestion pipelines. The expected deployment topology is:

```
   end user (browser / CLI / email / bot)
             │
             ▼
       ┌──────────────┐         ← authenticates the end-user,
       │   ADAPTER    │           maps them to an `adapter_id`,
       │   SERVICE    │           holds the STREAM_<chat_id> reader,
       └──────────────┘           applies policy / quotas / rate limits
             │  HTTP (POST /init_chat, /set_req_from_adapter)
             ▼
       ┌──────────────┐         ← this codebase.
       │ EXT-LLM-GW   │           Trusts any caller in its
       │  (gateway)   │           `handler.adapters` allow-list.
       └──────────────┘
             │
             ▼
     Redis + external cipher/LLM/decipher services
```

The gateway's authentication surface is only the `adapter_id` allow-list (`handler.adapters` in `config.json`). Everything else — user login, session cookies, API keys, tenant isolation, abuse protection — belongs to the adapter layer above it. The shipped `/set_req_from_adapter_sync` endpoint bypasses this adapter layer and exists **only for a simple UI proof-of-concept**; see §7 for the full caveat.

---

## 2. Why it exists — the design motivations

Reading the code (and the inline comments the author left across `models/models.go`, `orchestrator/interfaces.go`, `registry/dispatchers/*.go`) you can reconstruct the motivations:

1. **Every LLM integration looks similar but differs in the details.** You always need: an inbound API, a chain of pre/post-processing steps, a durable queue between steps, retries, backoff, timeouts, ACL boundaries between stages, and a response channel back to the caller. The author chose to write the *skeleton* once and let the *interesting parts* be plugged in.

2. **The pre/post-processing may contain sensitive data that must not leak into the LLM, and the LLM response must not be seen in the clear by any stage that has no business seeing it.** Hence each worker stage gets a **temporary Redis ACL user**, created at the moment work is dispatched, scoped to exactly the keys that stage needs to read and write. This is a Just-In-Time, least-privilege model. See `registry/datasource/redis.go#SetTempAclUserPermissions`.

3. **The service must survive crashes.** Workers crash. Pods restart. LLMs time out. Messages must not be lost, must not be double-billed, and dangling state must be cleaned up. Every hop between stages is implemented as a Redis Stream with `XACK`/`XAUTOCLAIM`/`XPENDING` semantics, plus Lua scripts that make the ACK+publish-to-next-stream atomic.

4. **The service must be asynchronous.** A ciphering step might take 50ms; an LLM call might take 90 seconds. Blocking the HTTP handler for the full duration is not acceptable. The handler returns immediately with a `chat_id` and a **stream password**; the adapter later reads the final response asynchronously from `STREAM_<chat_id>`. A *synchronous* convenience endpoint (`/set_req_from_adapter_sync`) exists for simple UIs, but the primary path is async.

5. **Multi-pod horizontal scaling should "just work".** All state lives in Redis; any pod can handle any request; `XAUTOCLAIM` lets a surviving pod pick up work abandoned by a crashed pod.

---

## 3. Key architectural properties

### 3.1 Pluggability — the Registry pattern

The core of the pluggability lives in `registry/registry.go`. It is a classic **type registry / factory map** that maps string names (from config) to zero-arg constructors:

```go
var registry = make(map[string]Creator)

func init() {
    registry["Redis"]              = func() interface{} { return &ds.RedisDatasource{} }
    registry["REDISACL_RESTAPI"]   = func() interface{} { return &dispatchers.RestAPIDispatcher{} }
    registry["REDISACL_FINALIZER"] = func() interface{} { return &dispatchers.RedisAclFinalizer{} }
    registry["GINRESTAPI"]         = func() interface{} { return &handlers.RestAPIHandler{} }
}

func CreateInstance(name string) interface{} {
    if fn, ok := registry[name]; ok {
        return fn()
    }
    return nil
}
```

In `cmd/main.go` this is used as a **reflection-style instantiation** from the config values:

```go
dataS := registry.CreateInstance(LlmExtGatewayConfig.Datasource.Name).(orchestrator.Datasource)
hndlr := registry.CreateInstance(LlmExtGatewayConfig.Handler.Name).(orchestrator.Handler)
dw    := registry.CreateInstance(vw.Dispatcher.Protocol).(orchestrator.Dowork)
```

Each concrete type is then initialized via its interface method (`InitDataSource`, `InitHandler`, `InitDispatcher`) with the full sub-block from the config JSON. **There is no hard-coded wiring**: if you delete every mention of `REDISACL_RESTAPI` from `config.json` and register your own `MY_KAFKA_DISPATCHER`, the main binary does not change.

### 3.2 Robustness

The system is robust across many independent failure modes. Each of these is worth calling out because the cost of getting any one of them wrong in a queue-based pipeline is huge (silent data loss, silent double-processing, or runaway cost):

| Failure mode                          | Mitigation                                                                                 | Where |
|---------------------------------------|--------------------------------------------------------------------------------------------|-------|
| **Dispatcher panics mid-message**     | Worker has a `defer recover()`, increments a restart counter, retries up to `maxRestarts` before panicking the whole pod. | `orchestrator/workers.go#Worker.Start` |
| **Pod dies holding an unacked message** | Other pods (or the same pod after restart) `XAUTOCLAIM` messages idle for more than `IdleThresholdSec`. | `orchestrator/synchronizer.go#ClaimNAckFromStream` |
| **Message stuck forever (truly broken)** | Messages pending for `UnackMsgDurationSeconds` are hard-deleted; the originating `reqid` is notified on its adapter stream with an `_ERROR_` payload. | `orchestrator/synchronizer.go#DelOldNAckFromStream` |
| **ACK succeeds but publish to next stream fails (or vice-versa)** | A single Lua script (`ackAndPublishLua`) runs both `XACK` and `XADD` atomically inside Redis. Partial states are impossible. | `orchestrator/synchronizer.go#AckAndPublishToNextStreamAtomic` |
| **REST downstream transiently unavailable** | Heimdall `ExponentialBackoff` + `Retrier` wraps every outbound HTTP call with `BackoffInterval`, `MaxBackoff`, `RetryCount`, and per-request `Timeout`. | `registry/dispatchers/restapi_dispatcher.go#InitDispatcher` |
| **Oversized prompt** | Handler rejects at the edge with a `ResourceExhausted` gRPC code (mapped to HTTP 500) before anything hits Redis. | `registry/handlers/utils.go#SetReqToStream` |
| **Abandoned chat sessions accumulate data and ACL users forever** | A background ticker (1 minute cadence) scans a ZSet of "last active time per chat_id", deletes every Redis key matching the expired `reqid`, and removes all temp ACL users whose names contain the expired id. | `orchestrator/workerhandlers.go#cleanupExpiredSessions` + `utils/utils.go#PerformCleanup` + `utils/utils.go#CleanupTempUsers` |
| **Adapter ID spoofing at init time** | `InitReq` verifies `adapter_id` is in the configured allow-list before doing anything. | `orchestrator/workerhandlers.go#InitReq` |
| **Duplicate/out-of-order `SetRequest` calls**| Every request must carry a `request_key` that equals the current `SEQ_<chat_id>` value; mismatches return `Internal` immediately. The "next" key is generated and returned only after successful processing of the current one. | `orchestrator/workerhandlers.go#SetReqFromAdapterInternal` |

The net effect: **at-least-once delivery** with idempotency guarantees enforced at the sequencing layer.

### 3.3 Asynchrony

The entire pipeline is built on Redis Streams. Each worker owns a stream named `WS_WorkerGrp-<group>_WorkerInd<N>`, created in `main.go` via `synchDS.CreateWorkerStream(...)`. Work flows **exclusively** through these streams:

```
Handler (HTTP)
   │  XADD → WS_WorkerGrp-anonimyzer_WorkerInd0  (cipher_req stream)
   ▼
Worker[0] (cipher_req)  ─── Dowork() ──→ cipher service
   │  Lua: XACK + XADD → WorkerInd1
   ▼
Worker[1] (send_to_gemini) ─── Dowork() ──→ LLM
   │  Lua: XACK + XADD → WorkerInd2
   ▼
Worker[2] (decipher_resp) ─── Dowork() ──→ decipher service
   │  Lua: XACK + XADD → WorkerInd3
   ▼
Worker[3] (_FINALIZER_)  ─── Dowork() ──→ SetFinalResponseInAdapterStream
                                          │
                                          ▼
                              STREAM_<chat_id>  ←─── adapter reads via XREAD
```

The handler **never waits** for the pipeline to finish (except in the optional sync endpoint). On the client side, the `InitChatResponse` returns a `stream_pwd` and the adapter uses `XREAD STREAMS STREAM_<chat_id> $` (blocking) to pick up the final answer when it arrives.

Inside each worker, the main loop (in `orchestrator/workers.go#Worker.Start`) runs three things per tick:

1. `FetchMessageFromStream` — grab one new message with `XREADGROUP`.
2. `ClaimNAckFromStream` — grab one idle (abandoned) message with `XAUTOCLAIM`.
3. `DelOldNAckFromStream` — garbage-collect messages pending longer than the configured age.

This triple makes the system both *progressive* (keep making forward progress on new messages) and *self-healing* (cleanly reclaim or bury stuck work), all without any central coordinator.

### 3.4 JIT least-privilege security

Each dispatcher creates a **brand-new Redis ACL user** every time it processes a message, with read/write globs tailored to the keys it will touch:

```go
// RestAPIDispatcher.Dowork — excerpt
reqRedisAccessPermissions.UserID = fmt.Sprintf("%s_%d_ACL_%s", di.WorkerGroup, di.WorkerID, reqid)
reqRedisAccessPermissions.Pwd    = uuid.New().String()

if di.WorkerName == "cipher_req" {
    // This stage can READ N previous sequences of its own worker slot,
    // READ+WRITE its metadata key,
    // and WRITE ONLY the next sequence slot.
    reqRedisAccessPermissions.ReadAccessKeys       = GenerateKeysList(reqid, di.WorkerGroup, di.WorkerID, 1, seq)
    reqRedisAccessPermissions.ReadAccessContentMeta  = fmt.Sprintf(models.MetaReqID, di.WorkerGroup, reqid)
    reqRedisAccessPermissions.WriteAccessContentMeta = fmt.Sprintf(models.MetaReqID, di.WorkerGroup, reqid)
    reqRedisAccessPermissions.WriteAccessKeys = append(...)
}
```

The credentials are then POSTed (as a JSON blob) to the external cipher/LLM/decipher service, which connects *as that user* to Redis to fetch the content and write its result. The gateway itself **never carries the payload in memory** for non-finalizer stages. It only moves **references and permissions**.

When the session expires (see §11), the ACL user is destroyed and any live connections using it are killed (`CleanupTempUsers` in `utils/utils.go`).

---

## 4. Repository layout

```
ext-llm-gateway/
├── cmd/
│   └── main.go                          # Composition root: reads config, wires everything via registry, starts workers
├── conf/
│   └── config.json                      # The single source of truth for handlers/datasources/workers/dispatchers
├── logger/
│   └── logger.go                        # Level-aware, worker-aware structured-ish logger
├── models/
│   └── models.go                        # All wire types + all config types + all Redis key templates
├── orchestrator/
│   ├── interfaces.go                    # The three plug-point interfaces: Handler, Dowork, Datasource
│   ├── workerhandlers.go                # Mux — the orchestration entry point used by handlers
│   ├── workers.go                       # Worker goroutine loop (fetch, reclaim, GC, dispatch)
│   └── synchronizer.go                  # SynchRedisDatasource — all Redis Streams / ACL / Lua logic
├── registry/
│   ├── registry.go                      # The factory map (name → constructor)
│   ├── datasource/
│   │   └── redis.go                     # Datasource impl: Redis with JIT ACL
│   ├── dispatchers/
│   │   ├── restapi_dispatcher.go        # REDISACL_RESTAPI — generic outbound REST worker
│   │   ├── redisacl_finalizer.go        # REDISACL_FINALIZER — terminal stage, writes to adapter stream
│   │   └── utils.go                     # Shared key-list builders
│   └── handlers/
│       ├── restapihandlers.go           # GINRESTAPI — Gin HTTP handler, 3 endpoints
│       └── utils.go                     # Stream read/write glue for sync endpoint
├── utils/
│   └── utils.go                         # Redis option builders, gRPC↔HTTP code mapping, cleanup procs
├── tests/
│   └── restapi/
│       ├── initchat.json                # Sample init payload
│       ├── set_req_existing_chtid.json  # Sample follow-up payload (John Carter / Datadog prompt)
│       ├── set_req2_existing_chtid.json # Sample follow-up (Emily Rodriguez prompt)
│       ├── set_req3_existing_chtid.json # Sample follow-up
│       ├── set_req4_existing_chtid.json # Sample follow-up (date arithmetic prompt)
│       ├── set_req5_existing_chtid.json # Sample follow-up (capitalize prompt)
│       ├── set_req_wrong_chtid.json     # Sample negative-path payload
│       └── commands_to_execute.sh       # curl one-liners against :7700
├── Dockerfile                           # Multi-stage build, alpine-based
├── go.mod / go.mod.docker               # go.mod.docker is what ends up inside the image
├── go.sum
├── restart_session_mng.sh               # Dev helper: rebuild + restart container, linked to my-redis2
```

---

## 5. High-level flow (end-to-end)

Using the shipped `config.json` (adapter → cipher → Gemini → decipher → finalizer):

### Step 1 — `POST /init_chat`

Adapter sends:
```json
{ "llm": "chatgpt", "user_id": "sss@ff.com", "adapter_id": "ada1" }
```

In `RestAPIHandler.InitChat` → `Mux.InitReq`:
1. Validate JSON, validate `adapter_id` is in `handler.adapters` allow-list.
2. Generate `internal_chat_id = uuid.New()`.
3. Set `SEQ_<chat_id>` to `00_<uuid>` — this is the *only* acceptable `request_key` for the first SetRequest.
4. Store `METADATA_<chat_id>` = the marshalled `InitChatRequest`.
5. Create `STREAM_<chat_id>` and attach an ACL user named `ada1` with **read-only** access to that stream only (`+XREAD +xdel -xadd`). The password is a fresh UUID returned to the adapter.
6. Mark the session timestamp in the `anonimyzer_active_sessions` ZSet.

Response:
```json
{
  "status": "success",
  "chat_id": "<uuid>",
  "set_next_req_key": "00_<uuid>",
  "message": "New Internal chat ID was created",
  "stream_pwd": "<uuid>"
}
```

### Step 2 — `POST /set_req_from_adapter`

Adapter sends:
```json
{
  "internal_chat_id": "<uuid-from-step-1>",
  "request_key": "00_<uuid>",
  "content": { "input_text": "Hi, I'm John Carter..." }
}
```

In `RestAPIHandler.SetReqFromAdapter` → `SetReqToStream` → `Mux.SetReqFromAdapterInternal`:
1. Validate size against `ValRedisMaxSizeByte`.
2. If `request_key` starts with `00` and a `prompt` is configured, **prepend the prompt** to the input text. This is how the anonymizer's "immutable PER_xxx_SEQyy_POSzz instruction" is injected.
3. Check `METADATA_<chat_id>` exists (session was initialized).
4. Check submitted `request_key` equals current `SEQ_<chat_id>`.
5. `ds.SetContent(...)` → write the content to Redis at `seq_00_workergroup_anonimyzer_wrkind_00_reqid_<chat_id>`.
6. Run the atomic Lua `luaSetWorker0Atomic`: update `SEQ_<chat_id>` to a fresh `01_<uuid>` AND `XADD` onto the first worker stream in one step.
7. Touch the session timestamp.
8. Return HTTP 200 with `success`.

At this point the request is "in the pipe" and the handler is done.

### Step 3 — Pipeline processing (fully async)

The `cipher_req` worker (2 threads, see config) wakes up, `XREADGROUP`s the message, parses out `seq` and `reqid`. Its `Dowork`:
1. Mints a temp ACL user with: **read** = prior-sequence key; **read+write** = metadata key; **write** = next-sequence slot on worker-index+1.
2. POSTs the ACL blob to `http://host.docker.internal:8211/cipher` with header `X-User-ID: <reqid>` (used for Istio sticky routing).
3. The cipher service connects to Redis as that temp user, reads the raw input, runs its replacement, writes the ciphered result at `seq_00_wrkind_01_...`, and updates the metadata with its person→token map.
4. On HTTP 200, the worker runs `AckAndPublishToNextStreamAtomic`: Lua `XACK` + `XDEL` on own stream, `XADD` on `send_to_gemini` stream — atomically.

`send_to_gemini` then picks up the message. Its ACL permissions are slightly different: when `seq > 0` it uses `interleave()` to give the LLM access to both prior user requests *and* prior model replies (kept in a separate `llmRepWorkerIND` slot that's lazily discovered the first time the send-to-gemini stage writes a response). This is how multi-turn context is threaded without the LLM ever seeing the deciphered names.

`decipher_resp` then reverses the ciphering.

### Step 4 — Finalization

`_FINALIZER_` uses the `REDISACL_FINALIZER` dispatcher rather than `REDISACL_RESTAPI`. It does **not** call an external service. Instead it:
1. Mints a JIT user with read access to the final-stage key (where the deciphered response lives).
2. Calls `SetFinalResponseInAdapterStream(reqid, {ReqRedisAccessPermissions: ...})` which `XADD`s onto `STREAM_<chat_id>`.
3. Returns. This worker has `nextStreamInSeq = "NA"`, so the Lua script takes the terminal branch and just returns `"ack_is_successful_in_finalizer"`.

### Step 5 — Adapter reads the response

The adapter (on its own schedule, with the `stream_pwd` from step 1) does:

```
XREAD BLOCK <big-timeout> STREAMS STREAM_<chat_id> $
```

It gets back `{ response: <ReqRedisAccessPermissions json>, next_seq: "01_<uuid>" }`. It then connects to Redis as that JIT user and fetches the actual deciphered content from the one readable key. It now knows the next valid `request_key` and can continue the conversation.

**The sync shortcut** (`/set_req_from_adapter_sync`) does the XREAD server-side and returns the content directly in the HTTP response. **It is an unauthenticated / unauthorized convenience endpoint intended purely for a simple UI PoC** — it exists so a developer can `curl` the gateway from a browser or a minimal front-end and see a round-tripped response without writing an adapter. **It is not the production shape of the system.** In production, a separate **adapter layer** should sit between any front-end (web UI, CLI, email ingestion, chatbot, etc.) and this gateway. That adapter is the component responsible for:

- **Authentication** of the end-user (OIDC, mTLS, API keys, whatever the front-end channel calls for).
- **Authorization** — mapping the authenticated principal onto one of the `adapter_id`s the gateway knows about, and enforcing per-user quotas, rate limits, and policy.
- **Multiplexing** — holding the async `STREAM_<chat_id>` read loop for its users, buffering responses, and delivering them back to the originating front-end in whatever shape that channel expects.
- **Credential custody** — safeguarding the `stream_pwd` returned by `InitChat` so end-users never see Redis credentials.

The gateway itself is deliberately agnostic about end-users: it only knows about adapters (via the `handler.adapters` allow-list). The `/set_req_from_adapter_sync` endpoint bypasses the adapter layer entirely, which is fine for a PoC against a trusted localhost Redis but must be disabled — or fronted by an authenticating proxy — in any real deployment. The comment in `restapihandlers.go` additionally warns that for heavy content (long LLM completions) this endpoint is impractical because the HTTP response time is dominated by end-to-end LLM latency and can easily exceed client-side timeouts.

---

## 6. Deep dive: the three abstractions you can plug into

All three live in `orchestrator/interfaces.go`. They are deliberately small.

### 6.1 Adding a new Handler (inbound protocol)

Interface:
```go
type Handler interface {
    InitHandler(workerGrp *models.Workergroup, Wh *Mux, cfg models.Handler)
    Start()
}
```

**What you need to do:**
1. Create a new package under `registry/handlers/` (e.g. `grpchandlers/`).
2. Define a struct that holds whatever your transport needs (server instance, port, adapters list, etc.).
3. Implement `InitHandler` — store the `Mux` reference (you'll call `Wh.InitReq` and `Wh.SetReqFromAdapterInternal` from your endpoint code) and bind your routes / register your gRPC services.
4. Implement `Start` — launch in a goroutine so `main.go` can continue (see the shipped Gin version).
5. Add to the registry in `registry/registry.go`:
   ```go
   registry["GRPCGATEWAY"] = func() interface{} { return &grpchandlers.GRPCHandler{} }
   ```
6. Flip `handler.name` in `config.json` to `"GRPCGATEWAY"`.

You don't modify `main.go` because it already does:
```go
hndlr := registry.CreateInstance(LlmExtGatewayConfig.Handler.Name).(orchestrator.Handler)
hndlr.InitHandler(&v, wh, LlmExtGatewayConfig.Handler)
hndlr.Start()
```

### 6.2 Adding a new Datasource (storage backend)

Interface:
```go
type Datasource interface {
    InitDataSource(cfg models.DataSourceConfig)
    SetContent(reqid string, content *models.Content, workergrp string, workerInd int, sequance int, persistTimeSec int, args ...any) (bool, string)
    SetContentMeta(reqid string, data []byte, workergrp string, persistTimeSec int, args ...any) (bool, string)
    GetLastNContents(reqid string, workergrp string, workerInd int, lastNsequances int, startingSeq int, args ...any) (bool, string)
    GetContentMeta(reqid string, workergrp string, args ...any) (bool, string)
    SetTempAclUserPermissions(aclTempUser, aclTempUserPassword string, readAccessKeys, writeAccessKeys []string, args ...any) (bool, string)
    DBMaintainenceProc(overdueChatIds []string)
}
```

**What you need to do:**
1. Create `registry/datasource/<yourthing>.go`.
2. Implement every method. If your backend doesn't natively do per-user ACLs (e.g. plain Postgres), you can simulate it with row-level security, or issue short-lived JWTs, or simply no-op `SetTempAclUserPermissions` if your threat model allows it.
3. Register under a new name (`"Postgres"`, `"S3"`, etc.).
4. Point `datasource.name` in `config.json` to your new name; add any backend-specific fields to the `DataSourceConfig` struct (they're all `omitempty`, so you won't break anybody else).

The current `RedisDatasource` has two half-implemented methods (`GetLastNContents`, `SetContentMeta`, `GetContentMeta`) returning placeholder success values — a planned extension spot noted by the author. The production path today doesn't go through them because the external cipher/LLM services write content themselves via their JIT credentials.

### 6.3 Adding a new Dispatcher (worker stage)

Interface:
```go
type Dowork interface {
    InitDispatcher(cfg models.Dispatcher)
    Dowork(di *models.DispatcherMandatoryInputs, reqid string, seq int, ds Datasource, synchDS SyncDSForDispatcher, args ...any) (bool, string)
}
```

**What you need to do:**
1. Create `registry/dispatchers/<your>_dispatcher.go`.
2. Implement `InitDispatcher` to cache config (URL, credentials, backoff, retry count, anything your stage-specific logic needs).
3. Implement `Dowork` — this is where the stage's actual business logic lives. You receive:
   - `di` — who you are (worker group, worker index, worker name, thread ID) so the permissions and logging know the caller.
   - `reqid` — the chat ID.
   - `seq` — current sequence number.
   - `ds` — the datasource (Redis). Use `ds.SetTempAclUserPermissions` to mint temp users.
   - `synchDS` — just enough of the synch datasource to call `SetFinalResponseInAdapterStream` (useful if your stage terminates the pipeline for this message, e.g. it detected policy violation).
4. Register under a new protocol name (e.g. `"KAFKA_PRODUCER"`, `"LAMBDA_INVOKE"`, `"PII_SCRUBBER"`).
5. Reference it from any `workers[*].dispatcher.protocol` in `config.json`. Add any new config fields to `models.Dispatcher` (all `omitempty`).

The most important thing `Dowork` must return is `(ok bool, errMsg string)`. If it returns `false`, the worker **does not** ACK the message and does **not** publish to the next stream — the message stays pending and will be reclaimed later. This is how you get at-least-once semantics for free.

---

## 7. File-by-file walkthrough

### `cmd/main.go`

The composition root. Walkthrough:

1. Reads config path from env var `EXT_LLM_GW_CONFIG` (default `./conf/config.json`), `os.ReadFile`, `json.Unmarshal` into `models.LlmExtGwConfig`.
2. Assigns a per-pod unique `models.ProcessID = uuid.New().String()` — used in worker user IDs so that log lines and temp users are traceable per pod.
3. Instantiates `SynchRedisDatasource` directly (it's the one concrete type main.go knows about — this is the one "not pluggable" corner, because streams themselves are the orchestrator's substrate).
4. Calls `registry.CreateInstance(...)` three times: for the datasource, for the handler, and (inside the worker loop) for each dispatcher.
5. Builds the `Mux` with `NewMux`, which also kicks off the 1-minute background session-cleanup ticker.
6. Initializes and starts the handler.
7. Converts `ValRedisMaxSizeInMB` (string) → `ValRedisMaxSizeByte` (int, with `*1024`).
8. Loops over every `Worker` in the config, instantiates the dispatcher for its protocol, creates its worker stream in Redis, and spawns `NumberOfThreads` goroutines via `worker.Start(ctx)`.
9. `select {}` — blocks forever.

### `logger/logger.go`

A tiny structured-ish logger. Levels: `DEBUG < INFO < ERROR < FATAL`. The crucial bit is `WithWorker(*DispatcherMandatoryInputs)` which returns a derived `*Logger` carrying the worker group, worker ID, worker name, and thread ID, so every log line from a worker tick is tagged:

```
[2026/04/22 10:31:05] [DEBUG] [WorkerGroup anonimyzer][WrkThrdID 1-0]: Fetched msgid FromStream 1714000000000-0
```

This is what makes the streaming pipeline debuggable across 2×4 workers × N pods.

### `models/models.go`

The complete type catalog plus every key template. Key constants worth knowing:

```go
const MetadataInternalChat      = "METADATA_"       // METADATA_<chat_id>     → marshalled InitChatRequest
const ReqProcessingSeq          = "SEQ_"            // SEQ_<chat_id>          → next valid request_key
const WorkerStreamTemplateName  = "WS_WorkerGrp-%s_WorkerInd%d"
const KeyPattern                = "seq_%02d_workergroup_%s_wrkind_%02d_reqid_%s"
const MetaReqID                 = "meta_workergroup_%s_reqid_%s"
const PersistanceTimeSeconds    = 300000
const TimeoutSyncMessageSeconds = 6000000000
```

Wire types have extensive comments. `ReqRedisAccessPermissions` is particularly important — it is **the message passed between the gateway and external services**. It carries the Redis URL, username, password, read-access keys, write-access keys, and read/write metadata keys. This struct *is* the permission model.

### `orchestrator/interfaces.go`

Already covered in §6. Also defines:
```go
type SyncDSForDispatcher interface {
    SetFinalResponseInAdapterStream(reqid string, getResponseFromStream *models.GetResponseFromStream) (bool, string)
}
```
This is a **narrowed interface** that is handed to dispatchers. It's deliberately tiny — a dispatcher should not be able to create streams, kill users, or XACK — it should only be able to push a final response into the adapter's stream (typically on error, or in the finalizer's case, on success).

### `orchestrator/workerhandlers.go` (the `Mux`)

Two public entry points called from handlers:

- **`InitReq(data, adapters)`** — validates `adapter_id`, generates a UUID chat_id, writes `SEQ_`, writes `METADATA_`, creates the adapter's `STREAM_` with read-only ACL, returns everything the adapter needs.
- **`SetReqFromAdapterInternal(req)`** — validates that `METADATA_` exists, that the submitted `request_key` equals current `SEQ_`, writes the content, and atomically updates `SEQ_` + publishes to the first worker stream.

Plus the session cleanup goroutine:

- **`cleanupExpiredSessions`** — ticks every minute. Reads expired entries from the `<group>_active_sessions` ZSet (members whose score ≤ now − sessionTTL), calls `utils.PerformCleanup` (deletes every Redis key matching `*<reqid>*` via SCAN+UNLINK), calls `o.DS.DBMaintainenceProc(expiredSessions)` (delegates datasource-specific cleanup — which for Redis also calls `CleanupTempUsers`), and finally removes the entries from the ZSet.
- **`setSessionTimestamp(key)`** — `ZADD` with score = now Unix timestamp. Called on every init and every successful set.

### `orchestrator/workers.go` (the `Worker`)

`NewWorker` wires the dispatcher, its target streams (current and next-in-sequence; `"NA"` for `_FINALIZER_`), and the config knobs (`sleepTimeSec`, `maxRestarts`, `IdleThresholdSec`, `UnackMsgDurationSeconds`) into a `Worker` struct.

`Start(ctx)` spawns a goroutine that loops on `time.Sleep(sleepTimeSec)` and, per tick:
1. Fetches new → dispatches → ACKs and publishes.
2. Reclaims idle → dispatches → ACKs and publishes.
3. Garbage-collects truly-expired pending messages.
4. On panic, increments `restartCount`; after `maxRestarts`, panics up (and takes the whole pod with it — deliberate, because at that point the box is unhealthy and Kubernetes should reschedule).

The inner method `doworkAndACKPublish` parses the stream message (format: `<2-digit seq>_<reqid>`), calls `w.dW.Dowork(...)`, and on success calls `w.Syn.AckAndPublishToNextStreamAtomic(...)`. On dispatcher failure, the message is left unacked on purpose (so another worker can reclaim it). On the atomic-publish call failing, it panics — that is considered a data-integrity failure and halts the worker.

### `orchestrator/synchronizer.go` (the `SynchRedisDatasource`)

This is where all the Redis Streams choreography lives. Every method has a one-line purpose:

| Method                                    | Purpose                                                                                  |
|-------------------------------------------|------------------------------------------------------------------------------------------|
| `InitDataSource`                          | Connect; ping to verify; panic on failure.                                               |
| `SetFinalResponseInAdapterStream`         | XADD to `STREAM_<reqid>` with the `GetResponseFromStream` JSON and the next sequence key.|
| `GetSequanceKey` / `SetSequanceKey`       | Read/write `SEQ_<reqid>`.                                                                |
| `GetRequestMetadata` / `GetRequestPersistTimeSec` / `InitRequest` | Read/write/parse `METADATA_<reqid>`.                                   |
| `AdapterStreamCreation`                   | Create an empty `STREAM_<reqid>` stream and an ACL user scoped to it with `+XREAD +xdel -xadd`. |
| `CreateWorkerStream`                      | `XGROUP CREATE MKSTREAM` for a worker's stream.                                          |
| `SetWorker0Atomic`                        | Lua: set the next `SEQ_` AND XADD to worker-0's stream in one atomic script.             |
| `FetchMessageFromStream`                  | `XREADGROUP` with a 2-second block.                                                      |
| `ClaimNAckFromStream`                     | `XAUTOCLAIM` for idle messages.                                                          |
| `DelOldNAckFromStream`                    | `XPENDING` scan; for each older than `msgAgeThresholdSeconds`, notify the adapter with an `_ERROR_` payload and `XDEL`+`XACK`. |
| `AckAndPublishToNextStreamAtomic`         | Lua: `XACK` + `XDEL` on current stream, and `XADD NOMKSTREAM MINID ~ ...` on next stream — atomically. Special case: if `nextStream == "NA"`, stops after ACK. |

The Lua scripts are worth quoting because they encode the critical-section semantics:

```lua
-- AckAndPublishToNextStreamAtomic (abbreviated)
local acked = redis.call("XACK", KEYS[1], KEYS[2], ARGV[1])
if acked ~= 1 then return 0 end
redis.call("XDEL", KEYS[1], ARGV[1])
if KEYS[3] == "NA" then return "ack_is_successful_in_finalizer" end
local ok, id = pcall(redis.call, "XADD", KEYS[3], "NOMKSTREAM", "MINID", "~", ARGV[3], "*", "message", ARGV[2])
if not ok or not id then return 2 end
return id
```

The `NOMKSTREAM` guard is important: if the next stream does not exist (misconfiguration, or a partial teardown) the XADD fails instead of silently creating a fresh stream that nobody is consuming. The `MINID ~ <threshold>` clause trims old entries from the destination on the way in — a cap on unbounded stream growth.

### `registry/registry.go`

14 lines; already covered. This is the file you edit when you add any new Handler / Datasource / Dispatcher.

### `registry/datasource/redis.go`

Implements the `Datasource` interface for Redis. The interesting function is `SetTempAclUserPermissions`, which composes a variadic `ACL SETUSER` command with `+get` and `+set` selectors restricted to the given key lists:

```go
aclArgs := []string{"SETUSER", aclTempUser, "reset", "on", ">" + aclTempUserPassword}
if len(readAccessKeys) > 0 {
    aclArgs = append(aclArgs, "(+get ~" + strings.Join(readAccessKeys, " ~") + ")")
}
if len(writeAccessKeys) > 0 {
    aclArgs = append(aclArgs, "(+set ~" + strings.Join(writeAccessKeys, " ~") + ")")
}
```

`reset` wipes any pre-existing rights; `on` enables the user; the selectors encode the scoped `get`/`set` verbs with key globs.

### `registry/dispatchers/restapi_dispatcher.go`

The workhorse. Built on top of `gojek/heimdall` for retries + backoff:

```go
backoff := heimdall.NewExponentialBackoff(d.backoffInterval, d.maxBackoff, 2.0, 500*time.Millisecond)
retrier := heimdall.NewRetrier(backoff)
d.client = httpclient.NewClient(
    httpclient.WithHTTPTimeout(d.timeout),
    httpclient.WithRetrier(retrier),
    httpclient.WithRetryCount(d.retryCount),
)
```

`Dowork` branches on `di.WorkerName`:

| WorkerName        | Read keys                                              | Write keys                              | Read/Write meta         |
|-------------------|--------------------------------------------------------|-----------------------------------------|-------------------------|
| `cipher_req`      | prior 1 key at current worker index                    | next sequence at worker index + 1       | R/W the meta key        |
| `decipher_resp`   | prior 1 key at current worker index                    | next sequence at worker index + 1       | R the meta key          |
| `send_to_gemini`  | *interleaved* prior-N user requests and prior-N model responses | next sequence at worker index + 1       | none                    |

`interleave()` is what lets the LLM see a multi-turn history without the gateway having to copy any content around: the cipher stage's outputs and the (previously) gemini stage's outputs are both visible via ACL keys, alternated for presentation ordering.

After `SetTempAclUserPermissions`, the dispatcher POSTs the permission JSON to the configured URL (e.g. `http://host.docker.internal:8211/cipher`) with header `X-User-ID: <reqid>` — the embedded comment explains this is set up for Istio header-based sticky routing so multi-pod deployments can keep a single chat pinned to a single downstream instance where stateful caches may live.

### `registry/dispatchers/redisacl_finalizer.go`

The terminal stage. Almost identical to the RestAPI dispatcher's setup but:
- It does **not** make an outbound HTTP call.
- Its read-access keys point at the last stage's output.
- After minting the ACL user, it calls `synchDS.SetFinalResponseInAdapterStream(reqid, {ReqRedisAccessPermissions: ...})` directly.

This is the single place where the gateway returns "here are credentials to the final content" to the adapter.

### `registry/dispatchers/utils.go`

Two helpers:
- `GenerateKeysList(reqID, group, workerInd, lastN, startSeq)` — returns `lastN` keys going backwards from `startSeq` to `startSeq-lastN+1` (clamped to 0).
- `MergeWriteReadKeys(write, read, writeMeta, readMeta)` — tacks meta keys into their respective lists (used to hand ACL a single unified allowlist).

### `registry/handlers/restapihandlers.go`

Three Gin endpoints:
- `POST /init_chat` — calls `Mux.InitReq`.
- `POST /set_req_from_adapter` — async; calls `SetReqToStream` and returns immediately. **This is the production path** — the caller is expected to be a trusted adapter component that has already authenticated the end-user.
- `POST /set_req_from_adapter_sync` — sync; calls `SetReqToStream`, then `GetContentFromStream` (which blocks on `XREAD` for up to `TimeoutSyncMessageSeconds`) and returns the final deciphered content in the HTTP response. **⚠ Unauthenticated PoC-only endpoint.** It has no auth, no authorization, no rate limiting, and no adapter layer in front of it. It exists so a simple UI or `curl` can exercise the pipeline end-to-end during development. In a real deployment this endpoint must either be disabled at build time or placed behind an authenticating reverse-proxy; the production architecture expects a dedicated **adapter service** to sit between any front-end and this gateway (see §5 and the endpoint summary above).

### `registry/handlers/utils.go`

Two functions:
- `SetReqToStream(data, maxSize, prompt, mux)` — unmarshal `SetRequest`, size check, optional prompt-prepend on the 00-sequence request, hand to `mux.SetReqFromAdapterInternal`.
- `GetContentFromStream(chatID, syn)` — the server-side XREAD. Parses the message, extracts the `ReqRedisAccessPermissions`, opens a **short-lived** Redis client as the JIT user (the conn pool config in `utils.RedisOptionsFromURL` is deliberately `PoolSize=2, MinIdleConns=0, ConnMaxIdleTime=1ms` — so the connection is discarded basically as soon as the call returns), reads the one key it's allowed to read, returns the content.

### `utils/utils.go`

Utility grab-bag:
- `RedisOptionsFromURL` — **short-lived client** options (see above).
- `GRPCToHTTPStatus` — maps gRPC codes to HTTP status codes. The comment on code 6 (`AlreadyExists → 409 Conflict`) marks it as having fixed a prior crash.
- `InitRedisDataSource` — reads the password from the file pointed to by the env var named in `cfg.Password` (e.g. `SYNCH_REDIS_SECRET_FILE`), falls back to `"mysecret"` if the env var is unset. This is how credentials come out of Kubernetes secrets.
- `PerformCleanup(expiredKeys, rdb, ctx)` — `SCAN` for pattern `*<reqid>*` and `UNLINK` the matches.
- `CleanupTempUsers(ctx, rdb, patterns)` — `ACL LIST`, extract usernames, for each pattern match any containing username, `ACL DELUSER` + `CLIENT KILL USER`.

---

## 8. Configuration reference

The full `conf/config.json` has four sections.

```json
{
  "synchdb":     { ... Redis connection for the sync/orchestration DB ... },
  "datasource":  { ... Redis connection for the datasource (registered by name) ... },
  "handler":     { "name": "GINRESTAPI", "port": "7700", "adapters": ["ada1","ada2"] },
  "workergroup": { ... the actual pipeline ... }
}
```

The `workergroup` block is the interesting one:

```json
"workergroup": {
  "max_size_in_mb_of_val_in_redis": "50",
  "workergroup": "anonimyzer",
  "session_ttl_sec": 60,
  "prompt": "For all future interactions ... PER_<string>_SEQ<2-digits>_POS<2-3-digits> ...",
  "workers": [
    { "name": "cipher_req",     "dispatcher": {"protocol": "REDISACL_RESTAPI", ... }, "number_of_threads": 2, ... },
    { "name": "send_to_gemini", "dispatcher": {"protocol": "REDISACL_RESTAPI", ... }, "number_of_threads": 2, ... },
    { "name": "decipher_resp",  "dispatcher": {"protocol": "REDISACL_RESTAPI", ... }, "number_of_threads": 2, ... },
    { "name": "_FINALIZER_",    "dispatcher": {"protocol": "REDISACL_FINALIZER", ...}, "number_of_threads": 2, ... }
  ]
}
```

Relevant per-worker knobs (from `models.Worker`):

- `number_of_threads` — how many goroutines run this worker.
- `sleep_time_sec` — delay between work loops (prevents CPU churn).
- `idle_threshold_sec` — how old a pending message must be before `XAUTOCLAIM` reclaims it.
- `unack_msg_duration_seconds` — how old a pending message must be before it is hard-dropped and the adapter notified of failure.
- `max_restarts` — how many panic-and-recovers the worker accepts before escalating to a full pod panic.

Dispatcher knobs (from `models.Dispatcher`):

- `protocol` — name of the factory in the registry.
- `url`, `http_action` — for REST dispatchers.
- `datasource_url` — the Redis URL that will be embedded in the `ReqRedisAccessPermissions` payload so downstream services can connect.
- `backoff_interval`, `max_backoff`, `retry_count`, `timeout` — Heimdall retry policy.

---

## 9. Data model & Redis key conventions

| Purpose                           | Key / Stream                                             | Owner                      |
|----------------------------------|----------------------------------------------------------|----------------------------|
| Per-chat metadata                | `METADATA_<chat_id>`                                     | Mux (orchestrator)         |
| Next allowed request key         | `SEQ_<chat_id>`                                          | Mux                        |
| Per-stage content                | `seq_<2d>_workergroup_<group>_wrkind_<2d>_reqid_<chat>`   | Dispatchers via JIT users  |
| Per-chat meta for dispatchers    | `meta_workergroup_<group>_reqid_<chat>`                   | `cipher_req` dispatcher    |
| Adapter inbox stream             | `STREAM_<chat_id>`                                        | Mux (created at init)      |
| Worker inbox stream              | `WS_WorkerGrp-<group>_WorkerInd<N>`                       | Main (created at startup)  |
| Active-session tracker ZSet      | `<group>_active_sessions`                                 | Mux                        |

ACL users follow the pattern `<group>_<workerID>_ACL_<chat_id>` and exist only for the duration of a single dispatch; they are destroyed at session cleanup.

---

## 10. Runtime internals: Lua scripts, atomicity, back-pressure

**Two Lua scripts** carry all the atomicity in the system:

1. **`luaSetWorker0Atomic`** (set sequence + enqueue first stage):
   ```lua
   redis.call("SET", KEYS[1], ARGV[1], "EX", ARGV[3])
   return redis.call("XADD", KEYS[2], "MINID", "~", ARGV[4], "*", "message", ARGV[2])
   ```
   Guarantees that the caller can never publish work without also updating `SEQ_`, and vice-versa. Without this, a crash between the two commands would leave the caller able to submit a new request that is immediately accepted (wrong) or unable to submit (also wrong).

2. **`ackAndPublishLua`** (worker → next worker handoff). See §7 for the full source. Guarantees: no message is ACKed without being removed from its stream, no message is handed off without being ACKed first, and a handoff to a non-existent next stream fails loudly instead of succeeding silently.

**Back-pressure** is emergent rather than explicit:
- Workers sleep `sleepTimeSec` between polls. With 2 threads × 4 stages × the poll loop, under load each stream is drained at roughly `(2 threads / poll interval)` messages/sec per stage, so the slowest stage (Gemini) sets the pipeline throughput.
- Upstream streams have unlimited length by default — if the LLM slows dramatically, messages queue on the `send_to_gemini` stream. The `MINID ~ <threshold>` on every XADD trims entries older than `PersistanceTimeSeconds`, so the queue cannot grow without bound forever.
- If the LLM is down long enough that messages exceed `unack_msg_duration_seconds` in the pending list, they are dropped with an error notification to the adapter instead of piling up invisibly.

---

## 11. Session lifecycle, cleanup & TTL

Every successful `init_chat` or `set_req_from_adapter` calls `Mux.setSessionTimestamp(chat_id)` which does a `ZADD <group>_active_sessions <unix-ts> <chat_id>`.

A background goroutine started by `NewMux` wakes every minute:
1. `ZRANGEBYSCORE -inf (now - sessionTTL)` — everything older than TTL.
2. For each expired `chat_id`:
   - `utils.PerformCleanup` — `SCAN *<chat_id>*` then `UNLINK` every matching key (sync DB).
   - `datasource.DBMaintainenceProc([expired...])` — for Redis this also runs the same scan+unlink on the datasource DB, plus `CleanupTempUsers` which `ACL DELUSER`s and `CLIENT KILL`s every ACL user whose name contains the `chat_id`.
   - `ZREM` the chat_id from the active-sessions ZSet.

Configurable via `workergroup.session_ttl_sec` (default `300`).

**Why ZSet + timestamp and not TTL on each key?** Because there are many keys per chat (one per stage per sequence, plus meta, plus sequence, plus stream), and TTL would be cumbersome to keep in sync. A single central "is this chat still active" datum, scanned once a minute, is simpler and makes the cleanup job trivially auditable.

---

## 12. Docker, build & deploy

### `Dockerfile`

```dockerfile
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY ext-llm-gateway/go.sum ./
COPY ext-llm-gateway .
COPY ext-llm-gateway/go.mod.docker ./go.mod
RUN cat go.mod
RUN go mod download
RUN go build -o server ./cmd/main.go
EXPOSE 7800
CMD ["./server"]
```

Notable details:
- Uses `go.mod.docker` instead of the repo's `go.mod`. The two are identical in shipped state, but this indirection lets you pin a different dependency set for containerized builds — useful if private replace directives exist in dev.
- `EXPOSE 7800` is informational (the actual port comes from `handler.port` in config; `restart_session_mng.sh` uses `-p 7700:7700` because the default config is `7700`).

### `restart_session_mng.sh`

Dev convenience. Runs from the **parent** directory of the repo:

```bash
cd ..
docker stop  ext-llm-gateway
docker container rm ext-llm-gateway
docker build -t ext-llm-gateway -f ext-llm-gateway/Dockerfile .
docker run -d --link my-redis2:redis --name ext-llm-gateway -p 7700:7700 ext-llm-gateway
```

The `--link my-redis2:redis` is the reason the config uses `addr: "redis:6379"`: `my-redis2` is the already-running Redis container, linked into this container under the hostname `redis`.


### `sample_redis.go`

Out of scope of the actual gateway — this is a standalone demo file (it has its own `func main`). It illustrates the same Redis Streams + ACL + Lua pattern in a minimal form. It exists as documentation-by-example; it is not compiled into the gateway.

---

## 13. Manual REST API exercises

### Sample payloads & curl commands

`tests/restapi/` contains ready-to-use JSON payloads illustrating the full conversation the shipped anonymizer pipeline is designed for:

| File                             | Scenario                                                                  |
|----------------------------------|---------------------------------------------------------------------------|
| `initchat.json`                  | Open a new chat as `ada1` / `sss@ff.com`                                  |
| `set_req_existing_chtid.json`    | First turn: "John Carter at Acme Corp's New York office / Datadog"        |
| `set_req2_existing_chtid.json`   | Follow-up turn: "Emily Rodriguez from TechNova in San Francisco"          |
| `set_req3_existing_chtid.json`   | Recall turn: "Who were the people names that were mentioned?"             |
| `set_req4_existing_chtid.json`   | Calculation turn: "provide a new date string +30years"                    |
| `set_req5_existing_chtid.json`   | Trivial turn: "capitalize the word beautiful"                             |
| `set_req_wrong_chtid.json`       | Negative path: unknown chat_id — should return an error                   |
| `commands_to_execute.sh`         | `curl -X POST -H "Content-Type: application/json" -d @... http://localhost:7700/...` one-liners |

Typical flow:
```bash
# 1. Open a chat (keep the chat_id and next_req_key from the response)
curl -X POST -H "Content-Type: application/json" \
  -d @tests/restapi/initchat.json \
  http://localhost:7700/init_chat

# 2. Patch the chat_id / request_key into a set_req_* file, then:
curl -X POST -H "Content-Type: application/json" \
  -d @tests/restapi/set_req_existing_chtid.json \
  http://localhost:7700/set_req_from_adapter
```

---

## 14. Observability (the logger)

- Global, initialized in `logger.init()`.
- Default level is `DEBUG` (change via `SetLevel("INFO")`).
- Worker-tagged logger lines prepend `[WorkerGroup anonimyzer][WrkThrdID <wid>-<tid>]` so you can grep a specific thread's narrative across a noisy multi-pod log stream.
- Every `Fatal` both logs and returns the formatted string — used in panic paths so the panic value carries the same log line (`panic(logger.Log.WithWorker(...).Fatal(...))`).

---

## 15. Istio / multi-pod routing note

Every outbound request from `RestAPIDispatcher.Dowork` sets `X-User-ID: <reqid>`. The embedded comment documents the intended Istio recipe:

1. Label specific pods in your deployment (e.g. `version: targeted`).
2. Create a `DestinationRule` grouping those labeled pods into a subset.
3. Create a `VirtualService` with `match.headers["X-User-ID"]`.
4. Point matching traffic at the subset.

This makes a given chat_id stick to a specific downstream pod — useful if the cipher or LLM adapter keeps per-chat state (e.g. a local person→token map cache).

---

## 16. Extending — worked examples

### Example A: add a policy-check stage in front of the LLM

You want to reject prompts that mention certain banned topics before they reach Gemini.

1. Create `registry/dispatchers/policy_dispatcher.go` with a struct holding your banned-topic list and URL of a policy service.
2. Implement `InitDispatcher` (copy the Heimdall retry setup from `restapi_dispatcher.go` if you're calling out) and `Dowork` (call the service; on violation, mint an ACL with no write keys, call `synchDS.SetFinalResponseInAdapterStream(reqid, &GetResponseFromStream{ErrorString: "policy_violation"})`, return `(true, "")` so the message is ACKed — the chain is effectively short-circuited at this point since the next stream won't be published to… actually *will* be, because the worker's post-Dowork calls `AckAndPublishToNextStreamAtomic`. If you want a true short-circuit, return `(false, ...)` and rely on the adapter having already received the error message on its own stream; the pending message will eventually be GC'd by `DelOldNAckFromStream`. Better: introduce a sentinel return value if you want first-class short-circuit semantics.)
3. Register: `registry["POLICY_CHECK"] = func() interface{} { return &dispatchers.PolicyDispatcher{} }`.
4. Insert a new worker between `cipher_req` and `send_to_gemini` in `config.json` with `"protocol": "POLICY_CHECK"`.

No other code changes.

### Example B: swap Redis for Postgres as the datasource

1. Create `registry/datasource/postgres.go`, struct `PostgresDatasource`, implement the seven `Datasource` methods.
2. For `SetTempAclUserPermissions`, either issue temporary database roles (Postgres supports `CREATE ROLE ... VALID UNTIL`) or issue short-lived JWTs and have the downstream services authenticate with them. The returned credentials in `ReqRedisAccessPermissions` will need to change shape (or you introduce a parallel struct for Postgres-style creds).
3. Register `registry["Postgres"] = ...`.
4. Flip `datasource.name` in `config.json`.

Note that the `synchdb` is hard-coded to `SynchRedisDatasource` in `cmd/main.go` — if you also want to replace the orchestration substrate, that's a bigger change (streams and XAUTOCLAIM are load-bearing).

### Example C: add a gRPC handler

1. Create `registry/handlers/grpchandlers/grpc.go`.
2. Define a proto with `InitChat`, `SetRequest`, `SetRequestSync` RPCs mirroring the current HTTP endpoints.
3. Implement the `Handler` interface: `InitHandler` stores the `*Mux` and starts a gRPC server; `Start` runs it. Each RPC translates the incoming message and calls `Wh.InitReq` / `SetReqToStream` / `GetContentFromStream` with the same `Mux`/`Syn` the HTTP handler uses.
4. Register, flip config, done.

---

## 17. Glossary

- **Adapter** — the client that opens a chat, submits prompts, and reads responses. Identified by `adapter_id` (must be in the handler's `adapters` allow-list).
- **Chat ID** / **internal_chat_id** / **reqid** — UUID uniquely identifying one conversation. Used throughout every Redis key and stream name. (The codebase uses all three terms, usually interchangeably.)
- **Sequence key** — the token (`NN_<uuid>`) that the adapter must send as `request_key` on each `set_req_from_adapter` call. Prevents replay and out-of-order turns.
- **Worker group** — a named pipeline (here, `anonimyzer`). One gateway binary hosts exactly one.
- **Worker** — one stage of the pipeline (e.g. `cipher_req`). Has `number_of_threads` concurrent goroutines.
- **Dispatcher** — the business logic *inside* a worker. Workers are generic (fetch / reclaim / GC); dispatchers do the actual work.
- **Handler** — the inbound protocol (Gin HTTP, gRPC, etc.).
- **Datasource** — the storage backend for content (Redis today).
- **SynchRedisDatasource** — the orchestration/queue substrate. Separate from the datasource because it is architecturally special (the worker loop depends on its stream semantics).
- **JIT ACL user** — a Redis user created by a dispatcher for one message, scoped to just the keys that message's downstream service will touch. Destroyed at session cleanup.

---

## Appendix: Dependency inventory

From `go.mod`:

| Package                                       | Why                                                           |
|-----------------------------------------------|---------------------------------------------------------------|
| `github.com/gin-gonic/gin`                    | HTTP router for the GINRESTAPI handler.                       |
| `github.com/gojek/heimdall/v7`                | HTTP client with retries, backoff, timeouts for dispatchers.  |
| `github.com/google/uuid`                      | Chat IDs, sequence keys, JIT user passwords, process ID.      |
| `github.com/redis/go-redis/v9`                | Redis client for synch + datasource + JIT ACL management.     |
| `google.golang.org/grpc`                      | Only for the `codes` package — status codes mapped to HTTP by `utils.GRPCToHTTPStatus`. No actual gRPC server today. |

---

*This README was generated by analyzing the full source tree of `ext-llm-gateway` (every file marked between `==== start` and `==== End` in `combined-gateway3.txt`), including the orchestrator comments that describe the design reasoning.*
