package providers

import (
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"
    "llm-gemini-mediator/internal/ai"


	"context"
	"encoding/json"

	"google.golang.org/genai"
	"github.com/redis/go-redis/v9"
)
type GeminiProvider struct {}

func (p *GeminiProvider) CreateSession(ctx context.Context, rdb *redis.Client, client *genai.Client,model  string, sessionId string, keys []string) (ai.Messenger, error) {
    if len(keys) > 1 {
        return p.reconstructChat(ctx, rdb, keys[1:], client, model)
    }
    return client.Chats.Create(ctx, model, nil, nil)
}

func (p *GeminiProvider) reconstructChat(ctx context.Context, rdb *redis.Client, readAccessKeys []string, client *genai.Client,model string) (ai.Messenger, error) {
    logger.Log.Info("Reconstructing chat id : ")
    var sdkHistory []*genai.Content

    for i, accessKey := range readAccessKeys {
        raw, err := rdb.Get(ctx, accessKey).Result()
        if err != nil {
            return nil, err
        }
        
        // Use your pointer-based struct
        req := &models.Content{InputText: ""}
        if err := json.Unmarshal([]byte(raw), req); err != nil {
            return nil, err
        }

        role := "user"
        if i%2 != 0 {
            role = "model"
        }

        sdkHistory = append(sdkHistory, &genai.Content{
            Role: role,
            Parts: []*genai.Part{
                {Text: req.InputText},
            },
        })
    }
    return client.Chats.Create(ctx, model, nil, sdkHistory)
}