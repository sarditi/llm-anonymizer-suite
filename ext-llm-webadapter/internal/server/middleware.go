package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"ext-llm-webadapter/internal/auth"
	"ext-llm-webadapter/internal/config"
	"ext-llm-webadapter/internal/models"
)

const (
	contextUsername = "ctx_username"
	contextRequest  = "ctx_req_id"
)

// AuthMiddleware enforces a valid Bearer JWT and stashes the resolved username
// onto the gin context.
func AuthMiddleware(tm *auth.TokenManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			abortWithError(c, http.StatusUnauthorized, "missing_authorization", "Authorization header is required")
			return
		}
		parts := strings.SplitN(raw, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			abortWithError(c, http.StatusUnauthorized, "invalid_authorization", "Authorization header must be 'Bearer <token>'")
			return
		}
		claims, err := tm.Verify(strings.TrimSpace(parts[1]))
		if err != nil {
			abortWithError(c, http.StatusUnauthorized, "invalid_token", "token is invalid or expired")
			return
		}
		c.Set(contextUsername, claims.Username())
		c.Next()
	}
}

// CORSMiddleware emits CORS headers based on the supplied config. The config
// is resolved up-front (config.json defaults → env-var overrides) so this
// middleware itself is environment-agnostic; redeploying to Kubernetes only
// requires changing a ConfigMap or Deployment env var, never code.
//
// Behaviour:
//   - If AllowedOrigins contains "*" the middleware allows any origin. When
//     AllowCredentials=true it cannot literally emit "*" (the browser
//     rejects that combination), so it reflects the request's Origin header
//     instead and adds Vary: Origin.
//   - Otherwise the request's Origin must be in AllowedOrigins (exact match,
//     case-sensitive); if it is, that origin is echoed back; if not, no
//     Access-Control-Allow-Origin header is sent and the browser blocks the
//     response. This mirrors the behaviour of the Go cors libraries without
//     pulling one in.
//   - Preflight (OPTIONS) requests short-circuit with 204 once the headers
//     are set.
func CORSMiddleware(cfg config.CORSConfig) gin.HandlerFunc {
	allowAny := false
	allowedSet := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if o == "*" {
			allowAny = true
			continue
		}
		allowedSet[o] = struct{}{}
	}

	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAgeSeconds)

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		if origin != "" {
			switch {
			case allowAny && cfg.AllowCredentials:
				// Browsers reject ACAO=* with credentials.
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			case allowAny:
				c.Header("Access-Control-Allow-Origin", "*")
			default:
				if _, ok := allowedSet[origin]; ok {
					c.Header("Access-Control-Allow-Origin", origin)
					c.Header("Vary", "Origin")
				}
			}
		}

		if methods != "" {
			c.Header("Access-Control-Allow-Methods", methods)
		}
		if headers != "" {
			c.Header("Access-Control-Allow-Headers", headers)
		}
		if cfg.AllowCredentials {
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if cfg.MaxAgeSeconds > 0 {
			c.Header("Access-Control-Max-Age", maxAge)
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func abortWithError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, models.ErrorResponse{
		Status:  "error",
		Code:    code,
		Message: message,
	})
}
