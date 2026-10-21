package util

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var ErrPasswordEmpty = errors.New("password is empty")
var ErrPasswordTooLong = fmt.Errorf("password exceeds bcrypt's %d-byte limit: %w", BcryptMaxPasswordBytes, bcrypt.ErrPasswordTooLong)

const BcryptMaxPasswordBytes = 72

func ValidateBcryptPasswordBytes(password string) error {
	if len([]byte(password)) > BcryptMaxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword hashes a password for storage.
func HashPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", ErrPasswordEmpty
	}
	if err := ValidateBcryptPasswordBytes(password); err != nil {
		return "", err
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

// VerifyPassword verifies a bcrypt hash and supports legacy plaintext values.
// The boolean result reports whether the caller should replace the stored value.
func VerifyPassword(storedPassword string, candidate string) (bool, bool) {
	if storedPassword == "" || candidate == "" {
		return false, false
	}
	if err := ValidateBcryptPasswordBytes(candidate); err != nil {
		return false, false
	}

	if strings.HasPrefix(storedPassword, "$2a$") ||
		strings.HasPrefix(storedPassword, "$2b$") ||
		strings.HasPrefix(storedPassword, "$2y$") {
		return bcrypt.CompareHashAndPassword([]byte(storedPassword), []byte(candidate)) == nil, false
	}

	legacyMatch := subtle.ConstantTimeCompare([]byte(storedPassword), []byte(candidate)) == 1
	return legacyMatch, legacyMatch
}
