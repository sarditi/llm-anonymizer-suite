package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	commonmodels "ext-llm-common/models"
)

// Client is a thin wrapper around the gateway's REST API. It only calls the
// async endpoints (init_chat and set_req_from_adapter); the sync flow is
// re-implemented in this adapter.
type Client struct {
	baseURL    string
	httpClient *http.Client
	adapterID  string
	defaultLLM string
}

func NewClient(baseURL, adapterID, defaultLLM string, timeout time.Duration) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
		adapterID:  adapterID,
		defaultLLM: defaultLLM,
	}
}

// WithHTTPClient lets tests inject a custom *http.Client.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.httpClient = h
	return c
}

// InitChat calls POST /init_chat. The userID is forwarded so the gateway can
// record it in METADATA_<chat_id>; the adapter_id and llm come from config.
func (c *Client) InitChat(ctx context.Context, userID string, persistTimeSec int) (*commonmodels.InitChatResponse, error) {
	body := commonmodels.InitChatRequest{
		LLM:                    c.defaultLLM,
		UserID:                 userID,
		AdapterID:              c.adapterID,
		PersistanceTimeSeconds: persistTimeSec,
	}
	var resp commonmodels.InitChatResponse
	if err := c.do(ctx, http.MethodPost, "/init_chat", body, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return &resp, fmt.Errorf("gateway init_chat returned status=%q message=%q", resp.Status, resp.Message)
	}
	if resp.ChatID == "" || resp.SetNextReqKey == "" || resp.StreamPwd == "" {
		return &resp, errors.New("gateway init_chat returned incomplete payload")
	}
	return &resp, nil
}

// SetReqFromAdapter calls the gateway's async POST /set_req_from_adapter. The
// returned value is the gateway's immediate ack — the actual reply lands on
// STREAM_<chat_id> later, which the adapter reads via the streamreader.
func (c *Client) SetReqFromAdapter(ctx context.Context, req commonmodels.SetRequest) (*commonmodels.SetReqHttpResponse, error) {
	var resp commonmodels.SetReqHttpResponse
	if err := c.do(ctx, http.MethodPost, "/set_req_from_adapter", req, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return &resp, fmt.Errorf("gateway set_req returned status=%q message=%q", resp.Status, resp.Message)
	}
	return &resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call gateway %s%s: %w", c.baseURL, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read gateway response: %w", err)
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("gateway %s returned %d: %s", path, resp.StatusCode, truncate(string(raw), 256))
	}

	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode gateway response (%s, status=%d): %w", path, resp.StatusCode, err)
		}
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("gateway %s returned %d", path, resp.StatusCode)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
