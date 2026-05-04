package auth

import (
	"errors"

	"ext-llm-webadapter/internal/config"
)

// ErrInvalidCredentials is returned by UserStore.Authenticate for both unknown
// usernames and bad passwords. The two cases share an error to avoid leaking
// which usernames exist.
var ErrInvalidCredentials = errors.New("invalid username or password")

// UserStore looks up users from the loaded configuration and verifies their
// passwords against bcrypt hashes.
type UserStore struct {
	users map[string]string // username -> bcrypt hash
}

func NewUserStore(users []config.UserCredentials) *UserStore {
	m := make(map[string]string, len(users))
	for _, u := range users {
		m[u.Username] = u.PasswordBcrypt
	}
	return &UserStore{users: m}
}

func (s *UserStore) Authenticate(username, password string) error {
	hash, ok := s.users[username]
	if !ok {
		return ErrInvalidCredentials
	}
	if err := VerifyPassword(hash, password); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

func (s *UserStore) Has(username string) bool {
	_, ok := s.users[username]
	return ok
}
