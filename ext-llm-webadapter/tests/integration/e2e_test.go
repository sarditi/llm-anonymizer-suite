package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonmodels "ext-llm-common/models"
	"ext-llm-webadapter/internal/auth"
	"ext-llm-webadapter/internal/config"
	gw "ext-llm-webadapter/internal/gateway"
	"ext-llm-webadapter/internal/models"
	"ext-llm-webadapter/internal/server"
	"ext-llm-webadapter/internal/sessions"
	"ext-llm-webadapter/internal/streamreader"
)

// TestE2E_FullChatTurn exercises the full adapter flow against a fake gateway
// and miniredis: login, init_chat, send a message, and have the gateway "reply"
// onto STREAM_<chat_id> so the adapter can fetch the JIT-credentialed content
// and return it to the caller.
func TestE2E_FullChatTurn(t *testing.T) {
	// miniredis' multi-user ACL surface is incomplete (it accepts AUTH but
	// can't model per-user key-pattern allow-lists), so this integration
	// test runs against an auth-less miniredis and uses an adapter
	// configured with empty Redis credentials. The streamreader package
	// has its own unit tests that exercise the credential-bearing path.
	mr := miniredis.RunT(t)

	// --- fake gateway ---
	const chatID = "11111111-2222-3333-4444-555555555555"
	const firstKey = "00_init-key"
	const nextKey = "01_after-key"
	contentKey := "seq_00_workergroup_anonimyzer_wrkind_03_reqid_" + chatID

	// Plant the deciphered final content the JIT user would GET.
	finalContent, err := json.Marshal(commonmodels.Content{InputText: "John Carter says hi"})
	require.NoError(t, err)
	mr.Set(contentKey, string(finalContent))

	streamPayload, err := json.Marshal(commonmodels.GetResponseFromStream{
		ReqRedisAccessPermissions: &commonmodels.ReqRedisAccessPermissions{
			UserID:         "default",
			Pwd:            "",
			RedisURL:       mr.Addr(),
			ReadAccessKeys: []string{contentKey},
		},
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/init_chat", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(commonmodels.InitChatResponse{
			Status: "success", ChatID: chatID, SetNextReqKey: firstKey,
			Message: "ok", StreamPwd: "ignored-by-noauth-miniredis",
		})
	})
	mux.HandleFunc("/set_req_from_adapter", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(commonmodels.SetReqHttpResponse{
			Status: "success", ChatID: chatID, Message: "ok",
		})
		// Asynchronously publish the pipeline reply, mimicking the gateway's
		// _FINALIZER_ stage writing to STREAM_<chat_id>.
		go func() {
			time.Sleep(50 * time.Millisecond)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer rdb.Close()
			rdb.XAdd(context.Background(), &redis.XAddArgs{
				Stream: "STREAM_" + chatID,
				Values: map[string]interface{}{
					"response": string(streamPayload),
					"next_seq": nextKey,
				},
			})
		}()
	})
	gwServer := httptest.NewServer(mux)
	defer gwServer.Close()

	// --- adapter wiring ---
	hash, err := auth.HashPassword("s3cret")
	require.NoError(t, err)

	tokens := auth.NewTokenManager([]byte("integration-secret-32-characters!!!"), time.Hour, "iss")
	users := auth.NewUserStore([]config.UserCredentials{{Username: "alice", PasswordBcrypt: hash}})
	store := sessions.New(time.Hour)
	gwClient := gw.NewClient(gwServer.URL, "ada1", "chatgpt", 5*time.Second)
	reader := streamreader.New(mr.Addr(), 2*time.Second, 5*time.Second)

	deps := &server.Deps{
		Users:    users,
		Tokens:   tokens,
		Gateway:  gwClient,
		Sessions: store,
		Reader:   reader,
		// "default" with empty pwd is what miniredis accepts when no
		// RequireUserAuth has been registered; in production this would
		// be the configured adapter_id.
		AdapterID: "default",
		Version:   "integration",
	}
	router := server.BuildRouter(deps)

	// --- 1. login ---
	tok := login(t, router, "alice", "s3cret")

	// --- 2. init chat ---
	rr := do(t, router, http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var initResp commonmodels.InitChatResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&initResp))
	require.Equal(t, chatID, initResp.ChatID)
	require.Equal(t, firstKey, initResp.SetNextReqKey)

	// --- 3. send message; adapter should block on STREAM_<chat_id> and return the content ---
	rr = do(t, router, http.MethodPost, "/api/v1/chat/message", tok, models.SendMessageRequest{
		ChatID:     chatID,
		RequestKey: firstKey,
		Content:    commonmodels.Content{InputText: "Hi, working with John Carter"},
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var msgResp models.SendMessageResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&msgResp))

	assert.Equal(t, "success", msgResp.Status)
	assert.Equal(t, chatID, msgResp.ChatID)
	assert.Equal(t, nextKey, msgResp.SetNextReqKey)
	assert.Equal(t, "John Carter says hi", msgResp.Content.InputText)
}

func login(t *testing.T, router http.Handler, user, pw string) string {
	t.Helper()
	rr := do(t, router, http.MethodPost, "/api/v1/auth/login", "", models.LoginRequest{Username: user, Password: pw})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var lr models.LoginResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&lr))
	require.NotEmpty(t, lr.Token)
	return lr.Token
}

func do(t *testing.T, router http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		b = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, b)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}
