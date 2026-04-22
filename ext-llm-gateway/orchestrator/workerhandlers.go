package orchestrator

import (
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"
	"ext-llm-gateway/utils"

	"fmt"
	"strconv"
	"strings"
	"encoding/json"
	"time"

	"google.golang.org/grpc/codes"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)
/*
THE ACTUAL ORCHETRATION OF THE WORK GRPOUP	
*/
type Mux struct {
	DS Datasource
	Syn *SynchRedisDatasource
	firstWorkerStream string
	workerGrp 		  string  
	workerName 		  string 
	workerInd		  int
	Prompt 			  *string
	sessionTTL        time.Duration
	activeSessions    string 
}

const activeSessionsTemplate = "active_sessions"
const vaccumFrequencySecs = 300
const defaultSessionTTLSecs = 300
//Initiate the synchronizer
func NewMux(dataS Datasource, synchDS *SynchRedisDatasource, prompt *string, workerGrpName string, sessionTTL int) *Mux {

	ttl := time.Second * time.Duration(sessionTTL)
	if sessionTTL == 0 {
		ttl = time.Second * time.Duration(defaultSessionTTLSecs)
	}

	mux := &Mux{
		DS: dataS,
		Syn: synchDS,
		Prompt: prompt,
		workerGrp: workerGrpName,
		sessionTTL: ttl,
		activeSessions: workerGrpName + "_" + activeSessionsTemplate,  
	} 
	mux.cleanupExpiredSessions()
	return mux
}
//Check sessions that are passed their sessionTTL, meaning no activity was done on the reqid for ore than sessionTTL value
func (o *Mux) checkExpiredSessions() ([]string){
    // 1. Calculate the cutoff (anything smaller than this is expired)
    cutoff := time.Now().Add(-1 * o.sessionTTL).Unix()
    maxScore := fmt.Sprintf("%d", cutoff)

    // 2. Find all members (key names) in the ZSet that have expired
    expiredKeys, err := o.Syn.Rdb.ZRangeByScore(*o.Syn.Ctx, o.activeSessions, &redis.ZRangeBy{
        Min: "-inf",
        Max: maxScore,
    }).Result()

    if err != nil {
        logger.Log.Error(fmt.Sprintf("Failed to fetch expired keys from %s: %v", o.activeSessions, err))
        return []string{}
    }

	return expiredKeys
}
//Perform a cleanup all all data in SyncDB and Datasource for reqid's with expired sessions
func (o *Mux)cleanupExpiredSessions() {
    go func() {
        // Creates a channel that delivers a signal every 60 seconds
        ticker := time.NewTicker(1 * time.Minute)
        defer ticker.Stop()

        logger.Log.Debug("Redis Synch cleanup worker started (Interval: 1m)")

        for {
            select {
            case <-ticker.C:
			    expiredSessions := o.checkExpiredSessions()
				if len(expiredSessions) > 0 {
					logger.Log.Debug("Expired sessions are : " + strings.Join(expiredSessions, ", "))
					utils.PerformCleanup(expiredSessions, o.Syn.Rdb, o.Syn.Ctx)
					o.DS.DBMaintainenceProc(expiredSessions)
					// 1. Create the interface slice with the same length
					args := make([]interface{}, len(expiredSessions))

					// 2. Populate it
					for i, v := range expiredSessions {
						args[i] = v
					}

					// 3. Pass it to ZRem using the spread operator
					if err := o.Syn.Rdb.ZRem(*o.Syn.Ctx, o.activeSessions, args...).Err(); err != nil {
						logger.Log.Error("Failed to remove from ZSet: " + err.Error())
					}				
				}

            case <-(*o.Syn.Ctx).Done():
                logger.Log.Error("Stopping Redis cleanup worker...")
                return
            }
        }
    }()
}
//Sets the first worker's stream
func (o *Mux) SetFirstWorkerStream(workerGrp string, workerName string, workerInd int){
	o.workerGrp  = workerGrp
	o.workerName = workerName
	o.workerInd  = workerInd
	o.firstWorkerStream = fmt.Sprintf(models.WorkerStreamTemplateName,workerGrp,workerInd)
}
// A method that is being executed for every SetRequest
func (o *Mux)SetReqFromAdapterInternal(req *models.SetRequest)  (int, string, string) {

	var metadataChatJ models.InitChatRequest
	metadateChat := o.Syn.GetRequestMetadata(req.InternalChatID)
	
	//Check that chat id was created
	if metadateChat == "" {
		logger.Log.Error(req.InternalChatID + " chat was not initiated")
		return int(codes.AlreadyExists), req.InternalChatID, req.InternalChatID + " was not initiated or expiration time past"
	}
	// unmarshel the metadata of chat
	logger.Log.Debug("Umarsheling metadata for chat id " + req.InternalChatID)
	if err :=json.Unmarshal([]byte(metadateChat), &metadataChatJ); err != nil {
		logger.Log.Error("Failed Umarsheling metadata for chat id " + req.InternalChatID)
		return int(codes.Internal), req.InternalChatID, "Umarsheling metadata for chat id " + req.InternalChatID
	}
	
	logger.Log.Debug("Retrieving the next sequance key for chat id " + req.InternalChatID)
	seqKeyVal := o.Syn.GetSequanceKey(req.InternalChatID)
	//Check if the right sequance key is in place
	if seqKeyVal != req.ReqKey {
		return int(codes.Internal),req.InternalChatID, "Wrong sequance key"
	}

	// A new request key that will be shared once the currentrequest is finished processing - will be delivered to the adapter q as part of the new message
	logger.Log.Debug("Assigning request " + req.ReqKey + " of chat id " + req.InternalChatID + " to the first worker...")
	
	//Set the content in the data source DB
	seq, _ := strconv.Atoi(seqKeyVal[:2])
	if ok, errMsg := o.DS.SetContent(req.InternalChatID, &req.Content, o.workerGrp, o.workerInd,seq,metadataChatJ.PersistanceTimeSeconds); !ok {
		return int(codes.Internal),"", errMsg
	} 
	logger.Log.Debug(fmt.Sprintf("Calling SetContentMeta with reqid %s and persistTimeSec %d", req.InternalChatID, metadataChatJ.PersistanceTimeSeconds))
	if ok, errMsg := o.DS.SetContentMeta(req.InternalChatID, []byte(`{}`), o.workerGrp,metadataChatJ.PersistanceTimeSeconds); !ok {
		return int(codes.Internal),"", errMsg
	} 

	seqKeyNewVal := fmt.Sprintf("%02d_%s", seq+1, uuid.New().String())

	o.Syn.SetWorker0Atomic(req.InternalChatID, seqKeyNewVal, o.firstWorkerStream, fmt.Sprintf("%02d_%s", seq, req.InternalChatID), metadataChatJ.PersistanceTimeSeconds)
	//Marks an activity for the reqid
	o.setSessionTimestamp(req.InternalChatID)
	return int(codes.OK), req.InternalChatID,"New request was set"
}

