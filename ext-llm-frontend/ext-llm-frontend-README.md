# ext-llm-frontend

**A lightweight, simplified React test UI for the ext-llm-gateway**

---

## Overview

`ext-llm-frontend` is a **super simplified React single-page application** built exclusively for **testing and demonstration purposes**.

It provides a minimal chat interface that directly interacts with the `ext-llm-gateway` backend, bypassing normal adapter authentication and authorization flows. 

The UI uses the gateway’s **backdoor synchronous endpoint** (`/set_req_from_adapter_sync`) to send messages and receive responses. This allows rapid testing of the full anonymization + LLM pipeline (cipher → Gemini → decipher) without implementing a full production adapter.

**Important**: This frontend is **not intended for production use**. It is a lightweight test tool only.

---

## Key Characteristics

- Extremely minimal React + Vite-style setup (no complex state management, no authentication, no error resilience).
- Single chat session support with basic message history.
- Hardcoded initialization parameters (`llm: "chatgpt"`, dummy `user_id` and `adapter_id`).
- Uses the gateway’s sync endpoint to guarantee strict request ordering and immediate response.
- Simple markdown-like rendering for bold text (`**text**`).
- Served behind Nginx for easy local testing alongside the Go backend.

---

## Architecture & Request Flow

1. **Chat Initialization** (`POST /api/init_chat`)
   - Calls the gateway to create a new chat session.
   - Receives `chat_id`, `set_next_req_key`, and stores them locally.

2. **Sending Messages** (`POST /api/set_req_from_adapter_sync`)
   - Sends user input directly using the pre-fetched `nextKey`.
   - The gateway processes the request through the full pipeline:
     - `cipher_req` → anonymization via `ext-cipher-service`
     - `send_to_gemini` → LLM call
     - `decipher_resp` → restoration of original names
     - `_FINALIZER_` → returns final response
   - Updates `nextKey` for the next message to maintain strict sequencing.

3. **Rendering**
   - Basic left/right message bubbles.
   - Simple line-break and bold text support via custom `renderText()` function.

**Note on Backdoor Usage**:  
This frontend deliberately skips the normal adapter registration and permission flow. It uses the internal synchronous endpoint provided by `ext-llm-gateway` for testing convenience. In a real deployment, a proper adapter would use the asynchronous `/set_req_from_adapter` endpoint with proper Redis ACL credentials and stream handling.

---

## Project Structure

```text
ext-llm-frontend/
├── src/
│   ├── main.jsx          # React root entry point
│   └── App.jsx           # Main chat component + all business logic
├── index.html            # Basic HTML template
├── nginx.conf            # Nginx configuration for serving frontend + proxying API
└── combined-frontend.txt # Single-file bundle of the entire project
```
### Important Files & Functions
- src/App.jsx – Contains all logic:
    - startNewChat() – Initializes a new chat via /init_chat
    - sendMessage() – Sends user input via /set_req_from_adapter_sync and updates UI
    - renderText() – Simple client-side rendering for newlines and **bold** text

- nginx.conf – Serves the React build at /llmtest and proxies all /api/* requests to the Go gateway running on port 7700.
- index.html – Minimal entry point that loads the React app.

---

## Deployment

The frontend is designed to run behind Nginx in a containerized environment together with the ext-llm-gateway backend.
Typical usage:

* Build and run the full stack (gateway + frontend) using Docker Compose or the provided scripts.
* Access the test UI at http://localhost:8766/llmtest (or simply http://localhost:8766 which redirects).

### Nginx responsibilities:

* Serves static React files from /usr/share/nginx/html/
* Proxies API calls from /api/ to the Go backend (host.docker.internal:7700)
* Handles long-lived LLM responses with extended timeouts

---

## Limitations & Test-Only Nature

* Lightweight only: No loading indicators, no proper error recovery, no multi-chat support, no persistence.
* No security: Hardcoded dummy credentials and direct use of internal endpoints.
* Bypasses adapter layer: Skips authentication, Redis ACL setup, and stream-based async handling.
* For testing only: Intended solely for developers to quickly verify the cipher → LLM → decipher pipeline end-to-end.

Production frontends or mobile adapters should implement proper adapter registration, use the asynchronous endpoint, and handle Redis Streams directly.