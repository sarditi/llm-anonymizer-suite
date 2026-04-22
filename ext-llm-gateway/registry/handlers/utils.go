package handlers

import (
	"ext-llm-gateway/models"
	"ext-llm-gateway/utils"
	"ext-llm-gateway/logger"
	"ext-llm-gateway/orchestrator"

	"encoding/json"
	"fmt"
	"strings"
	"time"
	"context"

	"google.golang.org/grpc/codes"
	"github.com/redis/go-redis/v9"
)

func GetContentFromStream(internalChatID string, syn *orchestrator.SynchRedisDatasource)  (int, string, string, *models.Content) {

	logger.Log.Debug("Reading from stream STREAM_" + internalChatID + "...")

	streams, err := syn.Rdb.XRead(context.Background(), &redis.XReadArgs{
 		Streams: []string{"STREAM_" + internalChatID, "$"}, 
 		Block:   time.Duration(models.TimeoutSyncMessageSeconds) * time.Second,
 		Count:   1,
 	}).Result()

	if err == redis.Nil {
		return int(codes.InvalidArgument), internalChatID, fmt.Sprintf("Timeout: no new message in stream %s after %v", "STREAM_" + internalChatID, models.TimeoutSyncMessageSeconds),nil
	} else if err != nil {
		return int(codes.InvalidArgument), internalChatID, fmt.Sprintf("Timeout redis error: %v", err),nil
	}

	if len(streams) > 0 && len(streams[0].Messages) > 0 {
		latestMessage := streams[0].Messages[0]
		
		respString := latestMessage.Values["response"].(string)
		nextSeq    := latestMessage.Values["next_seq"].(string)
		logger.Log.Debug("Reading message from stream STREAM_" + internalChatID + " response=" + respString + " next_seq=" + nextSeq)

		var respStream models.GetResponseFromStream
    
		// Inline execution: Unmarshal and handle error immediately
		if err := json.Unmarshal([]byte(respString), &respStream); err != nil {
			return int(codes.InvalidArgument), internalChatID, fmt.Sprintf("failed to map redis permissions: %v", err),nil
		}

		if respStream.ReqRedisAccessPermissions == nil {
			return int(codes.InvalidArgument), internalChatID, respStream.ErrorString,nil
		}

		opt, err := utils.RedisOptionsFromURL(respStream.ReqRedisAccessPermissions.RedisURL, respStream.ReqRedisAccessPermissions.UserID, respStream.ReqRedisAccessPermissions.Pwd)
		if err != nil {
			return int(codes.InvalidArgument), internalChatID, fmt.Sprintf("failed to retrieve redis opts: %v", err),nil
		}
		rdb := redis.NewClient(opt)
		defer rdb.Close()

		readKey := respStream.ReqRedisAccessPermissions.ReadAccessKeys[0]
    	logger.Log.Debug("Latest ReadAccessKey is : " + readKey)
		raw, err := rdb.Get(*syn.Ctx, readKey).Result()
		if err != nil {
			return int(codes.InvalidArgument), internalChatID, fmt.Sprintf("Get from Redis DB failed %v", err),nil
		}

		var content models.Content
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			return int(codes.InvalidArgument), internalChatID, fmt.Sprintf("Unmarshel raw failed  %v", err),nil
		}
		return int(codes.OK), internalChatID, nextSeq, &content

	}
	return int(codes.InvalidArgument), internalChatID, "received empty stream result",nil
}

func SetReqToStream(data []byte, valRedisMaxSizeByte int, prompt *string,synWorkHandler  *orchestrator.Mux) (int, *models.SetRequest, string, string){

	var req models.SetRequest

	if err := json.Unmarshal([]byte(data), &req); err != nil {
		logger.Log.Error("Failed to validate JSON for request")
		return int(codes.InvalidArgument), nil, "", err.Error()
	}

	if len(req.Content.InputText) > valRedisMaxSizeByte {
		errMsg := fmt.Sprintf("%s message content length exceeded %dMB", req.InternalChatID, valRedisMaxSizeByte)
		logger.Log.Error(errMsg)
		return int(codes.ResourceExhausted), nil, req.InternalChatID, errMsg
	}

	if strings.HasPrefix(req.ReqKey, "00") && prompt != nil {
		logger.Log.Debug("Adding Prompt " + *prompt + " to the first request for " + req.InternalChatID)
		req.Content.InputText = *prompt + " " + req.Content.InputText
	}

	code, internalChatID, retMsg := synWorkHandler.SetReqFromAdapterInternal(&req)
	return code, &req, internalChatID, retMsg

}
