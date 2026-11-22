package util

import (
	"errors"
	"strings"
	"testing"
)

func TestHashPasswordEnforcesBcryptByteLimit(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", BcryptMaxPasswordBytes)); err != nil {
		t.Fatalf("hash password at bcrypt byte limit: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", BcryptMaxPasswordBytes+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("expected bcrypt length sentinel, got %v", err)
	}
	if _, err := HashPassword(strings.Repeat("中", 25)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("expected multibyte password over byte limit to fail, got %v", err)
	}
}

func TestVerifyPasswordRejectsOverlongCandidatesIncludingLegacyPlaintext(t *testing.T) {
	hash, err := HashPassword("short-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	overlong := strings.Repeat("a", BcryptMaxPasswordBytes+1)
	if matched, _ := VerifyPassword(hash, overlong); matched {
		t.Fatal("bcrypt hashes must reject overlong candidates")
	}
	if matched, _ := VerifyPassword(overlong, overlong); matched {
		t.Fatal("legacy plaintext verification must reject overlong candidates")
	}
}
