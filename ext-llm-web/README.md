# ext-llm-web

Flutter web frontend for the `ext-llm-webadapter`. Login, multiple
concurrently-managed chat sessions, and a composer that is locked while a
turn is in flight (so you can't send a new prompt until the previous reply
arrives).

The bundle is served by a tiny self-contained Go static-file server (no
nginx). The container image is therefore ~20 MB, runs as a non-root user, and
takes its backend URL from an env var so the same image can be redeployed to
any environment without rebuilding.

## Layout

```
ext-llm-web/
├── lib/                 Dart sources (Flutter app)
│   ├── api/             HTTP client for the webadapter
│   ├── models/          wire-level DTOs matching ext-llm-common/models
│   ├── screens/         login, chat
│   ├── widgets/         chat list, message bubble, composer
│   └── state/           AppState (provider) + runtime config bridge
├── web/                 Flutter web shell (index.html, manifest, config.js)
├── server/              Go static-file server (single binary)
├── k8s/                 Deployment / Service / ConfigMap / example Ingress
├── Dockerfile           Three-stage: flutter build → go build → alpine runtime
└── restart_session_mng.sh  Local: stop+rebuild+run the container
```

## Prerequisites

- For local dev with `flutter run`: Flutter SDK ≥ 3.22 (`flutter --version`).
- For the container build / deploy paths: just Docker — the Dockerfile pins a
  Flutter SDK version and downloads it into the build stage.
- A reachable `ext-llm-webadapter` (port 9555 by default).

## Configuration

The frontend reads its backend URL from `window.EXT_LLM_WEB_CONFIG`, which the
static server emits at `GET /config.js` based on env vars:

| Variable              | Default                  | Purpose                                       |
| --------------------- | ------------------------ | --------------------------------------------- |
| `PORT`                | `8080`                   | Listen port inside the container              |
| `STATIC_DIR`          | `/app/web`               | Where the Flutter bundle lives                |
| `WEBADAPTER_BASE_URL` | `http://localhost:9555`  | Forwarded to the SPA as `webadapterBaseUrl`   |
| `APP_TITLE`           | `ext-llm-web`            | Forwarded to the SPA as `appTitle`            |

Because the bundle is configured at runtime, the same image works in any
environment. The Flutter app falls back to `http://localhost:9555` if no
config is provided.

## Run locally with the Flutter dev server

Useful for fast UI iteration; talks directly to a webadapter you already have
running on your laptop.

```bash
cd ext-llm-web
flutter pub get
flutter run -d chrome \
    --dart-define-from-file=/dev/null \
    --web-port 8766
```

The app will pick up `web/config.js` at `http://localhost:8766/config.js`,
which by default points at `http://localhost:9555`. Edit `web/config.js` if
your webadapter lives elsewhere.

CORS note: the webadapter does not currently emit `Access-Control-Allow-*`
headers, so a dev-mode browser will block cross-origin calls between
`localhost:8766` (Flutter) and `localhost:9555` (webadapter). For local
end-to-end testing, prefer the container path below (which serves the SPA
from the same container that proxies your XHRs back to the webadapter), or
launch Chrome with `--disable-web-security` *for development only*.

## Run locally as a container

The build context is the suite root, the same convention used by the other
services in this repo.

```bash
# from /Users/<you>/gptanonym/llm-anonymizer-suite
docker build -t ext-llm-web -f ext-llm-web/Dockerfile .
docker run --rm -p 8766:8080 \
    -e WEBADAPTER_BASE_URL=http://localhost:9555 \
    -e APP_TITLE=ext-llm-web \
    ext-llm-web
```

> `WEBADAPTER_BASE_URL` is consumed by your **browser**, not by the container.
> `host.docker.internal` only resolves from inside a Docker container, so
> using it here causes `ERR_NAME_NOT_RESOLVED`. Use `http://localhost:<port>`
> (or a hostname your browser can actually resolve).

