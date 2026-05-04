package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenManager_RoundTrip(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "ext-llm-webadapter-test")
	tok, expiresAt, err := tm.Issue("alice")
	require.NoError(t, err)
	require.NotEmpty(t, tok)
	require.WithinDuration(t, time.Now().Add(time.Hour), expiresAt, 5*time.Second)

	claims, err := tm.Verify(tok)
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Username())
	assert.Equal(t, "alice", claims.Subject)
	assert.Equal(t, "ext-llm-webadapter-test", claims.Issuer)
}

func TestTokenManager_RejectsExpiredToken(t *testing.T) {
	tm := NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "iss")

	past := time.Now().Add(-2 * time.Hour)
	pastTM := tm.withClock(func() time.Time { return past })
	tok, _, err := pastTM.Issue("alice")
	require.NoError(t, err)

	_, err = tm.Verify(tok)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "exp"))
}

func TestTokenManager_RejectsBadSignature(t *testing.T) {
	a := NewTokenManager([]byte("secret-a-aaaaaaaaaaaaaaaaaaaaaaa"), time.Hour, "iss")
	b := NewTokenManager([]byte("secret-b-bbbbbbbbbbbbbbbbbbbbbbb"), time.Hour, "iss")
	tok, _, err := a.Issue("alice")
	require.NoError(t, err)
	_, err = b.Verify(tok)
	require.Error(t, err)
}

func TestTokenManager_RejectsWrongIssuer(t *testing.T) {
	a := NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "issuer-a")
	b := NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "issuer-b")
	tok, _, err := a.Issue("alice")
	require.NoError(t, err)
	_, err = b.Verify(tok)
	require.Error(t, err)
}

func TestTokenManager_RejectsAlgNone(t *testing.T) {
	// Forge a token with alg=none and ensure it is rejected.
	claims := jwt.MapClaims{"sub": "alice", "iss": "iss"}
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	tm := NewTokenManager([]byte("test-secret-32+chars-for-hs256-ok"), time.Hour, "iss")
	_, err = tm.Verify(signed)
	require.Error(t, err)
}

func TestTokenManager_IssueRequiresUsername(t *testing.T) {
	tm := NewTokenManager([]byte("k"), time.Minute, "iss")
	_, _, err := tm.Issue("")
	require.Error(t, err)
}
