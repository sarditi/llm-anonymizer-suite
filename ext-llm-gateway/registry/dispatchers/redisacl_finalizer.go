package dispatchers


import (
    "ext-llm-gateway/models"
	"ext-llm-gateway/logger"
	"ext-llm-gateway/orchestrator"

    "fmt"
    "encoding/json"

    "github.com/google/uuid"
)
// See orchestrator/interface.go for the Dowrok interface (Dispatcher). This is an implementation of the interface for REDISACL_FINALIZER as mentioned in registry/registry.go
// The Dispatchers send to the stream of the reqid (STREAM_<reqid>) via SetFinalResponseInAdapterStream the JIT permissions to access the finalr content
// after it had went through all the workers
type RedisAclFinalizer struct{
    redisURL        string
}

func (d *RedisAclFinalizer)InitDispatcher(cfg models.Dispatcher)  {
    d.redisURL = cfg.DatasourceUrl
}

func NewRedisAclFinalizer(redisURL string) *RedisAclFinalizer {
    
    logger.Log.Info("Starting RedisAclFinalizer Dispatcher..")
    r := &RedisAclFinalizer{
        redisURL: redisURL,
    }

	return r
}

func (d *RedisAclFinalizer)Dowork(di *models.DispatcherMandatoryInputs, reqid string,seq int,ds orchestrator.Datasource,synchDS orchestrator.SyncDSForDispatcher,args ...any) (bool, string) {

    logger.Log.WithWorker(di).Debug(fmt.Sprintf("Processing reqid - %s", reqid))

    reqRedisAccessPermissions := &models.ReqRedisAccessPermissions{
        ReadAccessKeys:  []string{},
        WriteAccessKeys: []string{},
        ReadAccessContentMeta: 	"",
	    WriteAccessContentMeta:	"",
        RedisURL: d.redisURL,
    }
    // Creating the temporary REDIS user that will enable the cipher dispatcher with the required access rights 
    reqRedisAccessPermissions.UserID = fmt.Sprintf("%s_%d_ACL_%s",di.WorkerGroup, di.WorkerID,reqid)
    reqRedisAccessPermissions.Pwd = uuid.New().String()
    reqRedisAccessPermissions.ReadAccessKeys = GenerateKeysList(reqid, di.WorkerGroup,di.WorkerID, 1, seq)

    data, _ := json.Marshal(reqRedisAccessPermissions)

    // For debugging purposes only #####################################################################
    logger.Log.WithWorker(di).Debug(fmt.Sprintf("ACL for reqid %s - %s", reqid, string(data)))
    // For debugging purposes only #####################################################################

    writeKeys,readKeys := MergeWriteReadKeys(reqRedisAccessPermissions.WriteAccessKeys, reqRedisAccessPermissions.ReadAccessKeys, reqRedisAccessPermissions.WriteAccessContentMeta, reqRedisAccessPermissions.ReadAccessContentMeta)
    
    if ok, errMsg := ds.SetTempAclUserPermissions(reqRedisAccessPermissions.UserID, reqRedisAccessPermissions.Pwd,readKeys,writeKeys); !ok {
        return ok, errMsg
    }

    getResponseFromStream := models.GetResponseFromStream{
        ReqRedisAccessPermissions:  reqRedisAccessPermissions,
    }

    if ok, errMsg := synchDS.SetFinalResponseInAdapterStream(reqid, &getResponseFromStream); !ok {
        return ok, errMsg
    }

	return true, ""
}