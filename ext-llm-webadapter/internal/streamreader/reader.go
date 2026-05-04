package streamreader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	commonmodels "ext-llm-common/models"
)

// Reader knows how to wait for the gateway's pipeline reply on
// STREAM_<chat_id> and then dereference the JIT credentials in the message to
// fetch the final content from the gateway's data-source Redis.
type Reader struct {
	addr        string
	dialTimeout time.Duration
	blockTime   time.Duration
}

func New(addr string, dialTimeout, blockTime time.Duration) *Reader {
	return &Reader{addr: addr, dialTimeout: dialTimeout, blockTime: blockTime}
}

// AwaitContent blocks until a message is published to STREAM_<chat_id>,
// dereferences the JIT credentials it carries, fetches the final content from
// the data-source Redis, and returns the parsed Content plus the next request
// key the caller should use on its next turn.
func (r *Reader) AwaitContent(ctx context.Context, chatID, adapterID, streamPwd string) (*commonmodels.Content, string, error) {
	streamName := "STREAM_" + chatID

	streamRdb, err := r.dialAs(adapterID, streamPwd)
	if err != nil {
		return nil, "", fmt.Errorf("connect to stream redis as %q: %w", adapterID, err)
	}
	defer streamRdb.Close()

	res, err := streamRdb.XRead(ctx, &redis.XReadArgs{
		Streams: []string{streamName, "$"},
		Block:   r.blockTime,
		Count:   1,
	}).Result()

	if errors.Is(err, redis.Nil) {
		return nil, "", fmt.Errorf("timeout waiting for %s", streamName)
	}
	if err != nil {
		return nil, "", fmt.Errorf("xread %s: %w", streamName, err)
	}
	if len(res) == 0 || len(res[0].Messages) == 0 {
		return nil, "", fmt.Errorf("empty stream result for %s", streamName)
	}

	msg := res[0].Messages[0]
	respRaw, _ := msg.Values["response"].(string)
	nextSeq, _ := msg.Values["next_seq"].(string)

	var streamMsg commonmodels.GetResponseFromStream
	if err := json.Unmarshal([]byte(respRaw), &streamMsg); err != nil {
		return nil, "", fmt.Errorf("decode stream response: %w", err)
	}
	if streamMsg.ReqRedisAccessPermissions == nil {
		if streamMsg.ErrorString != "" {
			return nil, nextSeq, errors.New(streamMsg.ErrorString)
		}
		return nil, nextSeq, errors.New("stream message had neither permissions nor error")
	}
	perms := streamMsg.ReqRedisAccessPermissions
	if len(perms.ReadAccessKeys) == 0 {
		return nil, nextSeq, errors.New("stream permissions had no read keys")
	}

	contentRdb, err := r.dialAs(perms.UserID, perms.Pwd, redisURLOverride(perms.RedisURL))
	if err != nil {
		return nil, nextSeq, fmt.Errorf("connect with jit creds: %w", err)
	}
	defer contentRdb.Close()

	readKey := perms.ReadAccessKeys[0]
	raw, err := contentRdb.Get(ctx, readKey).Result()
	if err != nil {
		return nil, nextSeq, fmt.Errorf("read content key %s: %w", readKey, err)
	}

	var content commonmodels.Content
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, nextSeq, fmt.Errorf("decode content: %w", err)
	}
	return &content, nextSeq, nil
}

type dialOption func(*redis.Options)

func redisURLOverride(url string) dialOption {
	return func(o *redis.Options) {
		if url != "" {
			o.Addr = strings.TrimPrefix(url, "redis://")
		}
	}
}

func (r *Reader) dialAs(username, password string, opts ...dialOption) (*redis.Client, error) {
	o := &redis.Options{
		Addr:        r.addr,
		Username:    username,
		Password:    password,
		DialTimeout: r.dialTimeout,
		PoolSize:    2,
		ReadTimeout: r.blockTime + 5*time.Second,
	}
	for _, opt := range opts {
		opt(o)
	}
	client := redis.NewClient(o)
	pingCtx, cancel := context.WithTimeout(context.Background(), r.dialTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}