> **CORS.** When the SPA and the webadapter are on different origins (e.g.
> `localhost:8766` and `localhost:9555`), the browser will preflight every
> request. The webadapter has a config-driven CORS middleware — point it at
> this SPA's origin via env, no code/image rebuild needed:
>
> ```bash
> docker run -d --rm -p 9555:9555 \
>     -e CORS_ALLOWED_ORIGINS="http://localhost:8766" \
>     ext-llm-webadapter
> ```
>
> See [`ext-llm-webadapter/README.md` §6.1](../ext-llm-webadapter/README.md#61-cors)
> for the full list of env vars and the K8s ConfigMap pattern. Same-origin
> deploys (Ingress in front of both services) skip CORS entirely.

Open http://localhost:8766 — you should see the login screen.

The included helper does the same flow plus a stop/remove first:

```bash
./ext-llm-web/restart_session_mng.sh
```

Override `HOST_PORT` or `WEBADAPTER_BASE_URL` via env vars if needed:

```bash
HOST_PORT=9999 \
WEBADAPTER_BASE_URL=http://host.docker.internal:9555 \
    ./ext-llm-web/restart_session_mng.sh
```

## Build the bundle without Docker

```bash
cd ext-llm-web
flutter pub get
flutter build web --release   # output in build/web/

cd server
go build -o /tmp/ext-llm-web-server .
STATIC_DIR=$PWD/../build/web \
WEBADAPTER_BASE_URL=http://localhost:9555 \
PORT=8766 \
    /tmp/ext-llm-web-server
```

## Deploy to Kubernetes

The manifests under `k8s/` are intentionally minimal so they deploy unmodified
on most clusters; tighten them per your environment.

```bash
# load the image (kind/minikube example) — or push to your registry first
docker build -t ext-llm-web -f ext-llm-web/Dockerfile .
kind load docker-image ext-llm-web    # or: minikube image load ext-llm-web

kubectl apply -f ext-llm-web/k8s/configmap.yaml
kubectl apply -f ext-llm-web/k8s/deployment.yaml
kubectl apply -f ext-llm-web/k8s/service.yaml

# (optional) expose at an Ingress that also proxies /api/v1 → webadapter
kubectl apply -f ext-llm-web/k8s/ingress.example.yaml
```

Smoke test:

```bash
kubectl port-forward svc/ext-llm-web 8766:80
# open http://localhost:8766
```

The ConfigMap drives the runtime config — change `WEBADAPTER_BASE_URL` and
restart the Deployment to swap backends without rebuilding the image:

```bash
kubectl edit configmap ext-llm-web-config
kubectl rollout restart deploy/ext-llm-web
```

### Same-origin pattern (recommended for prod)

If your Ingress also fronts the webadapter at the same host (see
`ingress.example.yaml`), set `WEBADAPTER_BASE_URL` to a relative origin so the
browser never makes a cross-origin call:

```yaml
data:
  WEBADAPTER_BASE_URL: ""   # empty = same origin
```

(With this set, the Flutter `WebadapterClient` will issue requests to
`/api/v1/...` on the same host that served the SPA.)

## How the chat flow maps to the webadapter

| User action            | Webadapter call                       | Notes                                                                    |
| ---------------------- | ------------------------------------- | ------------------------------------------------------------------------ |
| Sign in                | `POST /api/v1/auth/login`             | Token persisted in `localStorage` for refresh-survival                   |
| Click **New chat**     | `POST /api/v1/chat/init`              | Stores `chat_id` + `set_next_req_key` in the session                     |
| Send a message         | `POST /api/v1/chat/message`           | Synchronous; composer disabled until response or error                   |
| Sign out               | (client-only)                         | Clears token + session list                                              |

The "no second prompt while one is pending" guarantee is enforced per
session: each `ChatSession` has an `inFlight` flag flipped while
`POST /chat/message` is outstanding, and the composer's `enabled` is bound to
that flag (see `lib/widgets/message_input.dart`).

## Smoke commands cheatsheet

```bash
# rebuild + run the container
./ext-llm-web/restart_session_mng.sh

# tail logs
docker logs -f ext-llm-web

# quick HTTP probe
curl -fsS http://localhost:8766/healthz       # → ok
curl -fsS http://localhost:8766/config.js     # → window.EXT_LLM_WEB_CONFIG = {...}
```
