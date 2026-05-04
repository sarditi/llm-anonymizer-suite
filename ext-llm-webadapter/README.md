# ext-llm-webadapter

A Go REST adapter that sits between any web/CLI/API client and the
`ext-llm-gateway`. It is a concrete implementation of the *adapter tier*
described in §2.5 of the suite-level README (the conceptual layer that owns
end-user authN/Z, rate limiting, stream-reader ownership and credential
custody — none of which the gateway is responsible for).

This service is also the *correct implementation of the synchronous chat
turn*. The gateway ships a `/set_req_from_adapter_sync` endpoint as a PoC
shortcut so the bundled `llm-frontend` can talk to it directly. That sync
shortcut belongs in an adapter, not in the gateway: the adapter is the
component that holds the per-chat `stream_pwd`, owns the `XREAD` loop on
`STREAM_<internal_chat_id>`, and dereferences the JIT credentials the pipeline
publishes there. With this adapter deployed the gateway's sync endpoint can
be turned off (build-tag gated to `DEV_MODE`) and the test frontend can be
deleted — every real client calls the adapter instead.

---

## Table of contents

1. [What this service does](#1-what-this-service-does)
2. [Why it exists](#2-why-it-exists)
3. [Public REST API](#3-public-rest-api)
4. [End-to-end flow of a single turn](#4-end-to-end-flow-of-a-single-turn)
5. [Authentication model](#5-authentication-model)
6. [Configuration](#6-configuration)
7. [Running locally](#7-running-locally)
8. [Running in Docker](#8-running-in-docker)
9. [Wiring a frontend](#9-wiring-a-frontend)
10. [Testing](#10-testing)
11. [Operational considerations / gaps](#11-operational-considerations--gaps)

---

## 1. What this service does

- Exposes a small JSON REST API on port `9555` (configurable).
- Authenticates callers with **username + bcrypt-hashed password** stored in
  `conf/config.json` and issues a short-lived **HS256 JWT**.
- Forwards authenticated chat requests to the gateway's *async*
  endpoints (`/init_chat` and `/set_req_from_adapter`).
- Reads `STREAM_<internal_chat_id>` itself to pick up the gateway's pipeline reply.
- Dereferences the JIT Redis credentials the gateway places on that stream
  to fetch the final, deciphered content from the data-source Redis.
- Returns that content to the caller as a synchronous HTTP response — so
  callers never see Redis, never hold credentials, and never need to
  understand the pipeline.

The service is intentionally stateless except for an in-memory chat
session map (`internal_chat_id → owner_username + stream_pwd`) that expires on a
TTL. The session map is the only thing that prevents this from being
horizontally scalable today; see §11.

## 2. Why it exists

§2.5 of the suite-level README explains why an adapter is required: the
gateway's authentication surface is a static `adapter_id` allow-list, and
nothing else. End-user identity, OIDC, rate-limits, tenant isolation,
stream-reader ownership and JIT-credential dereferencing all have to live
elsewhere. This service is *one* such adapter — the simple, JWT-flavoured
one suitable for browser/CLI clients that want short-turn synchronous
calls.

The shipped `llm-frontend` test UI talks directly to the gateway's PoC
sync endpoint and hard-codes `adapter_id="ada1"`. With this adapter in
place, a real frontend talks to **only** the adapter (`/api/v1/...`) and
the gateway's sync back-door becomes deletable.

## 3. Public REST API

All paths are prefixed with `/api/v1`. All bodies are JSON; non-2xx
responses always have shape:

```json
{ "status": "error", "code": "...", "message": "..." }
```

| Method | Path                | Auth   | Purpose                                       |
|--------|---------------------|--------|-----------------------------------------------|
| GET    | `/health`           | none   | Liveness probe.                               |
| POST   | `/auth/login`       | none   | Exchange username/password for a JWT.         |
| POST   | `/auth/refresh`     | Bearer | Re-issue a JWT with a new expiry.             |
| POST   | `/chat/init`        | Bearer | Open a new chat session against the gateway.  |
| POST   | `/chat/message`     | Bearer | Send one chat turn; returns the model reply.  |

### `POST /auth/login`

Request:

```json
{ "username": "alice", "password": "s3cret" }
```

Response (`200`):

```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_at": 1714501234,
  "username": "alice"
}
```

`401` for any unknown user or bad password — the response is
intentionally identical for both cases.

### `POST /auth/refresh`

Authorization: `Bearer <token>`. Returns a fresh token with the same
shape as `/auth/login`. Refused (`401`) if the supplied token is expired
or signed with a different secret.

### `POST /chat/init`

Authorization: `Bearer <token>`. Optional body:

```json
{ "llm": "chatgpt", "persist_time_second": 7200 }
```

Response (`200`):

```json
{
  "status": "success",
  "internal_chat_id": "<uuid>",
  "set_next_req_key": "00_<seq 00 key uuid>"
}
```

Internally calls the gateway's `/init_chat`, which returns a `stream_pwd`
that authorises XREAD on `STREAM_<internal_chat_id>`. **The adapter never returns
that password to the client.** It is stashed server-side in the in-memory
session store and used on subsequent `/chat/message` calls.

### `POST /chat/message`

Authorization: `Bearer <token>`. Body:

```json
{
  "internal_chat_id": "<uuid>",
  "request_key": "00_<seq 00 key uuid>",
  "content": { "input_text": "Hello, world." }
}
```

Response (`200`):

```json
{
  "status": "success",
  "internal_chat_id": "<uuid>",
  "set_next_req_key": "01_<seq 01 key uuid>",
  "content": { "input_text": "Hi! How can I help?" }
}
```

Status codes worth knowing about:

| Code | Meaning                                                                |
|------|------------------------------------------------------------------------|
| 400  | Malformed request body or sequence-key check failed at the gateway.    |
| 401  | Missing / invalid / expired Bearer token.                              |
| 403  | The supplied `internal_chat_id` belongs to a different authenticated user.      |
| 404  | Unknown / expired chat session — caller must call `/chat/init` again.  |
| 502  | Gateway returned an error while accepting the request.                 |
| 504  | Pipeline did not publish a reply within the configured timeout.        |

### `GET /health`

Returns `{ "status": "ok", "service": "ext-llm-webadapter", "version": "..." }`.

## 4. End-to-end flow of a single turn

```
client                adapter                 gateway              redis (sync)
  |  POST /auth/login   |                        |                      |
  |-------------------->| verify bcrypt          |                      |
  |  JWT                | issue HS256 JWT        |                      |
  |<--------------------|                        |                      |
  |                     |                        |                      |
  |  POST /chat/init    |                        |                      |
  |-------------------->| POST /init_chat ------>| AdapterStreamCreation|
  |                     |  ada1 + user_id        | -- STREAM_<id> -->   |
  |                     |<-- internal_chat_id, key,       |                      |
  |                     |    stream_pwd          |                      |
  |                     | store (internal_chat_id ->      |                      |
  |                     |   {alice, stream_pwd}) |                      |
  |   internal_chat_id, key      |                        |                      |
  |<--------------------|                        |                      |
  |                     |                        |                      |
  |  POST /chat/message |                        |                      |
  |-------------------->| lookup session         |                      |
  |                     | POST /set_req_from_    |                      |
  |                     |   adapter ------------>| pipeline runs        |
  |                     |<-- {success, ack}      | XADD STREAM_<id> --> |
  |                     | XREAD STREAM_<id>      |                      |
  |                     |   as adapter_id /pwd <--------------------    |
  |                     | parse jit creds        |                      |
  |                     | dial redis as jit user |                      |
  |                     | GET <content key>   ----------------------->  |
  |                     |   reply <-----------------------------------  |
  |  reply, next_key    |                        |                      |
  |<--------------------|                        |                      |
```

This is exactly the work the gateway's `/set_req_from_adapter_sync`
handler does today, but moved to the adapter where it belongs. With this
service deployed:

- The gateway's sync endpoint is unused and can be removed (or build-tag
  gated).
- The Redis stream password is never exposed outside the adapter
  process.
- The JIT credentials the gateway places on `STREAM_<internal_chat_id>` are
  consumed inside the adapter and discarded; the client's HTTP response
  carries only the deciphered content.

## 5. Authentication model

- **Storage.** Users live in `auth.users[]` of `config.json`. Passwords
  are stored as bcrypt hashes; plaintext is never accepted by the loader.
  Use the bundled `cmd/hashpw` helper to generate hashes:

  ```
  go run ./cmd/hashpw 's3cret'
  # $2a$10$....
  ```

- **Login.** `/auth/login` verifies the bcrypt hash and issues an HS256
  JWT signed with the secret resolved from
  `auth.jwt_secret_env` → `auth.jwt_secret_inline`. Use the file-via-env
  approach in production; the inline value is for local dev only.

- **Continued connection.** Subsequent calls use
  `Authorization: Bearer <token>`. Tokens carry `iss`, `sub`, `iat`,
  `exp`, `nbf`. Verification rejects:
  - Anything signed with `alg=none` (explicit allow-list of HS256 only).
  - Wrong issuer (locked to `auth.issuer`).
  - Tokens past `exp`, or before `nbf`.
  - Tokens signed with a different secret.

- **Refresh.** `/auth/refresh` accepts a still-valid token and returns a
  new one with a fresh `exp`. There is intentionally no refresh-token
  flow — the tradeoff is that long sessions require periodic refresh,
  which keeps the implementation small and avoids storing a
  refresh-token table.

- **Authorization on chat endpoints.** Each chat session is bound to the
  username that opened it. `/chat/message` rejects (`403`) any caller
  whose JWT subject differs from the session owner.

## 6. Configuration

`conf/config.json` — overridable via `WEBADAPTER_CONFIG` env var or the
`-config` flag.

```json
{
  "server": {
    "port": "9555",
    "read_timeout_sec": 30,
    "write_timeout_sec": 6000,
    "shutdown_grace_sec": 10
  },
  "auth": {
    "jwt_secret_env": "WEBADAPTER_JWT_SECRET_FILE",
    "jwt_secret_inline": "dev-only-change-me-please-32+chars",
    "jwt_ttl_seconds": 3600,
    "issuer": "ext-llm-webadapter",
    "users": [
      { "username": "admin", "password_bcrypt": "$2a$10$..." }
    ]
  },
  "gateway": {
    "base_url": "http://host.docker.internal:7700",
    "adapter_id": "ada1",
    "default_llm": "chatgpt",
    "request_timeout_seconds": 6000
  },
  "redis": {
    "addr": "redis:6379",
    "stream_block_seconds": 6000,
    "dial_timeout_seconds": 5
  },
  "sessions": {
    "ttl_seconds": 3600
  },
  "cors": {
    "allowed_origins": ["*"],
    "allowed_methods": ["GET", "POST", "OPTIONS"],
    "allowed_headers": ["Authorization", "Content-Type"],
    "allow_credentials": false,
    "max_age_seconds": 600
  }
}
```

Notes:

- `auth.jwt_secret_env` is the **name** of the env var that points at a
  file containing the signing secret (matches the gateway's
  `REDIS_SECRET_FILE` convention). The inline value is a fallback used
  only if the env var is unset/empty.
- `gateway.adapter_id` must be in the gateway's `handler.adapters`
  allow-list (`adapters: ["ada1","ada2"]` in the shipped gateway config).
- `redis.addr` is the **synchronous** Redis address — the same one the
  gateway uses for `STREAM_<internal_chat_id>`. The adapter authenticates against
  it as `gateway.adapter_id` with the per-chat `stream_pwd` returned by
  `/init_chat`.
- `sessions.ttl_seconds` is the in-memory session TTL. Set it generously
  if you expect users to leave a tab open between turns.
- `cors.*` is documented in detail in §6.1.

The shipped config has one default user, **`admin`**, whose password is
literally `admin`. Replace it before exposing the service to anyone.

### 6.1 CORS

The CORS middleware is fully driven by configuration; nothing about the
allowed-origins policy is baked into the binary. K8s redeploys flip CORS
behaviour by changing a ConfigMap or Deployment env var — never code, never
a rebuild.

| `cors` field        | Type       | Default                              | Purpose                                                                  |
| ------------------- | ---------- | ------------------------------------ | ------------------------------------------------------------------------ |
| `allowed_origins`   | `[]string` | `["*"]`                              | Exact-match list. A single `"*"` allows any origin (see notes below).    |
| `allowed_methods`   | `[]string` | `["GET","POST","OPTIONS"]`           | Sent as `Access-Control-Allow-Methods`.                                  |
| `allowed_headers`   | `[]string` | `["Authorization","Content-Type"]`   | Sent as `Access-Control-Allow-Headers`.                                  |
| `allow_credentials` | `bool`     | `false`                              | Sets `Access-Control-Allow-Credentials: true` and forces origin echo.    |
| `max_age_seconds`   | `int`      | `600`                                | Sets `Access-Control-Max-Age` (preflight cache).                         |

Behaviour:

- **Exact-match list.** When `allowed_origins` is a list of concrete URLs
  (e.g. `["https://app.example.com"]`), only requests whose `Origin` header
  matches one of them get an `Access-Control-Allow-Origin` response header;
  everything else is silently denied by the browser. The middleware also
  emits `Vary: Origin` so caches don't pollute responses across origins.
- **Wildcard.** `["*"]` allows any origin. With `allow_credentials=true`
  the literal `*` is illegal per the CORS spec, so the middleware reflects
  the request's `Origin` instead and sets `Vary: Origin`.
- **Preflight (`OPTIONS`)** short-circuits with `204 No Content` after
  emitting headers, so it does not reach handler code or auth middleware.

#### Env-var overrides (highest priority)

Any of the JSON fields can be overridden at startup via env vars, which is
the recommended K8s/Docker path because it avoids editing the mounted
ConfigMap. Empty/unset env vars fall back to the JSON value (or its
default).

| Env var                  | Type             | Example                                          |
| ------------------------ | ---------------- | ------------------------------------------------ |
| `CORS_ALLOWED_ORIGINS`   | comma-separated  | `https://app.example.com,https://admin.example.com` |
| `CORS_ALLOWED_ORIGIN`    | single (alias)   | `https://app.example.com` *(legacy fallback)*    |
| `CORS_ALLOWED_METHODS`   | comma-separated  | `GET,POST,OPTIONS`                               |
| `CORS_ALLOWED_HEADERS`   | comma-separated  | `Authorization,Content-Type,X-Request-Id`        |
| `CORS_ALLOW_CREDENTIALS` | `true`/`false`   | `true`                                           |
| `CORS_MAX_AGE_SECONDS`   | int              | `3600`                                           |

#### Kubernetes example

```yaml
# ConfigMap consumed by the Deployment
apiVersion: v1
kind: ConfigMap
metadata:
  name: ext-llm-webadapter-cors
data:
  CORS_ALLOWED_ORIGINS: "https://app.example.com,https://admin.example.com"
  CORS_ALLOW_CREDENTIALS: "false"
  CORS_MAX_AGE_SECONDS: "3600"
---
# In the Deployment spec:
spec:
  template:
    spec:
      containers:
        - name: ext-llm-webadapter
          envFrom:
            - configMapRef:
                name: ext-llm-webadapter-cors
```

#### Quick docker example

```bash
docker run -d --rm -p 9555:9555 \
    -e CORS_ALLOWED_ORIGINS="http://localhost:8766" \
    -e CORS_ALLOW_CREDENTIALS=false \
    ext-llm-webadapter
```

#### Smoke test

```bash
# preflight (OPTIONS) must return 204 with the right headers
curl -sS -o /dev/null -D - -X OPTIONS http://localhost:9555/api/v1/chat/init \
  -H "Origin: http://localhost:8766" \
  -H "Access-Control-Request-Method: POST" \
  -H "Access-Control-Request-Headers: Authorization,Content-Type"
# → HTTP/1.1 204 No Content
# → Access-Control-Allow-Origin: http://localhost:8766
# → Access-Control-Allow-Methods: GET, POST, OPTIONS
# → Access-Control-Allow-Headers: Authorization, Content-Type
# → Access-Control-Max-Age: 600
```

> **Same-origin alternative.** If you put `ext-llm-web` and the webadapter
> behind the same Ingress (the SPA at `/` and the webadapter at
> `/api/v1/*` — see [`ext-llm-web/k8s/ingress.example.yaml`](../ext-llm-web/k8s/ingress.example.yaml)),
> the browser never makes a cross-origin call and CORS is moot — leave the
> defaults alone.

## 7. Running locally

Prerequisites: Go 1.23+, an `ext-llm-gateway` reachable at the URL in
`gateway.base_url`, and the same Redis the gateway uses reachable at
`redis.addr`.

```bash
# from llm-anonymizer-suite/
cd ext-llm-webadapter
go mod download
go run ./cmd
```

A successful start prints:

```
[INFO]: ext-llm-webadapter listening on :9555 (gateway=..., adapter_id=ada1)
```

Smoke-test:

```bash
curl -s http://localhost:9555/api/v1/health | jq
# {"status":"ok","service":"ext-llm-webadapter","version":"dev"}

# admin / admin (default in conf/config.json — change before deploying)
TOKEN=$(curl -s -X POST http://localhost:9555/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | jq -r .token)

curl -s -X POST http://localhost:9555/api/v1/chat/init \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{}' | jq
```

## 8. Running in Docker

```bash
# from llm-anonymizer-suite/
docker build -t ext-llm-webadapter -f ext-llm-webadapter/Dockerfile .

docker stop ext-llm-webadapter 2>/dev/null || true
docker rm   ext-llm-webadapter 2>/dev/null || true

docker run -d \
  --name ext-llm-webadapter \
  --link my-redis2:redis \
  -p 9555:9555 \
  ext-llm-webadapter
```

A `restart_session_mng.sh` is provided that wraps these commands.

For production, mount a JWT secret file and point the env var at it:

```bash
docker run -d \
  --name ext-llm-webadapter \
  --link my-redis2:redis \
  -v $(pwd)/secrets:/secrets:ro \
  -e WEBADAPTER_JWT_SECRET_FILE=/secrets/webadapter_jwt_secret \
  -p 9555:9555 \
  ext-llm-webadapter
```

The shipped `Dockerfile` copies the `cmd/hashpw` helper into the image
too, so you can produce bcrypt hashes against the same Go runtime your
service uses:

```bash
docker run --rm --entrypoint /app/hashpw ext-llm-webadapter 'my-new-password'
```

## 9. Wiring a frontend

The adapter is intentionally framework-agnostic; any browser/Node/CLI
client that can do JSON over HTTP can use it. A minimal browser flow:

```js
// 1. login once
const { token } = await (await fetch('/api/v1/auth/login', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ username, password })
})).json();
sessionStorage.setItem('token', token);

// 2. open a chat
const headers = {
  'Content-Type': 'application/json',
  Authorization: `Bearer ${token}`,
};
const init = await (await fetch('/api/v1/chat/init', {
  method: 'POST', headers, body: '{}',
})).json();
let chatId = init.internal_chat_id;
let nextKey = init.set_next_req_key;

// 3. send turns; each turn updates nextKey
async function send(text) {
  const r = await (await fetch('/api/v1/chat/message', {
    method: 'POST', headers,
    body: JSON.stringify({
      internal_chat_id: chatId, request_key: nextKey,
      content: { input_text: text },
    }),
  })).json();
  nextKey = r.set_next_req_key;
  return r.content.input_text;
}
```

The shipped `ext-llm-frontend` can be retargeted at this adapter by
changing its API base URL from `/api` (which today reverse-proxies to
the gateway's PoC sync endpoint) to the adapter's `/api/v1` and adding
the `Authorization: Bearer ...` header. That's the recommended migration
path before deleting `ext-llm-frontend` entirely.

## 10. Testing

The service has three layers of tests, all runnable with stock `go
test`. None of them require an external Redis, gateway, or LLM —
gateway calls go through `httptest`, and Redis is stubbed by
`miniredis`.

```bash
# from ext-llm-webadapter/
go test ./...           # runs everything

go test ./internal/... -run TestTokenManager -v
go test ./tests/integration -v
```

Coverage by package:

| Package                    | What's covered                                                         |
|----------------------------|------------------------------------------------------------------------|
| `internal/auth`            | Bcrypt hash/verify; JWT issue/parse; expired/wrong-secret/wrong-issuer/`alg=none` rejection. |
| `internal/sessions`        | Put/Get/Delete; owner enforcement; TTL expiry; cleanup; touch-on-read.  |
| `internal/config`          | Default values; validation; JWT-secret resolution from file & inline.   |
| `internal/gateway`         | `/init_chat` happy path; incomplete-payload rejection; gateway-error propagation; `/set_req_from_adapter` happy path; 5xx handling. |
| `internal/streamreader`    | XREAD round-trip with miniredis; pipeline-error path; timeout.          |
| `internal/server`          | Health (no-auth); login success/bad-pw/unknown-user/malformed-body; refresh; chat init / send round-trip; unknown-chat 404; cross-user 403; pipeline-error 504; gateway-error 502; expired-token rejection. |
| `tests/integration`        | Full login → init_chat → send_message flow with a fake gateway and a real miniredis stand-in for `STREAM_<internal_chat_id>` and the JIT-credentialed content key. |

## 11. Operational considerations / gaps

This service is small on purpose; the items below are deliberately out
of scope for the first cut and are the right things to add next.

- **In-memory session store.** Chat sessions live in a per-process map
  with a TTL. That's fine for a single instance; for HA / multi-pod
  deployments, swap the `sessions.Store` implementation for a Redis
  backend. The interface is small (Put/Get/Delete/Cleanup) on purpose.
- **Single-tenant user table.** Users come from `config.json`. Real
  deployments should swap `auth.UserStore` for a database-backed or
  OIDC-backed lookup; the interface is one method (`Authenticate`).
- **No rate limiting.** A future middleware should enforce per-user and
  per-IP caps; this is the natural place for it (the gateway has no
  business knowing about end-user identities).
- **No streaming responses.** `/chat/message` blocks for the full
  pipeline, exactly like the gateway's PoC sync endpoint did. Adding SSE
  would require partial-content support in the gateway pipeline first
  (see §6.3 of the suite README).
- **No refresh-token flow.** Issued JWTs are short-lived and can be
  refreshed while still valid; once expired the user logs in again. No
  server-side revocation list.
- **No PHI/PII handling.** Per organizational policy, do not send PHI
  to the adapter or the underlying pipeline; redact at the client.
