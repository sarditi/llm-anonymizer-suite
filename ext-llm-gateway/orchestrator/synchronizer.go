package orchestrator

import (
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"
	"ext-llm-gateway/utils"

	"context"
	"time"
	"strconv"
	"strings"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/google/uuid"
)

/*
	Redis DB and stream orchestration for the gateway
*/

type SynchRedisDatasource struct {
	Rdb *redis.Client
	Ctx *context.Context
}

const streamInternalChat = "STREAM_"

//Initialize redis
func (ds *SynchRedisDatasource) InitDataSource(cfg models.DataSourceConfig){
    
    logger.Log.Info("Starting SyncRedisDatasource..")

	options := utils.InitRedisDataSource(cfg)
    
	ctx, _ := context.WithCancel(context.Background())

	ds.Rdb = redis.NewClient(options)
	ds.Ctx = &ctx 
		// Test connection
	_, err := ds.Rdb.Ping(*ds.Ctx).Result()
	if err != nil {
		panic("Failed to connect to Redis " + cfg.Addr + " with username " + cfg.Username + " " + err.Error())
	}
	logger.Log.Info("Connected to Redis " + cfg.Addr + " with username " + cfg.Username)
}

//Sets the response in the related stream (i.e. STREAM_<reqid>). The method is a part of SyncDSForDispatcher interface, workers/dispatchers have an instance of it so
// they can respod to the related stream of reqid in case of an error in the process or successfully finishing it.
func (ds *SynchRedisDatasource) SetFinalResponseInAdapterStream(reqid string, getResponseFromStream *models.GetResponseFromStream) (bool, string){

	adapterReqStream := streamInternalChat + reqid
	nextSeq 		 := ds.GetSequanceKey(reqid)
	if nextSeq == "" { 
		return false, "Failed SetFinalResponseInAdapterStream - unable to retrieve sequance key " + reqid
	}
	thresholdMS := time.Now().UnixMilli() - models.PersistanceTimeSeconds * 1000
	minID := fmt.Sprintf("%d-0", thresholdMS)
	response, _ := json.Marshal(*getResponseFromStream)
	if _, err := ds.Rdb.XAdd(*ds.Ctx, &redis.XAddArgs{
		Stream: adapterReqStream,
		MinID:  minID,
		Values: map[string]interface{}{
				"response": string(response),
				"next_seq": nextSeq,
		},
		}).Result(); err != nil {
			return false, "Error unable to acreate message in adapter stream " + adapterReqStream + err.Error()
	}
	return true,""
}

//Return the sequanece key from SEQ_<reqid>
func (ds *SynchRedisDatasource) GetSequanceKey(key string) (string) {
	val, err := ds.Rdb.Get(*ds.Ctx, models.ReqProcessingSeq + key).Result()
	if err != nil {
		logger.Log.Error("No value for key " + key)
		return ""
	}
	return val
}
// Returns request metadata (i.e. METADATA_<reqid>)
func (ds *SynchRedisDatasource) GetRequestMetadata(key string) (string) {
	val, err := ds.Rdb.Get(*ds.Ctx, models.MetadataInternalChat + key).Result()
	if err != nil {
		logger.Log.Error("No value for key " + key)
		return ""
	}
	return val
}

