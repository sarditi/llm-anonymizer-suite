package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	models "ext-llm-common/models"
)

func TestClient_InitChat_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/init_chat", r.URL.Path)
		var body models.InitChatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "chatgpt", body.LLM)
		assert.Equal(t, "alice@example.com", body.UserID)
		assert.Equal(t, "ada1", body.AdapterID)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(models.InitChatResponse{
			Status:        "success",
			ChatID:        "chat-1",
			SetNextReqKey: "00_xyz",
			Message:       "ok",
			StreamPwd:     "supersecret",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "ada1", "chatgpt", time.Second)
	got, err := c.InitChat(context.Background(), "alice@example.com", 0)
	require.NoError(t, err)
	assert.Equal(t, "chat-1", got.ChatID)
	assert.Equal(t, "supersecret", got.StreamPwd)
}

func TestClient_InitChat_FailsOnIncompletePayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(models.InitChatResponse{
			Status: "success", ChatID: "c", SetNextReqKey: "k", // StreamPwd missing
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "ada1", "chatgpt", time.Second)
	_, err := c.InitChat(context.Background(), "u", 0)
	require.Error(t, err)
}

func TestClient_InitChat_PropagatesGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.InitChatResponse{Status: "error", Message: "Adapter ID nope is not configured"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "nope", "chatgpt", time.Second)
	_, err := c.InitChat(context.Background(), "u", 0)
	require.Error(t, err)
}

func TestClient_SetReqFromAdapter_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/set_req_from_adapter", r.URL.Path)
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(models.SetReqHttpResponse{Status: "success", ChatID: "c1", Message: "ok"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "ada1", "chatgpt", time.Second)
	got, err := c.SetReqFromAdapter(context.Background(), models.SetRequest{
		ReqKey: "k", InternalChatID: "c1", Content: models.Content{InputText: "hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "success", got.Status)
}

func TestClient_HandlesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "ada1", "chatgpt", time.Second)
	_, err := c.SetReqFromAdapter(context.Background(), models.SetRequest{InternalChatID: "x"})
	require.Error(t, err)
}
