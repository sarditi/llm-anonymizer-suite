package main


import (
	"ext-llm-gateway/logger"

	"os"
	"context"

	"github.com/gin-gonic/gin"
	"llm-gemini-mediator/internal/handlers"
	"llm-gemini-mediator/internal/ai"
	"llm-gemini-mediator/registry"

    //"net/http"
)


func main() {
	r := gin.Default()
	apiKey := os.Getenv("GEMINI_API_KEY")
	geminiModel := os.Getenv("GEMINI_MODEL")
	providerName := os.Getenv("GEMINI_PROVIDER")
	provider := registry.CreateInstance(providerName).(ai.SessionProvider)
	svc, err := ai.NewGeminiService(context.Background(), apiKey, geminiModel, provider)
	if(err != nil){
		panic(err)
	}
	hndlr := handlers.NewGeminiRestAPIHandler(svc)

	r.POST("/gemini_redis_acl", hndlr.GeminiRedisAcl)
	//r.POST("/chat", hndlr.Chat)
	logger.Log.Info("llm-gemini-mediator listening on :9111")

	_ = r.Run(":9111")
}
