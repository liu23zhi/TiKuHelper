package util

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// RegistrationBrowserTTL is fixed from issuance; page loads never extend it.
const RegistrationBrowserTTL = 24 * time.Hour
const registrationBrowserPayloadSize = 8 + 16 // issuance second + 128 random bits

// The key is configured once at startup by ConfigureJWT and shared by replicas.
// This signed value has a separate purpose/domain; it is never an auth token.
func registrationBrowserMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, jwtKey)
	mac.Write([]byte("tiku:registration-browser:v1\x00"))
	mac.Write(payload)
	return mac.Sum(nil)
}

func NewRegistrationBrowserCookieValue(now time.Time) (string, error) {
	var payload [registrationBrowserPayloadSize]byte
	binary.BigEndian.PutUint64(payload[:8], uint64(now.Unix()))
	if _, err := rand.Read(payload[8:]); err != nil {
		return "", err
	}
	raw := append(payload[:], registrationBrowserMAC(payload[:])...)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RegistrationBrowserHash accepts only an unexpired value issued by this
// service. The database gets a digest, never the reusable cookie or its nonce.
func RegistrationBrowserHash(value string, now time.Time) string {
	if len(value) != base64.RawURLEncoding.EncodedLen(registrationBrowserPayloadSize+sha256.Size) {
		return ""
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != registrationBrowserPayloadSize+sha256.Size {
		return ""
	}
	payload := raw[:registrationBrowserPayloadSize]
	if !hmac.Equal(raw[registrationBrowserPayloadSize:], registrationBrowserMAC(payload)) {
		return ""
	}
	issued := int64(binary.BigEndian.Uint64(payload[:8]))
	age := now.Unix() - issued
	if age < 0 || age >= int64(RegistrationBrowserTTL/time.Second) {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
