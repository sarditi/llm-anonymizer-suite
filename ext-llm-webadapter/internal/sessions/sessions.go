package sessions

import (
	"errors"
	"sync"
	"time"
)

// ErrSessionNotFound is returned when a chat_id is unknown or expired.
var ErrSessionNotFound = errors.New("chat session not found")

// ErrSessionForbidden is returned when a different user tries to access another
// user's chat session.
var ErrSessionForbidden = errors.New("chat session does not belong to this user")

// Session is the per-chat state the adapter holds: which user owns it and the
// stream password the gateway minted at /init_chat time.
type Session struct {
	ChatID    string
	Owner     string
	StreamPwd string
	CreatedAt time.Time
	LastUsed  time.Time
}

// Store is an in-memory ttl-bounded map of chat_id -> Session.
//
// In a multi-instance deployment this needs to move to Redis. The interface is
// intentionally small (Put/Get/Touch/Delete) so swapping implementations is a
// non-event.
type Store struct {
	mu  sync.RWMutex
	ttl time.Duration
	now func() time.Time

	entries map[string]*Session
}

func New(ttl time.Duration) *Store {
	return &Store{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]*Session),
	}
}

func (s *Store) WithClock(now func() time.Time) *Store {
	s.now = now
	return s
}

func (s *Store) Put(chatID, owner, streamPwd string) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[chatID] = &Session{
		ChatID:    chatID,
		Owner:     owner,
		StreamPwd: streamPwd,
		CreatedAt: now,
		LastUsed:  now,
	}
}

// Get returns the session if and only if it exists, has not expired, and
// belongs to the supplied owner. Touch the session's LastUsed on success so an
// active chat does not expire mid-conversation.
func (s *Store) Get(chatID, owner string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.entries[chatID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	if s.expired(sess) {
		delete(s.entries, chatID)
		return nil, ErrSessionNotFound
	}
	if sess.Owner != owner {
		return nil, ErrSessionForbidden
	}
	sess.LastUsed = s.now()
	out := *sess
	return &out, nil
}

func (s *Store) Delete(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, chatID)
}

// Cleanup walks the store and drops expired entries. Safe to call from a
// background goroutine.
func (s *Store) Cleanup() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for k, v := range s.entries {
		if s.expired(v) {
			delete(s.entries, k)
			dropped++
		}
	}
	return dropped
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *Store) expired(sess *Session) bool {
	if s.ttl <= 0 {
		return false
	}
	return s.now().Sub(sess.LastUsed) > s.ttl
}
