# `llm-frontend` — Minimal Test UI for the LLM Anonymizer Suite

> ⚠️ **This is a lightweight, test-purpose-only UI.** It is not a product. It has **no authentication, no authorization, no user management, and no production hardening**. It exists so a developer can visually verify that the anonymizer pipeline round-trips prompts end-to-end. Do not deploy it as a user-facing application.

---

## What this is

A tiny React (Vite) single-page app, served by Nginx, that lets you type prompts into a ChatGPT-style window and see the final deciphered response coming back from the `ext-llm-gateway`. Total signal-carrying code: about 90 lines of JSX and 35 lines of Nginx config.

## What this is *not*

- **Not an adapter.** In the production architecture of the gateway, any end-user-facing surface (web UI, CLI, chatbot, email ingestion) must go through a dedicated **adapter service** that authenticates the user, maps them to an `adapter_id`, holds the async `STREAM_<chat_id>` read loop, and enforces policy / quotas / rate limits.
- **Not secure.** This UI **bypasses the adapter layer entirely** by calling the gateway's **`/set_req_from_adapter_sync`** endpoint — a synchronous, unauthenticated "back door" the gateway ships for PoC/testing exactly like this one. There are no cookies, no tokens, no sessions, no rate limiting, no input validation beyond what the browser does for you. Anyone who can reach port 8766 can send arbitrary prompts.
- **Not scalable.** The sync endpoint holds the HTTP connection open for the entire duration of the LLM round-trip (cipher → Gemini → decipher → finalize). Nginx is configured with `proxy_read_timeout 6000s` to accommodate this. It works for one developer poking at the pipeline; it doesn't work for real traffic.

If you want to build a real front-end, use this as a reference only, and write an adapter service that sits between your UI and the gateway's **async** endpoints (`/init_chat` + `/set_req_from_adapter` + `XREAD STREAM_<chat_id>`).

---

## How it works

```
    browser  ──►  Nginx (:8766)  ──►  ext-llm-gateway (:7700)
               │                  │
               ├── /llmtest ──► static React bundle
               └── /api/    ──► proxy to gateway
```

Two routes proxied by Nginx:

- **`/llmtest`** — serves the built React SPA from `/usr/share/nginx/html/` (the Vite build output).
- **`/api/`** — reverse-proxies to `http://host.docker.internal:7700/` where the gateway is listening. The UI's JS calls `fetch('/api/init_chat')` and `fetch('/api/set_req_from_adapter_sync')` which Nginx forwards to the gateway's real endpoints.
- **`/`** — a 301 redirect to `/llmtest`.

## Flow in the UI

1. User clicks **+ New Chat** → `POST /api/init_chat` with a hard-coded body `{llm: "chatgpt", user_id: "sss@ff.com", adapter_id: "ada1"}`. The gateway returns a `chat_id` and a `set_next_req_key`, which the SPA stores in React state.
2. User types a message and submits → `POST /api/set_req_from_adapter_sync` with the stored `chat_id` + `request_key` + `{content: {input_text: userText}}`.
3. The sync endpoint blocks until the full pipeline (cipher → Gemini → decipher → finalizer → adapter stream) produces a result, then returns it inline.
4. The SPA reads `data.content.input_text` and renders it as the assistant's reply; updates `nextKey` from `data.set_next_req_key` for the following turn.
5. A tiny `renderText` helper splits on newlines and bolds `**bold**` spans — that's the full extent of the "rich text" support.

That's the whole UI. No state management library, no router, no CSS framework — just inline styles and `useState`.

## File layout

```
llm-frontend/
├── index.html            # Vite entry HTML
├── nginx.conf            # Nginx server block: serves SPA and proxies /api/
└── src/
    ├── main.jsx          # ReactDOM root render
    └── App.jsx           # The entire app: state, fetch, rendering
```

## Hard-coded values to know about

- **`adapter_id: "ada1"`** in `App.jsx` — must match one of the adapters in the gateway's `config.json → handler.adapters`.
- **`user_id: "sss@ff.com"`** — placeholder; the gateway doesn't validate it.
- **`API_BASE = "/api"`** — same-origin prefix, relies on the Nginx proxy.
- **Port `8766`** in `nginx.conf` — the public port this container exposes.
- **`http://host.docker.internal:7700/`** in `nginx.conf` — the gateway's address from inside the container. Requires Docker Desktop or equivalent host networking.

## Build & run

The repo ships only sources; you build with the standard Vite toolchain. A typical `Dockerfile` (not shipped in the combined archive, but trivially reconstructible) would:

1. `npm ci && npm run build` in a `node:alpine` builder stage.
2. Copy the `dist/` output into `/usr/share/nginx/html/` of an `nginx:alpine` stage.
3. Copy `nginx.conf` to `/etc/nginx/conf.d/default.conf`.
4. `EXPOSE 8766`.

Then run mapped to the gateway:

```bash
docker run -d --name llm-frontend -p 8766:8766 llm-frontend
# browser → http://localhost:8766/llmtest
```

The container reaches the gateway via `host.docker.internal:7700`, so the gateway must be running and listening on port 7700 on the Docker host.

## Summary

This is a ~125-line developer-only UI that shortcuts every security layer the production architecture mandates, by design, for a tiny feedback loop when iterating on the pipeline. Keep it off the open internet, keep it out of production, and remember that its existence is the reason the gateway's `/set_req_from_adapter_sync` endpoint exists at all.
