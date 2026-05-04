package models

import commonmodels "ext-llm-common/models"

// SendMessageRequest is the body for POST /api/v1/chat/message.
type SendMessageRequest struct {
	ChatID     string                `json:"internal_chat_id" binding:"required"`
	RequestKey string                `json:"request_key" binding:"required"`
	Content    commonmodels.Content  `json:"content" binding:"required"`
}


// SendMessageResponse is the synchronous reply assembled by this adapter.
type SendMessageResponse struct {
	Status        string                `json:"status"`
	ChatID        string                `json:"internal_chat_id"`
	SetNextReqKey string                `json:"set_next_req_key,omitempty"`
	Content       commonmodels.Content  `json:"content,omitempty"`
	Message       string                `json:"message,omitempty"`
}

// ErrorResponse is the standard error body.
type ErrorResponse struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// HealthResponse is the body returned by GET /api/v1/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

// LoginRequest is the body sent to POST /api/v1/auth/login.
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginResponse is returned on successful authentication.
type LoginResponse struct {
	Token     string `json:"token"`
	TokenType string `json:"token_type"`
	ExpiresAt int64  `json:"expires_at"`
	Username  string `json:"username"`
}