// A method that is being executed for every SetRequest
//TODO: we need to tailor security in this phase
func (o *Mux)InitReq(data []byte, adapters []string) (int, string, string, string, string) {
	
	var initChatReq models.InitChatRequest
	logger.Log.Debug("In InitChat....")
	// Validate input
	if err := json.Unmarshal([]byte(data), &initChatReq); err != nil {
		return int(codes.InvalidArgument),"", "Unable to convert input body to JSON","",""	
	}

	var ok bool
	var errMsg, pwd string
	logger.Log.Debug("In initReqInternal....")

	now := time.Now()
	initChatReq.CreatedAt = &now

	internal_chat_id := uuid.New().String()

	if initChatReq.PersistanceTimeSeconds == 0 {
		initChatReq.PersistanceTimeSeconds = models.PersistanceTimeSeconds
	} else {
		initChatReq.PersistanceTimeSeconds = initChatReq.PersistanceTimeSeconds * 1e9
	}

	data, err := json.Marshal(initChatReq)
	if err != nil {
		logger.Log.Error("Error marshaling JSON: " + err.Error())
		return int(codes.Internal),"", err.Error(),"",""	
	}

	if exists := func() bool { for _, v := range adapters { if v == initChatReq.AdapterID { return true } }; return false }();!exists {
		logger.Log.Error("Adapter ID " + initChatReq.AdapterID + " is not configured")
		return int(codes.Internal),"", "Adapter ID " + initChatReq.AdapterID + " is not configured","",""
	}

	//Generate a key that will be used by the adapter to set a new request - it can set a request only with this key
	seqWithVal0Key := fmt.Sprintf("00_%s", uuid.New().String())
	if ok, errMsg = o.Syn.SetSequanceKey(internal_chat_id, seqWithVal0Key, initChatReq.PersistanceTimeSeconds); !ok {
		return int(codes.Internal),"", errMsg,"",""
	}
	if ok, errMsg = o.Syn.InitRequest(internal_chat_id, string(data), initChatReq.PersistanceTimeSeconds); !ok {
		return int(codes.Internal),"", errMsg,"",""
	}
	if ok, errMsg, pwd = o.Syn.AdapterStreamCreation(initChatReq.AdapterID, internal_chat_id, initChatReq.PersistanceTimeSeconds); !ok {
		return int(codes.Internal),"", errMsg,"",""
	}
	// Success response
	o.setSessionTimestamp(internal_chat_id)
	return int(codes.OK),internal_chat_id, "New Internal chat ID was created",seqWithVal0Key,pwd
}
//Set current date/time for a reqid as an activity just happened
func (o *Mux)setSessionTimestamp(key string) {
	now := time.Now().Unix()
	if err := o.Syn.Rdb.ZAdd(*o.Syn.Ctx, o.activeSessions, redis.Z{
            Score:  float64(now),
            Member: key,
        }).Err(); err != nil {
        panic(err)
    }
}

