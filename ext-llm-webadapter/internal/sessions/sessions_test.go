package sessions

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_PutAndGet(t *testing.T) {
	s := New(time.Hour)
	s.Put("chat-1", "alice", "pwd-1")

	got, err := s.Get("chat-1", "alice")
	require.NoError(t, err)
	assert.Equal(t, "chat-1", got.ChatID)
	assert.Equal(t, "alice", got.Owner)
	assert.Equal(t, "pwd-1", got.StreamPwd)
}

func TestStore_OwnerEnforcement(t *testing.T) {
	s := New(time.Hour)
	s.Put("chat-1", "alice", "pwd-1")
	_, err := s.Get("chat-1", "bob")
	assert.ErrorIs(t, err, ErrSessionForbidden)
}

func TestStore_NotFound(t *testing.T) {
	s := New(time.Hour)
	_, err := s.Get("nope", "alice")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestStore_TTLExpiry(t *testing.T) {
	now := time.Now()
	tick := int64(0)
	s := New(time.Second).WithClock(func() time.Time {
		return now.Add(time.Duration(atomic.LoadInt64(&tick)) * time.Second)
	})
	s.Put("c1", "alice", "pwd")

	atomic.StoreInt64(&tick, 0)
	got, err := s.Get("c1", "alice")
	require.NoError(t, err)
	assert.Equal(t, "c1", got.ChatID)

	atomic.StoreInt64(&tick, 5) // 5s in the future, ttl is 1s
	_, err = s.Get("c1", "alice")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestStore_Cleanup(t *testing.T) {
	now := time.Now()
	tick := int64(0)
	s := New(time.Second).WithClock(func() time.Time {
		return now.Add(time.Duration(atomic.LoadInt64(&tick)) * time.Second)
	})
	s.Put("c1", "alice", "pwd")
	s.Put("c2", "alice", "pwd")
	atomic.StoreInt64(&tick, 5)
	dropped := s.Cleanup()
	assert.Equal(t, 2, dropped)
	assert.Equal(t, 0, s.Len())
}

func TestStore_Delete(t *testing.T) {
	s := New(time.Hour)
	s.Put("c1", "alice", "pwd")
	s.Delete("c1")
	_, err := s.Get("c1", "alice")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestStore_TouchUpdatesLastUsed(t *testing.T) {
	now := time.Now()
	tick := int64(0)
	s := New(time.Second * 10).WithClock(func() time.Time {
		return now.Add(time.Duration(atomic.LoadInt64(&tick)) * time.Second)
	})
	s.Put("c1", "alice", "pwd")
	atomic.StoreInt64(&tick, 9)
	_, err := s.Get("c1", "alice")
	require.NoError(t, err)
	atomic.StoreInt64(&tick, 18) // 18 - 9 = 9s since touch, still under 10s ttl
	_, err = s.Get("c1", "alice")
	require.NoError(t, err)
}