func (ds *SynchRedisDatasource) GetRequestPersistTimeSec(key string) (int) {
	metadateChat := ds.GetRequestMetadata(key)
	if metadateChat == "" {
		return -1
	}
	// unmarshel the metadata of chat
	var metadataChatJ models.InitChatRequest
	if err :=json.Unmarshal([]byte(metadateChat), &metadataChatJ); err != nil {
		logger.Log.Error("Failed Umarsheling metadata for req id " + key)
		return -1
	}
	return metadataChatJ.PersistanceTimeSeconds
}
//Set the sequanece key from SEQ_<reqid>
func (ds *SynchRedisDatasource) SetSequanceKey(key string, val string, persistTimeSec int) (bool, string){

	nextSeqKey := models.ReqProcessingSeq + key

	if err := ds.Rdb.Set(*ds.Ctx, nextSeqKey, val, 0).Err(); err != nil {
		logger.Log.Fatal("Error saving to Redis: " + err.Error())
		return false, "Error saving to Redis: " + err.Error()
	}
	return true, ""
}
//initilizing request by creating the request's metada object METADATA_<reqid>
func (ds *SynchRedisDatasource) InitRequest(key string, val any, persistTimeSec int) (bool, string) {

	if err := ds.Rdb.Set(*ds.Ctx, models.MetadataInternalChat + key, val, 0).Err(); err != nil {
		logger.Log.Fatal("Error saving to Redis: " + err.Error())
		return false, "Error saving to Redis: " + err.Error()
	}
	return true, ""
}
//Create the STREAM_<reqid>, the stream into which the adapter will receive the response upon completion of processing a request or in case of an error along the way
func (ds *SynchRedisDatasource) AdapterStreamCreation(adapter string, key string, persistTimeSec int) (bool, string, string) {
	// Create stream with TTL (use XADD with MAXLEN=0 to create empty stream)
	var msgID string
	var err error
	adapterReqStream := streamInternalChat + key
	thresholdMS := time.Now().UnixMilli() - models.PersistanceTimeSeconds * 1000
	minID := fmt.Sprintf("%d-0", thresholdMS)

	if msgID, err = ds.Rdb.XAdd(*ds.Ctx, &redis.XAddArgs{
		Stream: adapterReqStream,
		MinID:       minID,
		Values: map[string]interface{}{"init": "1"}, // placeholder
	}).Result(); err != nil {
		logger.Log.Fatal("Error unable to create stream " + adapterReqStream + " for user " + adapter + " in REDIS " + err.Error())
		return false, "Error unable to create stream " + adapterReqStream + " for user " + adapter + " in REDIS " + err.Error(), ""
	}
	// Delete placeholder so stream is logically empty
	if err := ds.Rdb.XDel(*ds.Ctx, adapterReqStream, msgID); err.Err() != nil {
		logger.Log.Fatal("Error unable to create stream " + adapterReqStream + " for user " + adapter + " in REDIS (while deleting init value)" + err.Err().Error())
		return false, "Error unable to create stream " + adapterReqStream + " for user " + adapter + " in REDIS (while deleting init value)" + err.Err().Error(), ""
	}
	// Define read-only user for the adapter
	pwd := uuid.New().String()
	if err := ds.Rdb.Do(*ds.Ctx, "ACL", "SETUSER", adapter, "on", ">" + pwd, "~"+adapterReqStream, "+XREAD", "+xdel", "-xadd").Err(); err != nil {
		logger.Log.Fatal("Error setting readonly permissions for Q " + adapterReqStream + " to user " + adapter + " in REDIS " + err.Error())
		return false, "Error setting readonly permissions for Q " + adapterReqStream + " to user " + adapter + " in REDIS " + err.Error(), ""
	}
	return true, "",pwd
}
//As every worker has a stream it consumes its reqid, this method creates a stream for the worker
func (ds *SynchRedisDatasource) CreateWorkerStream(key string) (bool, string) {
	// Create stream with TTL (use XADD with MAXLEN=0 to create empty stream)
	if cmd := ds.Rdb.XGroupCreateMkStream(*ds.Ctx, key, key, "$"); cmd.Err() != nil {
		logger.Log.Fatal("Error unable to create stream " + key + " in REDIS " + cmd.Err().Error())
		return false, "Error unable to create stream " + key + " in REDIS " + cmd.Err().Error()
	}
	return true, ""
}

const luaSetWorker0Atomic = `
        -- Set key with value
        redis.call("SET", KEYS[1], ARGV[1], "EX", ARGV[3])
        -- Add message to the stream
        return redis.call("XADD", KEYS[2], "MINID", "~", ARGV[4], "*","message", ARGV[2])
    `
//Sets the to the first worker a new request
func (ds *SynchRedisDatasource) SetWorker0Atomic(reqid string, newValue string, worker0Stream string, reqIdAndCurrSeq string,
	persistTimeSec int) (bool, string) {
	
    key := models.ReqProcessingSeq + reqid
    value := newValue
    stream := worker0Stream
    message := reqIdAndCurrSeq

	// Convert persistTimeSec to string for Lua
	ttlStr := strconv.Itoa(persistTimeSec)
	thresholdMS := time.Now().UnixMilli() - models.PersistanceTimeSeconds * 1000
	minID := fmt.Sprintf("%d-0", thresholdMS)

    if _, err := ds.Rdb.Eval(*ds.Ctx, luaSetWorker0Atomic, []string{key, stream}, value, message,ttlStr, minID).Result(); err != nil {
		return false, err.Error()
	}

	return true, "Success"
}
//returns the values of the message fetched from the stream
func (ds *SynchRedisDatasource) processMsgFromStream(stream string, consumer string, Messages []redis.XMessage, err error ) (bool, string, string){
	
	if err != nil && err != redis.Nil {
		logger.Log.Fatal("Unable to fetch message from stream " + stream + " for consumer  " + consumer + " Error - " + err.Error())
		return false, "NA","Unable to fetch message from stream " + stream + " for consumer  " + consumer + " Error - " + err.Error()
	}
	if len(Messages) > 0 {
		msg   := Messages[0]
		value := msg.Values["message"] // interface{}
		//messageStr := value.(string) 
		return true, msg.ID, value.(string) 
	}
	return true,"NA","No message to read"
}
//Fetch latest mesaage from stream
func (ds *SynchRedisDatasource) FetchMessageFromStream(stream string, consumer string) (bool, string, string) {

	streams, err := ds.Rdb.XReadGroup(*ds.Ctx, &redis.XReadGroupArgs{
		Group:    stream,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Block:    2 * time.Second,
		Count:    1,
	}).Result()

	if len(streams) == 0 {
		return true,"NA","No message to read"
	}
	return ds.processMsgFromStream(stream, consumer,streams[0].Messages, err)
}

