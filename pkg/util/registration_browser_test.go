package util

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestRegistrationBrowserCookieRequiresAuthenticUnexpiredValue(t *testing.T) {
	now := time.Unix(1800000000, 0)
	cookie, err := NewRegistrationBrowserCookieValue(now)
	if err != nil {
		t.Fatal(err)
	}
	digest := RegistrationBrowserHash(cookie, now)
	if len(digest) != 64 || digest == cookie || RegistrationBrowserHash(cookie, now.Add(24*time.Hour-time.Second)) != digest {
		t.Fatal("valid cookie did not produce a stable digest")
	}
	if RegistrationBrowserHash(cookie, now.Add(-time.Second)) != "" || RegistrationBrowserHash(cookie, now.Add(24*time.Hour)) != "" {
		t.Fatal("future or expired value was trusted")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		t.Fatal(err)
	}
	raw[8] ^= 1
	if RegistrationBrowserHash(base64.RawURLEncoding.EncodeToString(raw), now) != "" {
		t.Fatal("unverified browser nonce was accepted")
	}
}
