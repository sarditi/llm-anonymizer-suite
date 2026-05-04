package server

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// BuildRouter wires the routes; exposed independently from BuildServer so
// tests can use it with httptest without touching ports or sockets.
func BuildRouter(d *Deps) *gin.Engine {
	if os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())

	r.Use(CORSMiddleware(d.CORS))

	api := r.Group("/api/v1")

	api.GET("/health", d.Health)
	api.POST("/auth/login", d.Login)

	authed := api.Group("")
	authed.Use(AuthMiddleware(d.Tokens))
	authed.POST("/auth/refresh", d.Refresh)
	authed.POST("/chat/init", d.InitChat)
	authed.POST("/chat/message", d.SendMessage)

	return r
}

// BuildServer returns an *http.Server bound to the configured port. The caller
// is responsible for starting and shutting it down.
func BuildServer(port string, d *Deps) *http.Server {
	return &http.Server{
		Addr:    ":" + port,
		Handler: BuildRouter(d),
	}
}
