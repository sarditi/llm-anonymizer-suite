package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload issued to authenticated callers. Username is
// surfaced as a method so it does not collide with RegisteredClaims.Subject
// (both would otherwise serialize to the "sub" key).
type Claims struct {
	jwt.RegisteredClaims
}

// Username returns the authenticated subject.
func (c *Claims) Username() string { return c.Subject }

// TokenManager mints and verifies HS256 JWTs. The secret is held in memory for
// the lifetime of the process; callers should rotate by restarting.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
	issuer string
	now    func() time.Time
}

func NewTokenManager(secret []byte, ttl time.Duration, issuer string) *TokenManager {
	return &TokenManager{
		secret: secret,
		ttl:    ttl,
		issuer: issuer,
		now:    time.Now,
	}
}

// withClock is exposed for tests so they can pin the clock.
func (m *TokenManager) withClock(now func() time.Time) *TokenManager {
	cp := *m
	cp.now = now
	return &cp
}

func (m *TokenManager) Issue(username string) (string, time.Time, error) {
	if username == "" {
		return "", time.Time{}, errors.New("username required")
	}
	now := m.now()
	expiresAt := now.Add(m.ttl)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now.Add(-1 * time.Second)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, expiresAt, nil
}

func (m *TokenManager) Verify(tokenStr string) (*Claims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(m.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(m.now),
	)
	parsed, err := parser.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