var ackAndPublishLua = redis.NewScript(`
local acked = redis.call("XACK", KEYS[1], KEYS[2], ARGV[1])

if acked ~= 1 then
    -- XACK failed
    return 0
end

redis.call("XDEL", KEYS[1], ARGV[1])

-- If no next stream is specified, return success string
if KEYS[3] == "NA" then
    return "ack_is_successful_in_finalizer"
end

-- Try to add to next stream 
local ok, id = pcall(redis.call, "XADD", KEYS[3],"NOMKSTREAM","MINID", "~", ARGV[3], "*", "message", ARGV[2])

if not ok or not id then
    -- XADD failed
    return 2
end

-- Success: return the new entry ID
return id
`)

// Upon a worker/Dispatcher completing its work successfuly, the message is acknowledge and deleted from the worker streamand a new message 
// populates the next worker's stream
func (ds *SynchRedisDatasource) AckAndPublishToNextStreamAtomic(curreStream string, nextStream string, msgID string, reqID string) (bool) {
	//Ensure a message will be deleted after models.PersistanceTimeSeconds miiliseconds
	thresholdMS := time.Now().UnixMilli() - models.PersistanceTimeSeconds * 1000
	minID := fmt.Sprintf("%d-0", thresholdMS)

	res, err := ackAndPublishLua.Run(*ds.Ctx, ds.Rdb,
		[]string{curreStream, curreStream, nextStream},
		msgID, reqID, minID,
	).Result()
	
	if err != nil {
		// Lua runtime or Redis protocol error
		logger.Log.Error("Lua script execution error: " + err.Error())
		return false
	}

	switch v := res.(type) {
	case string:
		// XADD succeeded, message ID returned
		return true
	case int64:
		if v == 0 {
			// XACK failed
			return false
		}
		if v == 2 {
			// XADD failed
			logger.Log.Error("XADD failed while publishing message")
			return false
		}
	case nil:
		// unexpected nil
		logger.Log.Error("Lua returned nil unexpectedly")
		return false
	default:
		logger.Log.Error("Unexpected Lua return type")
		return false
	}
	
	return true
}
//Returns messages that were not acknoweldged yet, this happen if a dispatcher has died or another instance of the gateway had died while processing a message
func (ds *SynchRedisDatasource) ClaimNAckFromStream(stream string, consumer string, idleThreshold time.Duration) (bool, string, string) {
	Messages, _, err := ds.Rdb.XAutoClaim(*ds.Ctx, &redis.XAutoClaimArgs{
		Stream:   stream,
		Group:    stream,
		Consumer: consumer,
		MinIdle:  idleThreshold ,
		Start:    "0",
		Count:    1,
	}).Result()
	return ds.processMsgFromStream(stream, consumer,Messages, err)
}
//Deletes unack messages that have passed the threshold msgAgeThresholdSeconds
func (ds *SynchRedisDatasource) DelOldNAckFromStream(stream string, consumer string, msgAgeThresholdSeconds int) (bool, string) {
 	cutoff := time.Now().Add(-1 * time.Duration(msgAgeThresholdSeconds) * time.Second).UnixMilli()
	pending, err := ds.Rdb.XPendingExt(*ds.Ctx, &redis.XPendingExtArgs{
		Stream: stream,
		Group:  stream,
		Start:  "-",
		End:    "+",
		Count:  1000, // max messages per call
	}).Result()
	if err != nil {
		return false, err.Error()
	}
	var oldUnackMessages []string

	for _, msg := range pending {
		// Extract creation time from msg.ID
		msPart := msg.ID[:strings.Index(msg.ID, "-")]
		ms, _ := strconv.ParseInt(msPart, 10, 64)

		if ms <= cutoff {
			oldUnackMessages = append(oldUnackMessages, msg.ID)
		}
	}

	// Optional: process or delete them
	for _, msgID := range oldUnackMessages {
		// Fetch the message itself
		entries, err := ds.Rdb.XRange(*ds.Ctx, stream, msgID, msgID).Result()
		if err != nil {
			logger.Log.Error(fmt.Sprintf("Failed to read message for deletion from stream %s, msgID %s - %v ", stream, msgID, err))
			continue
		}

		if len(entries) > 0 {
			// Convert message values to string 
			msgStr := fmt.Sprintf("%v", entries[0].Values["message"])
			logger.Log.Debug(fmt.Sprintf("Deleting unack message ID=%s, payload=%s", msgID, msgStr))
			seq := msgStr[:2]
			reqid := msgStr[3:]

			getResponseFromStream := models.GetResponseFromStream{
				ErrorString: fmt.Sprintf("_ERROR_ ReqID %s in seq %s has been too long in stream %s - aborting sequance", reqid, seq, stream), 						
			}

			if ok, errMsg := ds.SetFinalResponseInAdapterStream(reqid, &getResponseFromStream); !ok {
				logger.Log.Error(fmt.Sprintf("Adapter NOT notified for seq termination reqid %s in worker stream %s - Error ", reqid, stream, errMsg))
			} 

		} else {
			logger.Log.Error(fmt.Sprintf("Message %s not found in stream %s", msgID, stream))
		}

	    ds.Rdb.XDel(*ds.Ctx, stream, msgID)
	    ds.Rdb.XAck(*ds.Ctx, stream, stream, msgID)

	}

	return true, ""
}










