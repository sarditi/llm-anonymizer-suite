package utils

import (
	"ext-llm-gateway/logger"
	"ext-llm-gateway/models"

	"time"
	"context"
	"fmt"
    "net/http"
    "strings"
    "os"

	"github.com/redis/go-redis/v9"
)

func RedisOptionsFromURL(url, username string, password string) (*redis.Options, error) {
	opt, err := redis.ParseURL("redis://" + url)
	if err != nil {
		return nil, err
	}
	opt.Username = username
	opt.Password = password

    opt.PoolSize     = 2         // Minimum possible footprint
    opt.MinIdleConns = 0     // Don't keep anything open
    opt.PoolTimeout  = 1 * time.Second
    
    // This tells the client to close connections as soon as the function ends
    opt.ConnMaxIdleTime = 1 * time.Millisecond

	return opt, nil
}

func GRPCToHTTPStatus(code int) int {
    switch code {
    case 0: // codes.OK
        return http.StatusOK
    case 3: // codes.InvalidArgument
        return http.StatusBadRequest
    case 5: // codes.NotFound
        return http.StatusNotFound
    case 6: // codes.AlreadyExists (This was your crash!)
        return http.StatusConflict
    case 16: // codes.Unauthenticated
        return http.StatusUnauthorized
    default:
        return http.StatusInternalServerError
    }
}

func InitRedisDataSource(cfg models.DataSourceConfig) (*redis.Options){
    
    logger.Log.Info("Starting RedisDatasource..")

	RedisSecretFilePath := os.Getenv(cfg.Password)
	var password string
	if RedisSecretFilePath != "" {
		data, err := os.ReadFile(RedisSecretFilePath)
		if err != nil {
			panic(err.Error())
		}
		password = strings.TrimSpace(string(data))
	} else {
		password = "mysecret"
	}

	options := &redis.Options{
		Addr:     cfg.Addr,
		Username: cfg.Username,
		Password: password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
		// Convert the JSON integers back to Go Durations
		ConnMaxIdleTime: time.Duration(cfg.ConnMaxIdleTime) * time.Second,
		ReadTimeout:     time.Duration(cfg.ReadTimeout) * time.Millisecond,
	}
    return options
}

func PerformCleanup(expiredKeys []string, rdb *redis.Client, ctx *context.Context) {

    // if isACL {
    //     cleanupTempUsers(ctx, rdb, expiredKeys)
    // } else {
    //     if err := rdb.Del(ctx, expiredKeys...).Err(); err != nil {
    //         logger.Log.Error(fmt.Sprintf("Failed to DEL actual keys: %v", err))
    //         // We continue anyway to try and clean the tracker
    //     }
    // }

    for _, id := range expiredKeys {
        // Create the pattern, e.g., "*abc*"
        pattern := fmt.Sprintf("*%s*", id)
        
        // Iterate through keys matching the pattern
        iter := rdb.Scan(*ctx, 0, pattern, 0).Iterator()
        
        var keysToDelete []string
        for iter.Next(*ctx) {
            keysToDelete = append(keysToDelete, iter.Val())
        }

        if err := iter.Err(); err != nil {
            logger.Log.Error("Scan error: " + err.Error())
            continue
        }

        // D the specific keys  found
        if len(keysToDelete) > 0 {
            rdb.Unlink(*ctx, keysToDelete...)
            logger.Log.Debug(fmt.Sprintf("Deleted keys for pattern %s: %v", pattern, keysToDelete))
        }
    }
}

func CleanupTempUsers(ctx *context.Context, rdb *redis.Client, patterns []string) {
    var totalKilled int64

    // 1. Get all existing ACL users to find matches
    // ACL LIST returns strings like "user username on >password ~keys* +@all"
    allUsersInfo, err := rdb.Do(*ctx, "ACL", "LIST").StringSlice()
    if err != nil {
        logger.Log.Error("failed to fetch ACL list: " + err.Error())
        return
    }

    // 2. Extract just the usernames from the list
    var existingUsers []string
    for _, info := range allUsersInfo {
        parts := strings.Fields(info)
        if len(parts) > 1 {
            existingUsers = append(existingUsers, parts[1])
        }
    }

    // 3. Loop through patterns and find matches in existingUsers
    for _, pattern := range patterns {
        if pattern == "" {
            continue
        }

        for _, username := range existingUsers {
            // Check if the actual username contains the pattern (case-insensitive or exact)
            if strings.Contains(username, pattern) {
                
                // A. Delete the ACL user
                err := rdb.Do(*ctx, "ACL", "DELUSER", username).Err()
                if err != nil {
                    logger.Log.Error(fmt.Sprintf("failed to delete ACL user %s: %v", username, err))
                    continue
                }

                // B. Kill active connections
                deletedCount, err := rdb.ClientKillByFilter(*ctx, "USER", username).Result()
                if err != nil {
                    // Fallback to raw command
                    rdb.Do(*ctx, "CLIENT", "KILL", "USER", username)
                }
                totalKilled += deletedCount
                
                logger.Log.Info(fmt.Sprintf("Pattern '%s' matched and cleaned up user: %s", pattern, username))
            }
        }
    }
}