package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonmodels "ext-llm-common/models"
	"ext-llm-webadapter/internal/auth"
	"ext-llm-webadapter/internal/config"
	gw "ext-llm-webadapter/internal/gateway"
	"ext-llm-webadapter/internal/models"
	"ext-llm-webadapter/internal/sessions"
)

// fakeReader stands in for the streamreader so the server tests don't depend
// on miniredis. The full Redis-backed flow has its own coverage in the
// streamreader package and the integration test under tests/integration.
type fakeReader struct {
	calls   int32
	content commonmodels.Content
	nextSeq string
	err     error
}

func (f *fakeReader) AwaitContent(_ context.Context, _, _, _ string) (*commonmodels.Content, string, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return nil, "", f.err
	}
	cp := f.content
	return &cp, f.nextSeq, nil
}

type fakeGateway struct {
	srv             *httptest.Server
	initStatus      int
	initBody        commonmodels.InitChatResponse
	setReqStatus    int
	setReqBody      commonmodels.SetReqHttpResponse
	lastSetReqInput commonmodels.SetRequest
}

func newFakeGateway() *fakeGateway {
	fg := &fakeGateway{
		initStatus: http.StatusOK,
		initBody: commonmodels.InitChatResponse{
			Status:        "success",
			ChatID:        "chat-1",
			SetNextReqKey: "00_first",
			Message:       "ok",
			StreamPwd:     "pwd-1",
		},
		setReqStatus: http.StatusOK,
		setReqBody:   commonmodels.SetReqHttpResponse{Status: "success", ChatID: "chat-1", Message: "ok"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/init_chat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fg.initStatus)
		_ = json.NewEncoder(w).Encode(fg.initBody)
	})
	mux.HandleFunc("/set_req_from_adapter", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&fg.lastSetReqInput)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fg.setReqStatus)
		_ = json.NewEncoder(w).Encode(fg.setReqBody)
	})
	fg.srv = httptest.NewServer(mux)
	return fg
}

func (fg *fakeGateway) Close() { fg.srv.Close() }

type harness struct {
	deps        *Deps
	router      http.Handler
	tokens      *auth.TokenManager
	gateway     *fakeGateway
	reader      *fakeReader
	plainPasswd string
	username    string
	store       *sessions.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	plain := "s3cret"
	hash, err := auth.HashPassword(plain)
	require.NoError(t, err)

	tokens := auth.NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "iss")
	users := auth.NewUserStore([]config.UserCredentials{{Username: "alice", PasswordBcrypt: hash}})
	store := sessions.New(time.Hour)
	fg := newFakeGateway()
	reader := &fakeReader{
		content: commonmodels.Content{InputText: "hello back"},
		nextSeq: "01_next",
	}
	deps := &Deps{
		Users:     users,
		Tokens:    tokens,
		Gateway:   gw.NewClient(fg.srv.URL, "ada1", "chatgpt", 2*time.Second),
		Sessions:  store,
		Reader:    reader,
		AdapterID: "ada1",
		Version:   "test",
		CORS: config.CORSConfig{
			AllowedOrigins:   []string{"https://app.example.com"},
			AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: false,
			MaxAgeSeconds:    600,
		},
	}
	t.Cleanup(func() { fg.Close() })
	return &harness{
		deps:        deps,
		router:      BuildRouter(deps),
		tokens:      tokens,
		gateway:     fg,
		reader:      reader,
		plainPasswd: plain,
		username:    "alice",
		store:       store,
	}
}

func (h *harness) do(method, path, token string, body any) *httptest.ResponseRecorder {
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
	h.router.ServeHTTP(rr, req)
	return rr
}

func (h *harness) login(t *testing.T) string {
	t.Helper()
	rr := h.do(http.MethodPost, "/api/v1/auth/login", "", models.LoginRequest{
		Username: h.username, Password: h.plainPasswd,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp models.LoginResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.NotEmpty(t, resp.Token)
	return resp.Token
}

func TestHealth_NoAuthRequired(t *testing.T) {
	h := newHarness(t)
	rr := h.do(http.MethodGet, "/api/v1/health", "", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var resp models.HealthResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "ok", resp.Status)
	assert.Equal(t, "ext-llm-webadapter", resp.Service)
}

func TestLogin_Success(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)
	claims, err := h.tokens.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Username())
}

