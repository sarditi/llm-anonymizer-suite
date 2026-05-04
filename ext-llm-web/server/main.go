// Tiny static-file server for the compiled Flutter web bundle. We
// deliberately avoid pulling in nginx so the runtime image stays scratch-thin
// and the whole thing is one binary that's trivial to ship to Kubernetes.
//
// Two responsibilities:
//
//  1. Serve everything under /web/ as static assets, with an SPA fallback to
//     index.html for unknown paths so Flutter's in-app routing keeps working
//     after a hard refresh.
//
//  2. Intercept GET /config.js and emit a window.EXT_LLM_WEB_CONFIG object
//     populated from environment variables, so the same compiled bundle can
//     point at any webadapter URL without rebuilding.
//
// Configuration (all optional):
//
//   PORT                       listen port (default 8080)
//   WEBADAPTER_BASE_URL        forwarded to the SPA as webadapterBaseUrl
//   APP_TITLE                  forwarded to the SPA as appTitle
//   STATIC_DIR                 directory to serve (default /app/web)
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	port := envOr("PORT", "8080")
	staticDir := envOr("STATIC_DIR", "/app/web")
	baseURL := envOr("WEBADAPTER_BASE_URL", "http://localhost:9555")
	appTitle := envOr("APP_TITLE", "ext-llm-web")

	if _, err := os.Stat(staticDir); err != nil {
		log.Fatalf("static dir %q is not accessible: %v", staticDir, err)
	}

	fileServer := http.FileServer(http.Dir(staticDir))

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Runtime config injection. The static config.js shipped in the bundle is
	// intentionally a placeholder; this handler always wins.
	mux.HandleFunc("/config.js", func(w http.ResponseWriter, _ *http.Request) {
		cfg := map[string]string{
			"webadapterBaseUrl": baseURL,
			"appTitle":          appTitle,
		}
		raw, _ := json.Marshal(cfg)
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "window.EXT_LLM_WEB_CONFIG = %s;\n", string(raw))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Resolve the requested path against the static dir; if it doesn't
		// exist (or it's a directory without an index.html), fall back to
		// index.html so SPA deep-links work after a refresh.
		clean := filepath.Clean(r.URL.Path)
		if clean == "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		full := filepath.Join(staticDir, clean)
		if !strings.HasPrefix(full, staticDir) {
			http.NotFound(w, r)
			return
		}
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback.
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})

	addr := ":" + port
	log.Printf("ext-llm-web serving %s on %s (webadapter=%s)", staticDir, addr, baseURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
