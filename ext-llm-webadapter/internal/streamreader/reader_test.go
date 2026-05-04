package streamreader

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	models "ext-llm-common/models"
)

func newMiniredis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	mr := miniredis.RunT(t)
	return mr
}

func TestReader_AwaitContent_Success(t *testing.T) {
	mr := newMiniredis(t)

	chatID := "chat-1"
	streamName := "STREAM_" + chatID
	contentKey := "seq_00_workergroup_anonimyzer_wrkind_03_reqid_" + chatID

	// Plant the final content the JIT-cred user is expected to GET.
	finalContent, err := json.Marshal(models.Content{InputText: "hello world"})
	require.NoError(t, err)
	mr.Set(contentKey, string(finalContent))

	// Plant the stream message that points at it.
	streamPayload, err := json.Marshal(models.GetResponseFromStream{
		ReqRedisAccessPermissions: &models.ReqRedisAccessPermissions{
			UserID:         "default", // miniredis ignores ACL, default user works
			Pwd:            "",
			RedisURL:       mr.Addr(),
			ReadAccessKeys: []string{contentKey},
		},
	})
	require.NoError(t, err)

	// Pre-publish the message; XREAD with $ would otherwise block forever
	// against miniredis since it doesn't run our planter concurrently.
	go func() {
		time.Sleep(50 * time.Millisecond)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer client.Close()
		client.XAdd(context.Background(), &redis.XAddArgs{
			Stream: streamName,
			Values: map[string]interface{}{
				"response": string(streamPayload),
				"next_seq": "01_next-uuid",
			},
		})
	}()

	r := New(mr.Addr(), 2*time.Second, 5*time.Second)
	content, nextSeq, err := r.AwaitContent(context.Background(), chatID, "default", "")
	require.NoError(t, err)
	require.NotNil(t, content)
	assert.Equal(t, "hello world", content.InputText)
	assert.Equal(t, "01_next-uuid", nextSeq)
}

func TestReader_AwaitContent_PipelineError(t *testing.T) {
	mr := newMiniredis(t)

	chatID := "chat-err"
	streamName := "STREAM_" + chatID

	streamPayload, err := json.Marshal(models.GetResponseFromStream{
		ErrorString: "_ERROR_ pipeline broke",
	})
	require.NoError(t, err)

	go func() {
		time.Sleep(50 * time.Millisecond)
		client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer client.Close()
		client.XAdd(context.Background(), &redis.XAddArgs{
			Stream: streamName,
			Values: map[string]interface{}{
				"response": string(streamPayload),
				"next_seq": "",
			},
		})
	}()

	r := New(mr.Addr(), 2*time.Second, 5*time.Second)
	_, _, err = r.AwaitContent(context.Background(), chatID, "default", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pipeline broke")
}

func TestReader_AwaitContent_TimeoutNoMessage(t *testing.T) {
	mr := newMiniredis(t)
	r := New(mr.Addr(), 2*time.Second, 200*time.Millisecond)
	_, _, err := r.AwaitContent(context.Background(), "no-such-chat", "default", "")
	require.Error(t, err)
}