func TestLogin_RejectsBadPassword(t *testing.T) {
	h := newHarness(t)
	rr := h.do(http.MethodPost, "/api/v1/auth/login", "", models.LoginRequest{
		Username: "alice", Password: "wrong",
	})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestLogin_RejectsUnknownUser(t *testing.T) {
	h := newHarness(t)
	rr := h.do(http.MethodPost, "/api/v1/auth/login", "", models.LoginRequest{
		Username: "ghost", Password: "anything",
	})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestLogin_RejectsMalformedBody(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestProtectedRoutes_RequireBearer(t *testing.T) {
	h := newHarness(t)
	rr := h.do(http.MethodPost, "/api/v1/chat/init", "", nil)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestProtectedRoutes_RejectMalformedAuth(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/init", nil)
	req.Header.Set("Authorization", "NotBearer something")
	rr := httptest.NewRecorder()
	h.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRefresh_RotatesToken(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)
	rr := h.do(http.MethodPost, "/api/v1/auth/refresh", tok, nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp models.LoginResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.NotEmpty(t, resp.Token)
}

func TestInitChat_StoresStreamPwdServerSide(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)

	rr := h.do(http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{LLM: "chatgpt"})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp commonmodels.InitChatResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "chat-1", resp.ChatID)
	assert.Equal(t, "00_first", resp.SetNextReqKey)

	// stream_pwd is server-side only.
	body := rr.Body.String()
	assert.NotContains(t, body, "pwd-1")
	assert.NotContains(t, body, "stream_pwd")
}

func TestInitChat_PropagatesGatewayFailure(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)
	h.gateway.initStatus = http.StatusBadRequest
	h.gateway.initBody = commonmodels.InitChatResponse{Status: "error", Message: "bad adapter"}

	rr := h.do(http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{})
	require.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestSendMessage_RoundTrip(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)

	// init chat first
	rr := h.do(http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{})
	require.Equal(t, http.StatusOK, rr.Code)

	// send a message
	rr = h.do(http.MethodPost, "/api/v1/chat/message", tok, models.SendMessageRequest{
		ChatID:     "chat-1",
		RequestKey: "00_first",
		Content:    commonmodels.Content{InputText: "hi there"},
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp models.SendMessageResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, "chat-1", resp.ChatID)
	assert.Equal(t, "01_next", resp.SetNextReqKey)
	assert.Equal(t, "hello back", resp.Content.InputText)

	// Gateway saw exactly the prompt the user sent.
	assert.Equal(t, "hi there", h.gateway.lastSetReqInput.Content.InputText)
	assert.Equal(t, "00_first", h.gateway.lastSetReqInput.ReqKey)
	assert.Equal(t, "chat-1", h.gateway.lastSetReqInput.InternalChatID)

	// Reader was invoked.
	assert.EqualValues(t, 1, atomic.LoadInt32(&h.reader.calls))
}

func TestSendMessage_UnknownChat(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)
	rr := h.do(http.MethodPost, "/api/v1/chat/message", tok, models.SendMessageRequest{
		ChatID: "unknown", RequestKey: "k", Content: commonmodels.Content{InputText: "x"},
	})
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSendMessage_DifferentUserCannotAccess(t *testing.T) {
	h := newHarness(t)

	// Pre-stash a session owned by "alice" so we can prove "bob" is rejected.
	h.store.Put("chat-1", "alice", "pwd-1")

	// Add bob and log him in.
	bobHash, _ := auth.HashPassword("bobpw")
	h.deps.Users = auth.NewUserStore([]config.UserCredentials{
		{Username: "alice", PasswordBcrypt: mustHash("s3cret")},
		{Username: "bob", PasswordBcrypt: bobHash},
	})
	h.router = BuildRouter(h.deps)

	rr := h.do(http.MethodPost, "/api/v1/auth/login", "", models.LoginRequest{Username: "bob", Password: "bobpw"})
	require.Equal(t, http.StatusOK, rr.Code)
	var lr models.LoginResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&lr))

	rr = h.do(http.MethodPost, "/api/v1/chat/message", lr.Token, models.SendMessageRequest{
		ChatID: "chat-1", RequestKey: "k", Content: commonmodels.Content{InputText: "x"},
	})
	require.Equal(t, http.StatusForbidden, rr.Code)
}

func TestSendMessage_PipelineErrorPropagates(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)
	rr := h.do(http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{})
	require.Equal(t, http.StatusOK, rr.Code)

	h.reader.err = errors.New("pipeline broke")
	rr = h.do(http.MethodPost, "/api/v1/chat/message", tok, models.SendMessageRequest{
		ChatID: "chat-1", RequestKey: "00_first", Content: commonmodels.Content{InputText: "x"},
	})
	require.Equal(t, http.StatusGatewayTimeout, rr.Code)
}

