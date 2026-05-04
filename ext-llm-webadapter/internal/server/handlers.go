package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"ext-llm-common/logger"
	commonmodels "ext-llm-common/models"
	"ext-llm-webadapter/internal/auth"
	"ext-llm-webadapter/internal/config"
	gw "ext-llm-webadapter/internal/gateway"
	"ext-llm-webadapter/internal/models"
	"ext-llm-webadapter/internal/sessions"
)

// StreamReader is the small surface we need from the streamreader package;
// extracted as an interface so the handlers can be tested with a fake.
type StreamReader interface {
	AwaitContent(ctx context.Context, chatID, adapterID, streamPwd string) (*commonmodels.Content, string, error)
}

// Deps bundles everything the handlers need.
type Deps struct {
	Users    *auth.UserStore
	Tokens   *auth.TokenManager
	Gateway  *gw.Client
	Sessions *sessions.Store
	Reader   StreamReader

	// AdapterID is the value passed to the gateway on init_chat — also the
	// Redis ACL username the adapter authenticates as when reading stream
	// messages back.
	AdapterID string

	// Version is reported via /health.
	Version string

	// CORS is resolved at startup (config.json defaults → env-var overrides)
	// and handed to the CORS middleware. K8s redeploys flip these via
	// ConfigMap/env, never code.
	CORS config.CORSConfig
}

const serviceName = "ext-llm-webadapter"

// Login authenticates a user and returns a signed JWT.
func (d *Deps) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortWithError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := d.Users.Authenticate(req.Username, req.Password); err != nil {
		logger.Log.Error("login failed for username=" + req.Username)
		abortWithError(c, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	token, expiresAt, err := d.Tokens.Issue(req.Username)
	if err != nil {
		logger.Log.Error("token issue failed: " + err.Error())
		abortWithError(c, http.StatusInternalServerError, "token_issue_failed", "could not issue token")
		return
	}
	c.JSON(http.StatusOK, models.LoginResponse{
		Token:     token,
		TokenType: "Bearer",
		ExpiresAt: expiresAt.Unix(),
		Username:  req.Username,
	})
}

// Refresh re-issues a JWT with a fresh expiry. Requires a still-valid token in
// the Authorization header (handled by AuthMiddleware).
func (d *Deps) Refresh(c *gin.Context) {
	username := c.GetString(contextUsername)
	if username == "" || !d.Users.Has(username) {
		abortWithError(c, http.StatusUnauthorized, "unknown_user", "user no longer exists")
		return
	}
	token, expiresAt, err := d.Tokens.Issue(username)
	if err != nil {
		abortWithError(c, http.StatusInternalServerError, "token_issue_failed", "could not issue token")
		return
	}
	c.JSON(http.StatusOK, models.LoginResponse{
		Token:     token,
		TokenType: "Bearer",
		ExpiresAt: expiresAt.Unix(),
		Username:  username,
	})
}

// InitChat asks the gateway for a fresh internal_chat_id and stashes the resulting
// stream_pwd server-side, so the client never has to handle Redis credentials.
func (d *Deps) InitChat(c *gin.Context) {
	username := c.GetString(contextUsername)

	var req commonmodels.InitChatRequest
	_ = c.ShouldBindJSON(&req) // body is optional

	resp, err := d.Gateway.InitChat(c.Request.Context(), username, req.PersistanceTimeSeconds)
	if err != nil {
		logger.Log.Error("gateway init_chat failed: " + err.Error())
		abortWithError(c, http.StatusBadGateway, "gateway_init_failed", err.Error())
		return
	}

	d.Sessions.Put(resp.ChatID, username, resp.StreamPwd)

	c.JSON(http.StatusOK, commonmodels.InitChatResponse{
		Status:        "success",
		ChatID:        resp.ChatID,
		SetNextReqKey: resp.SetNextReqKey,
		Message:       resp.Message,
		//StreamPwd:     resp.StreamPwd,
	})
}

// SendMessage is the synchronous turn-handling endpoint: it forwards the
// prompt to the gateway, waits on STREAM_<internal_chat_id> for the pipeline reply,
// dereferences the JIT credentials to fetch the deciphered content, and
// returns it together with the next request key.
func (d *Deps) SendMessage(c *gin.Context) {
	username := c.GetString(contextUsername)

	var req models.SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		abortWithError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	sess, err := d.Sessions.Get(req.ChatID, username)
	if err != nil {
		switch {
		case errors.Is(err, sessions.ErrSessionNotFound):
			abortWithError(c, http.StatusNotFound, "chat_not_found", "chat session is unknown or expired; call /chat/init first")
		case errors.Is(err, sessions.ErrSessionForbidden):
			abortWithError(c, http.StatusForbidden, "forbidden", "this chat session belongs to a different user")
		default:
			abortWithError(c, http.StatusInternalServerError, "session_lookup_failed", err.Error())
		}
		return
	}

	if _, err := d.Gateway.SetReqFromAdapter(c.Request.Context(), commonmodels.SetRequest{
		ReqKey:         req.RequestKey,
		InternalChatID: req.ChatID,
		Content:        req.Content,
	}); err != nil {
		logger.Log.Error("gateway set_req failed: " + err.Error())
		abortWithError(c, http.StatusBadGateway, "gateway_send_failed", err.Error())
		return
	}

	content, nextSeq, err := d.Reader.AwaitContent(c.Request.Context(), sess.ChatID, d.AdapterID, sess.StreamPwd)
	if err != nil {
		logger.Log.Error("await content failed for internal_chat_id=" + sess.ChatID + ": " + err.Error())
		abortWithError(c, http.StatusGatewayTimeout, "pipeline_failed", err.Error())
		return
	}

	c.JSON(http.StatusOK, models.SendMessageResponse{
		Status:        "success",
		ChatID:        sess.ChatID,
		SetNextReqKey: nextSeq,
		Content:       *content,
	})
}

// Health is an unauthenticated liveness probe.
func (d *Deps) Health(c *gin.Context) {
	c.JSON(http.StatusOK, models.HealthResponse{
		Status:  "ok",
		Service: serviceName,
		Version: d.Version,
	})
}
