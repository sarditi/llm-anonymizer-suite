package providers

import (
    "llm-gemini-mediator/internal/ai"

	"context"
	//"strings"
	"google.golang.org/genai"
	"github.com/redis/go-redis/v9"

)

type MockProvider struct{}

func (p *MockProvider) CreateSession(ctx context.Context, rdb *redis.Client, client *genai.Client, model  string,sessionId string, keys []string) (ai.Messenger, error) {
    return &ai.MockGemini{}, nil
}