func TestSendMessage_GatewayReportsError(t *testing.T) {
	h := newHarness(t)
	tok := h.login(t)
	rr := h.do(http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{})
	require.Equal(t, http.StatusOK, rr.Code)

	h.gateway.setReqStatus = http.StatusBadRequest
	h.gateway.setReqBody = commonmodels.SetReqHttpResponse{Status: "error", Message: "Wrong sequance key"}

	rr = h.do(http.MethodPost, "/api/v1/chat/message", tok, models.SendMessageRequest{
		ChatID: "chat-1", RequestKey: "00_first", Content: commonmodels.Content{InputText: "x"},
	})
	require.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestExpiredToken_IsRejected(t *testing.T) {
	h := newHarness(t)
	short := auth.NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Millisecond, "iss")
	tok, _, err := short.Issue("alice")
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)

	// Replace the deps' token manager so the middleware uses the same TM.
	h.deps.Tokens = short
	h.router = BuildRouter(h.deps)

	rr := h.do(http.MethodPost, "/api/v1/chat/init", tok, commonmodels.InitChatRequest{})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func mustHash(s string) string {
	h, err := auth.HashPassword(s)
	if err != nil {
		panic(err)
	}
	return h
}

// --- CORS middleware ---------------------------------------------------------

func TestCORS_AllowedOriginIsEchoed(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/chat/init", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization,Content-Type")
	rr := httptest.NewRecorder()
	h.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	assert.Equal(t, "https://app.example.com",
		rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Vary"), "Origin")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t,
		rr.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Equal(t, "600", rr.Header().Get("Access-Control-Max-Age"))
}

func TestCORS_DisallowedOriginGetsNoAllowOriginHeader(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/chat/init", nil)
	req.Header.Set("Origin", "https://attacker.example.com")
	rr := httptest.NewRecorder()
	h.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_WildcardWithCredentialsReflectsOrigin(t *testing.T) {
	plain := "s3cret"
	hash, err := auth.HashPassword(plain)
	require.NoError(t, err)
	tokens := auth.NewTokenManager(
		[]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "iss")
	users := auth.NewUserStore(
		[]config.UserCredentials{{Username: "alice", PasswordBcrypt: hash}})
	store := sessions.New(time.Hour)
	fg := newFakeGateway()
	t.Cleanup(func() { fg.Close() })
	deps := &Deps{
		Users:     users,
		Tokens:    tokens,
		Gateway:   gw.NewClient(fg.srv.URL, "ada1", "chatgpt", 2*time.Second),
		Sessions:  store,
		Reader:    &fakeReader{},
		AdapterID: "ada1",
		Version:   "test",
		CORS: config.CORSConfig{
			AllowedOrigins:   []string{"*"},
			AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: true,
			MaxAgeSeconds:    600,
		},
	}
	router := BuildRouter(deps)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/chat/init", nil)
	req.Header.Set("Origin", "https://anywhere.example.com")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	// With credentials, the literal "*" is illegal; the middleware reflects
	// the request's Origin instead.
	assert.Equal(t, "https://anywhere.example.com",
		rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true",
		rr.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, rr.Header().Get("Vary"), "Origin")
}

// Compile-time guard that fakeReader satisfies the StreamReader interface used
// by handlers; if the interface drifts this test file will fail to compile.
var _ StreamReader = (*fakeReader)(nil)

// helper for any future test that wants to log at a level — currently unused
// but keeps go vet happy when tests want fmt.
var _ = fmt.Sprint
