package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type ServerConfig struct {
	Port             string `json:"port"`
	ReadTimeoutSec   int    `json:"read_timeout_sec"`
	WriteTimeoutSec  int    `json:"write_timeout_sec"`
	ShutdownGraceSec int    `json:"shutdown_grace_sec"`
}

type UserCredentials struct {
	Username       string `json:"username"`
	PasswordBcrypt string `json:"password_bcrypt"`
}

type AuthConfig struct {
	// JWTSecretEnv names the env var that points at the file containing the
	// HS256 signing secret. If the env var is unset or empty, JWTSecretInline
	// is used as a fallback (intended for local dev only).
	JWTSecretEnv    string            `json:"jwt_secret_env"`
	JWTSecretInline string            `json:"jwt_secret_inline,omitempty"`
	JWTTTLSeconds   int               `json:"jwt_ttl_seconds"`
	Issuer          string            `json:"issuer"`
	Users           []UserCredentials `json:"users"`
}

type GatewayConfig struct {
	BaseURL               string `json:"base_url"`
	AdapterID             string `json:"adapter_id"`
	DefaultLLM            string `json:"default_llm"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
}

type RedisConfig struct {
	Addr               string `json:"addr"`
	StreamBlockSeconds int    `json:"stream_block_seconds"`
	DialTimeoutSeconds int    `json:"dial_timeout_seconds"`
}

type SessionsConfig struct {
	// Time-to-live for an entry in the in-memory chat-session store.
	TTLSeconds int `json:"ttl_seconds"`
}

// CORSConfig drives the CORS middleware. Every field is overridable via env
// vars (CORS_ALLOWED_ORIGINS, CORS_ALLOWED_METHODS, CORS_ALLOWED_HEADERS,
// CORS_ALLOW_CREDENTIALS, CORS_MAX_AGE_SECONDS) so a Kubernetes Deployment
// can change behaviour without touching code or rebuilding the image.
type CORSConfig struct {
	// AllowedOrigins is the explicit list of allowed origins (e.g.
	// ["https://app.example.com"]). A single "*" entry means "allow any
	// origin" — but that combination is incompatible with
	// AllowCredentials=true (the middleware will reflect the request's
	// Origin in that case to keep browsers happy).
	AllowedOrigins []string `json:"allowed_origins"`

	AllowedMethods   []string `json:"allowed_methods"`
	AllowedHeaders   []string `json:"allowed_headers"`
	AllowCredentials bool     `json:"allow_credentials"`
	MaxAgeSeconds    int      `json:"max_age_seconds"`
}

type Config struct {
	Server   ServerConfig   `json:"server"`
	Auth     AuthConfig     `json:"auth"`
	Gateway  GatewayConfig  `json:"gateway"`
	Redis    RedisConfig    `json:"redis"`
	Sessions SessionsConfig `json:"sessions"`
	CORS     CORSConfig     `json:"cors"`
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = "./conf/config.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.applyDefaults()
	c.applyEnvOverrides()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Port == "" {
		c.Server.Port = "9555"
	}
	if c.Server.ReadTimeoutSec == 0 {
		c.Server.ReadTimeoutSec = 30
	}
	if c.Server.WriteTimeoutSec == 0 {
		c.Server.WriteTimeoutSec = 6000
	}
	if c.Server.ShutdownGraceSec == 0 {
		c.Server.ShutdownGraceSec = 10
	}
	if c.Auth.JWTTTLSeconds == 0 {
		c.Auth.JWTTTLSeconds = 3600
	}
	if c.Auth.Issuer == "" {
		c.Auth.Issuer = "ext-llm-webadapter"
	}
	if c.Gateway.RequestTimeoutSeconds == 0 {
		c.Gateway.RequestTimeoutSeconds = 6000
	}
	if c.Gateway.DefaultLLM == "" {
		c.Gateway.DefaultLLM = "chatgpt"
	}
	if c.Redis.StreamBlockSeconds == 0 {
		c.Redis.StreamBlockSeconds = 6000
	}
	if c.Redis.DialTimeoutSeconds == 0 {
		c.Redis.DialTimeoutSeconds = 5
	}
	if c.Sessions.TTLSeconds == 0 {
		c.Sessions.TTLSeconds = 3600
	}
	if len(c.CORS.AllowedOrigins) == 0 {
		c.CORS.AllowedOrigins = []string{"*"}
	}
	if len(c.CORS.AllowedMethods) == 0 {
		c.CORS.AllowedMethods = []string{"GET", "POST", "OPTIONS"}
	}
	if len(c.CORS.AllowedHeaders) == 0 {
		c.CORS.AllowedHeaders = []string{"Authorization", "Content-Type"}
	}
	if c.CORS.MaxAgeSeconds == 0 {
		c.CORS.MaxAgeSeconds = 600
	}
}

// applyEnvOverrides lets a Kubernetes Deployment / Docker `-e` flag override
// individual CORS knobs without changing the mounted config.json. Empty env
// vars are ignored so callers can leave them unset.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("CORS_ALLOWED_ORIGINS"); v != "" {
		c.CORS.AllowedOrigins = splitCSV(v)
	} else if v := os.Getenv("CORS_ALLOWED_ORIGIN"); v != "" {
		// Backwards-compatible alias for the old single-origin env var.
		c.CORS.AllowedOrigins = []string{strings.TrimSpace(v)}
	}
	if v := os.Getenv("CORS_ALLOWED_METHODS"); v != "" {
		c.CORS.AllowedMethods = splitCSV(v)
	}
	if v := os.Getenv("CORS_ALLOWED_HEADERS"); v != "" {
		c.CORS.AllowedHeaders = splitCSV(v)
	}
	if v := os.Getenv("CORS_ALLOW_CREDENTIALS"); v != "" {
		c.CORS.AllowCredentials = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v := os.Getenv("CORS_MAX_AGE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			c.CORS.MaxAgeSeconds = n
		}
	}
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *Config) Validate() error {
	if len(c.Auth.Users) == 0 {
		return errors.New("auth.users must contain at least one user")
	}
	for i, u := range c.Auth.Users {
		if strings.TrimSpace(u.Username) == "" {
			return fmt.Errorf("auth.users[%d].username is empty", i)
		}
		if strings.TrimSpace(u.PasswordBcrypt) == "" {
			return fmt.Errorf("auth.users[%d].password_bcrypt is empty", i)
		}
	}
	if c.Gateway.BaseURL == "" {
		return errors.New("gateway.base_url is required")
	}
	if c.Gateway.AdapterID == "" {
		return errors.New("gateway.adapter_id is required")
	}
	if c.Redis.Addr == "" {
		return errors.New("redis.addr is required")
	}
	return nil
}

// ResolveJWTSecret reads the signing secret from the file pointed to by the
// configured env var; falls back to the inline value when the env var is unset.
// Returns an error when neither source provides a non-empty secret.
func (c *Config) ResolveJWTSecret() ([]byte, error) {
	if c.Auth.JWTSecretEnv != "" {
		if path := os.Getenv(c.Auth.JWTSecretEnv); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read jwt secret file %s: %w", path, err)
			}
			secret := strings.TrimSpace(string(data))
			if secret == "" {
				return nil, fmt.Errorf("jwt secret file %s is empty", path)
			}
			return []byte(secret), nil
		}
	}
	if strings.TrimSpace(c.Auth.JWTSecretInline) == "" {
		return nil, errors.New("no jwt secret available (set jwt_secret_env or jwt_secret_inline)")
	}
	return []byte(c.Auth.JWTSecretInline), nil
}

func (c *Config) JWTTTL() time.Duration {
	return time.Duration(c.Auth.JWTTTLSeconds) * time.Second
}

func (c *Config) GatewayTimeout() time.Duration {
	return time.Duration(c.Gateway.RequestTimeoutSeconds) * time.Second
}

func (c *Config) RedisStreamBlock() time.Duration {
	return time.Duration(c.Redis.StreamBlockSeconds) * time.Second
}

func (c *Config) RedisDialTimeout() time.Duration {
	return time.Duration(c.Redis.DialTimeoutSeconds) * time.Second
}

func (c *Config) SessionTTL() time.Duration {
	return time.Duration(c.Sessions.TTLSeconds) * time.Second
}
