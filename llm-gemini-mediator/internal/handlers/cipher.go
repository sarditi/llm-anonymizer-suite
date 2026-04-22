package handlers


import (

	"ext-llm-gateway/models"
	//"llm-gemini-mediator/internal/redisacl"
	"llm-gemini-mediator/internal/ai"
	"ext-llm-gateway/logger"

	"net/http"
	"context"
	"time"

	"github.com/gin-gonic/gin"

)

type GeminiRestAPIHandler struct {
	GeminiService *ai.GeminiService
}

func NewGeminiRestAPIHandler(geminiService *ai.GeminiService) *GeminiRestAPIHandler {
	return &GeminiRestAPIHandler{
		GeminiService: geminiService,
	}
}

func (gs *GeminiRestAPIHandler) GeminiRedisAcl(c *gin.Context)  {
	var req models.ReqRedisAccessPermissions
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Log.Error(err.Error())
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sessionId := c.GetHeader("X-User-ID")
    
    if sessionId == "" {
        // Handle missing ID (e.g., return 400 Bad Request)
        c.JSON(400, gin.H{"error": "X-User-ID header is required"})
        return
    }

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
    defer cancel() 

	ok, msg := gs.GeminiService.ProcessRedisContent(ctx, req, sessionId)
	if !ok {
		logger.Log.Error(msg)
		c.JSON(http.StatusInternalServerError, gin.H{"error": msg})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Cipher completed successfully"})
}


