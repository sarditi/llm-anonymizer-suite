package ai

import (
	"ext-llm-gateway/logger"
	"ext-llm-gateway/models"
	"ext-llm-gateway/utils"

	"context"
	"time"
	"encoding/json"

	"google.golang.org/genai"
	"github.com/redis/go-redis/v9"
	"github.com/patrickmn/go-cache"

)

type GeminiService struct {
	client      *genai.Client
	sessions    *cache.Cache
	geminiModel string
	//mockMode    bool
	provider    SessionProvider
}

const cacheGCInMinutes = 5
const keyTTLInMinutes  = 10

// Define this at the top of your file
type Messenger interface {
    SendMessage(ctx context.Context, parts ...genai.Part) (*genai.GenerateContentResponse, error)
}

type SessionProvider interface {
    CreateSession(ctx context.Context, rdb *redis.Client, client *genai.Client, model  string,sessionId string, readAccessKeys []string) (Messenger, error)
}

func NewGeminiService(ctx context.Context, apiKey string, geminiModel string,provider SessionProvider/*, mockMode bool*/) (*GeminiService, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		return nil, err
	}

	return &GeminiService{
		client:   client,
		sessions: cache.New(keyTTLInMinutes*time.Minute, cacheGCInMinutes*time.Minute),
		geminiModel: geminiModel,
		provider: provider,
		//mockMode: mockMode,
	}, nil
}

func (s *GeminiService) ProcessRedisContent(ctx context.Context, perms models.ReqRedisAccessPermissions, sessionId string) (bool, string) {

	opt, err := utils.RedisOptionsFromURL(perms.RedisURL, perms.UserID, perms.Pwd)
	if err != nil {
		return false, err.Error()
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()


	if len(perms.ReadAccessKeys) == 0 || len(perms.WriteAccessKeys) == 0 {
		return false, "no redis keys provided"
	}

	var sess Messenger
	var sessChat any
	var exists bool
	
	sessChat, exists = s.sessions.Get(sessionId)
	
	//logger.Log.Debug("ReadAccessKeys for sessionId - " + sessionId + " are " + strings.Join(perms.ReadAccessKeys, ", "))
	// In case session cache does not exist
	if !exists {
		var err error

		// readAccessKeys := perms.ReadAccessKeys
		// if s.mockMode {
        //     // Role 1: Return the Mock
        //     logger.Log.Debug("Mock Mode active: Creating fake session for " + sessionId)
        //     sess = &MockGemini{}
        // } else {
		// 	if len(readAccessKeys) > 1 {
		// 		logger.Log.Debug("Recreating session cache for sessionId - " + sessionId)
		// 		sess, err = s.reconstructChat(ctx, rdb, readAccessKeys[1:])
		// 	} else {
		// 		logger.Log.Debug("Creating a new session cache for sessionId - " + sessionId)
		// 		sess, err = s.client.Chats.Create(ctx, s.geminiModel, nil, nil)
		// 	}
		// 	if err != nil {
		// 		return false, err.Error()
		// 	}
		// }
		sess, err = s.provider.CreateSession(ctx, rdb, s.client, s.geminiModel, sessionId, perms.ReadAccessKeys)
        if err != nil {
            return false, err.Error()
        }
        s.sessions.Set(sessionId, sess, keyTTLInMinutes*time.Minute)
	} else {
		var ok bool
        sess, ok = sessChat.(Messenger)
        if !ok {
            return false, "session in cache does not support SendMessage"
        }
	}

	s.sessions.Set(sessionId, sess, keyTTLInMinutes*time.Minute)

	// Get the latest request from llm
	readKey := perms.ReadAccessKeys[0]
    logger.Log.Debug("Latest ReadAccessKey is : " + readKey)
	raw, err := rdb.Get(ctx, readKey).Result()
	if err != nil {
		return false, err.Error()	
	}

	var req models.Content
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return false, "Unmarshel raw failed " + err.Error()
	}

	logger.Log.Debug("sendToGemini for sessionId - " + sessionId + " readkey " + readKey)

	resp, err := s.sendToGemini(ctx, &req, sess)

	if err != nil {
		return false, err.Error()
	}

	// Initialize with the Status field to fix the "unknown field" error
	result := &models.Content{
		InputText: "",
	}

	// Iterate through candidates and parts to separate thoughts (explanations)
	for _, cand := range resp.Candidates {
		for _, p := range cand.Content.Parts {
			if p.Thought {
				result.Explanations += p.Text
			} else {
				result.InputText += p.Text
			}
		}
	}

	payload, _ := json.Marshal(result)
	writeKey := perms.WriteAccessKeys[len(perms.WriteAccessKeys)-1]

	logger.Log.Debug("Update redis for sessionId - " + sessionId + " payload " + string(payload))

	if err := rdb.Set(ctx, writeKey, payload, 0).Err(); err != nil {
		return false, "Set failed " + err.Error()
	}

	return true, ""
}

func (s *GeminiService) sendToGemini(ctx context.Context, req *models.Content, chatSession Messenger/**genai.Chat*/) (*genai.GenerateContentResponse, error) {

	type messenger interface {
    // Note the pointer on *genai.Part
    	SendMessage(context.Context, ...genai.Part) (*genai.GenerateContentResponse, error)
	}
	//session := chatSession.(messenger)
	//resp, err := session.SendMessage(ctx, &genai.Part{Text: req.InputText})
	resp, err := chatSession.SendMessage(ctx, genai.Part{Text: req.InputText})

	if err != nil {
		return nil, err
	}
	return resp, nil
}

func redisOptionsFromURL(url, username string, password string) (*redis.Options, error) {
	opt, err := redis.ParseURL("redis://" + url)
	if err != nil {
		return nil, err
	}
	opt.Username = username
	opt.Password = password
	return opt, nil
}

// func (s *GeminiService) reconstructChat(ctx context.Context, rdb *redis.Client, readAccessKeys []string) (Messenger, error) {
//     var sdkHistory []*genai.Content

//     for i, accessKey := range readAccessKeys {
//         raw, err := rdb.Get(ctx, accessKey).Result()
//         if err != nil {
//             return nil, err
//         }
        
//         // Use your pointer-based struct
//         req := &models.Content{InputText: ""}
//         if err := json.Unmarshal([]byte(raw), req); err != nil {
//             return nil, err
//         }

//         role := "user"
//         if i%2 != 0 {
//             role = "model"
//         }

//         sdkHistory = append(sdkHistory, &genai.Content{
//             Role: role,
//             Parts: []*genai.Part{
//                 {Text: req.InputText},
//             },
//         })
//     }

    // Corrected Create call for v1.42.0:
    // Since you don't have other configs (like temperature), pass nil for config.
	// For more config options refer to: https://pkg.go.dev/google.golang.org/genai#GenerateContentConfig
//    return s.client.Chats.Create(ctx, s.geminiModel, nil, sdkHistory)
//}