package ds

import (
	"ext-llm-gateway/utils"
	"ext-llm-gateway/models"
	"ext-llm-gateway/logger"

	"context"
	"fmt"
	"strings"
	"encoding/json"

	"github.com/redis/go-redis/v9"
)

// See orchestrator/interface.go for the Datasource interface. This is an implementation of the interface for Redis DB
// The instance is being instantiated "reflection-like" with registry/registry.go
type RedisDatasource struct {
	Rdb *redis.Client
	Ctx *context.Context
	RedisUrl string
}

func (ds *RedisDatasource) InitDataSource(cfg models.DataSourceConfig){
    
    logger.Log.Info("Starting RedisDatasource..")

	options := utils.InitRedisDataSource(cfg)
    
	ctx, _ := context.WithCancel(context.Background())

	ds.Rdb = redis.NewClient(options)
	ds.Ctx = &ctx 
	ds.RedisUrl = cfg.Addr  
		// Test connection
	_, err := ds.Rdb.Ping(*ds.Ctx).Result()
	if err != nil {
		panic("Failed to connect to Redis " + cfg.Addr + " with username " + cfg.Username + " " + err.Error())
	}
	logger.Log.Info("Connected to Redis " + cfg.Addr + " with username " + cfg.Username)
}

func (ds *RedisDatasource) DBMaintainenceProc(overdueChatIds []string) {
	utils.PerformCleanup(overdueChatIds, ds.Rdb, ds.Ctx)
	utils.CleanupTempUsers(ds.Ctx, ds.Rdb, overdueChatIds)
}

//Return value dependeds whether raw data was retrieved or only permissions granted
func (ds *RedisDatasource) GetLastNContents(reqid string, workergrp string, workerInd int,lastNsequances int,startingSeq int,args ...any) (bool, string) {
	return true, "PLACEHOLDER"
}

// sets the content of a key
func (ds *RedisDatasource) SetContent(reqid string, content *models.Content, workergrp string, workerInd int,sequance int,persistTimeSec int, args ...any) (bool, string) {
	key := fmt.Sprintf(models.KeyPattern,sequance,workergrp,workerInd,reqid)
	data, _ := json.Marshal(*content)

	logger.Log.Debug(fmt.Sprintf("Setting raw content %s to key %s with persistTimeSec %d", string(data), key, persistTimeSec))
	if err := ds.Rdb.Set(*ds.Ctx, key, data, 0).Err(); err != nil {
			logger.Log.Fatal("Error saving to Redis: " + err.Error())
			return false, "Error saving to Redis: " + err.Error()
	}

	return true, ""
}

//Set metadata content of the key which related ALL sequances e.g. metaReqID 
func (ds *RedisDatasource) SetContentMeta(reqid string, data []byte, workergrp string,persistTimeSec int, args ...any) (bool, string) {
	return true, "PLACEHOLDER"
}

func (ds *RedisDatasource) GetContentMeta(reqid string, workergrp string,args ...any) (bool, string) {
	return true, "PLACEHOLDER"
}

// SetTempAclUserPermissions sets temporary ACL user permissions in Redis
func (ds *RedisDatasource) SetTempAclUserPermissions(
	aclTempUser string,
	aclTempUserPassword string,
	readAccessKeys []string,
	writeAccessKeys []string,
	args ...any,
) (bool, string) {

	// Base ACL command: reset → enable → set password
	aclArgs := []string{"SETUSER", aclTempUser, "reset", "on", ">" + aclTempUserPassword}

	// Closure to convert []string -> []any
	stringSliceToAnySlice := func(s []string) []any {
		a := make([]any, len(s))
		for i, v := range s {
			a[i] = v
		}
		return a
	}

	// +get selector (single string)
	if len(readAccessKeys) > 0 {
		getSelector := "(+get ~" + strings.Join(readAccessKeys, " ~") + ")"
		aclArgs = append(aclArgs, getSelector)
	}

	// +set selector (single string)
	if len(writeAccessKeys) > 0 {
		setSelector := "(+set ~" + strings.Join(writeAccessKeys, " ~") + ")"
		aclArgs = append(aclArgs, setSelector)
	}

	// Execute the ACL SETUSER command
	err := ds.Rdb.Do(*ds.Ctx, append([]any{"ACL"}, stringSliceToAnySlice(aclArgs)...)...).Err()
	if err != nil {
		return false, err.Error()
	}

	return true, ""
}




