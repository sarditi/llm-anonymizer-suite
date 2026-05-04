package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(contents), 0o600))
	return p
}

const validJSON = `{
    "auth": {
        "jwt_secret_inline": "abcdefghijklmnopqrstuvwxyz012345",
        "users": [
            {"username": "u", "password_bcrypt": "$2a$10$abcdefghijklmnopqrstuv"}
        ]
    },
    "gateway": {"base_url": "http://gw:7700", "adapter_id": "ada1"},
    "redis":   {"addr": "redis:6379"}
}`

func TestLoad_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "config.json", validJSON)

	c, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "9555", c.Server.Port)
	assert.Equal(t, "ext-llm-webadapter", c.Auth.Issuer)
	assert.Equal(t, 3600, c.Auth.JWTTTLSeconds)
	assert.Equal(t, "chatgpt", c.Gateway.DefaultLLM)
}

func TestLoad_RejectsMissingUsers(t *testing.T) {
	dir := t.TempDir()
	bad := `{"gateway":{"base_url":"x","adapter_id":"y"},"redis":{"addr":"r"}}`
	path := writeFile(t, dir, "c.json", bad)
	_, err := Load(path)
	require.Error(t, err)
}

func TestLoad_RejectsMissingGateway(t *testing.T) {
	dir := t.TempDir()
	bad := `{"auth":{"jwt_secret_inline":"x","users":[{"username":"u","password_bcrypt":"h"}]},"redis":{"addr":"r"}}`
	path := writeFile(t, dir, "c.json", bad)
	_, err := Load(path)
	require.Error(t, err)
}

func TestResolveJWTSecret_FromFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := writeFile(t, dir, "secret", "  filesecret  \n")
	t.Setenv("MY_SECRET", secretPath)

	c := &Config{Auth: AuthConfig{JWTSecretEnv: "MY_SECRET", JWTSecretInline: "ignored"}}
	got, err := c.ResolveJWTSecret()
	require.NoError(t, err)
	assert.Equal(t, []byte("filesecret"), got)
}

func TestResolveJWTSecret_FromInlineFallback(t *testing.T) {
	c := &Config{Auth: AuthConfig{JWTSecretEnv: "UNSET_VAR_NAME", JWTSecretInline: "inline"}}
	got, err := c.ResolveJWTSecret()
	require.NoError(t, err)
	assert.Equal(t, []byte("inline"), got)
}

func TestResolveJWTSecret_RejectsEmpty(t *testing.T) {
	c := &Config{Auth: AuthConfig{}}
	_, err := c.ResolveJWTSecret()
	require.Error(t, err)
}
