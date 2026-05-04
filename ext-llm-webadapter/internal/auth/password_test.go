package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	require.NoError(t, VerifyPassword(hash, "correct horse battery staple"))
	assert.Error(t, VerifyPassword(hash, "wrong"))
}

func TestVerifyPassword_RejectsMalformedHash(t *testing.T) {
	assert.Error(t, VerifyPassword("not-a-bcrypt-hash", "anything"))
}
