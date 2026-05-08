package util

import "testing"

func TestConfigureJWTUsesExplicitSecret(t *testing.T) {
	if err := ConfigureJWT("fixed-secret"); err != nil {
		t.Fatalf("configure jwt: %v", err)
	}

	token := GenerateJwt(42)
	if token == "" {
		t.Fatal("expected generated token")
	}

	claims, err := ParseJwtWithClaims(token)
	if err != nil {
		t.Fatalf("parse jwt: %v", err)
	}

	subject, err := claims.GetSubject()
	if err != nil {
		t.Fatalf("get subject: %v", err)
	}

	if subject != "42" {
		t.Fatalf("expected subject 42, got %s", subject)
	}
}

func TestConfigureJWTFallsBackToEnvironmentSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "env-secret")

	if err := ConfigureJWT(""); err != nil {
		t.Fatalf("configure jwt from env: %v", err)
	}

	token := GenerateJwt(7)
	if token == "" {
		t.Fatal("expected generated token")
	}

	claims, err := ParseJwtWithClaims(token)
	if err != nil {
		t.Fatalf("parse jwt: %v", err)
	}

	subject, err := claims.GetSubject()
	if err != nil {
		t.Fatalf("get subject: %v", err)
	}

	if subject != "7" {
		t.Fatalf("expected subject 7, got %s", subject)
	}
}
