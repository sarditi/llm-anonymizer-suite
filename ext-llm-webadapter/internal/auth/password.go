package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword returns a bcrypt hash usable as the value of
// auth.users[].password_bcrypt in config.json.
func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword returns nil when plain matches the stored bcrypt hash.
func VerifyPassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}
