package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ext-llm-common/logger"
	"ext-llm-webadapter/internal/auth"
	"ext-llm-webadapter/internal/config"
	"ext-llm-webadapter/internal/gateway"
	"ext-llm-webadapter/internal/server"
	"ext-llm-webadapter/internal/sessions"
	"ext-llm-webadapter/internal/streamreader"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfgPath := flag.String("config", envOrDefault("WEBADAPTER_CONFIG", "./conf/config.json"), "path to config.json")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		logger.Log.Fatal("load config: " + err.Error())
	}

	secret, err := cfg.ResolveJWTSecret()
	if err != nil {
		logger.Log.Fatal("resolve jwt secret: " + err.Error())
	}

	tokens := auth.NewTokenManager(secret, cfg.JWTTTL(), cfg.Auth.Issuer)
	users := auth.NewUserStore(cfg.Auth.Users)
	gwClient := gateway.NewClient(cfg.Gateway.BaseURL, cfg.Gateway.AdapterID, cfg.Gateway.DefaultLLM, cfg.GatewayTimeout())
	store := sessions.New(cfg.SessionTTL())
	reader := streamreader.New(cfg.Redis.Addr, cfg.RedisDialTimeout(), cfg.RedisStreamBlock())

	deps := &server.Deps{
		Users:     users,
		Tokens:    tokens,
		Gateway:   gwClient,
		Sessions:  store,
		Reader:    reader,
		AdapterID: cfg.Gateway.AdapterID,
		Version:   version,
		CORS:      cfg.CORS,
	}

	srv := server.BuildServer(cfg.Server.Port, deps)
	srv.ReadTimeout = time.Duration(cfg.Server.ReadTimeoutSec) * time.Second
	srv.WriteTimeout = time.Duration(cfg.Server.WriteTimeoutSec) * time.Second

	go runSessionCleanup(store, cfg.SessionTTL())

	go func() {
		logger.Log.Info("ext-llm-webadapter listening on :" + cfg.Server.Port + " (gateway=" + cfg.Gateway.BaseURL + ", adapter_id=" + cfg.Gateway.AdapterID + ")")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Log.Fatal("http server: " + err.Error())
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	logger.Log.Info("shutdown signal received, draining...")

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.ShutdownGraceSec)*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Log.Error("shutdown: " + err.Error())
	}
}

func runSessionCleanup(store *sessions.Store, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	interval := ttl / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		if dropped := store.Cleanup(); dropped > 0 {
			logger.Log.Debug("session cleanup dropped expired entries")
		}
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
