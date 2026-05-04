// Runtime configuration. The static server (server/main.go) overrides this
// response on /config.js when WEBADAPTER_BASE_URL is set, so the Flutter
// bundle never needs to be rebuilt to point at a different backend.
window.EXT_LLM_WEB_CONFIG = {
  webadapterBaseUrl: "http://localhost:9555",
  appTitle: "ext-llm-web"
};
