package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ext-llm-webadapter/internal/config"
)

func TestUserStore_Authenticate(t *testing.T) {
	hash, err := HashPassword("s3cret")
	require.NoError(t, err)

	store := NewUserStore([]config.UserCredentials{
		{Username: "alice", PasswordBcrypt: hash},
	})

	require.NoError(t, store.Authenticate("alice", "s3cret"))

	assert.ErrorIs(t, store.Authenticate("alice", "nope"), ErrInvalidCredentials)
	assert.ErrorIs(t, store.Authenticate("bob", "anything"), ErrInvalidCredentials)
	assert.True(t, store.Has("alice"))
	assert.False(t, store.Has("bob"))
}
