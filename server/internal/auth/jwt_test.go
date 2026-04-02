package auth_test

import (
	"testing"
	"time"

	"im-server/internal/auth"
)

const testSecret = "test-secret-32-bytes-long-enough!"

func TestJWT_GenerateAndValidate(t *testing.T) {
	token, err := auth.GenerateToken(testSecret, 42, "alice")
	if err != nil {
		t.Fatalf("GenerateToken error: %v", err)
	}
	if token == "" {
		t.Fatal("token should not be empty")
	}

	claims, err := auth.ValidateToken(testSecret, token)
	if err != nil {
		t.Fatalf("ValidateToken error: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("expected UserID 42, got %d", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", claims.Username)
	}
}

func TestJWT_WrongSecret(t *testing.T) {
	token, _ := auth.GenerateToken(testSecret, 1, "bob")
	_, err := auth.ValidateToken("wrong-secret", token)
	if err == nil {
		t.Fatal("ValidateToken should fail with wrong secret")
	}
}

func TestJWT_MalformedToken(t *testing.T) {
	_, err := auth.ValidateToken(testSecret, "not.a.token")
	if err == nil {
		t.Fatal("ValidateToken should fail for malformed token")
	}
}

func TestJWT_EmptySecret(t *testing.T) {
	_, err := auth.GenerateToken("", 1, "carol")
	if err == nil {
		t.Fatal("GenerateToken should fail with empty secret")
	}
}

func TestJWT_ExpiryIsSetTo7Days(t *testing.T) {
	token, _ := auth.GenerateToken(testSecret, 1, "dave")
	claims, _ := auth.ValidateToken(testSecret, token)

	diff := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	expected := 7 * 24 * time.Hour
	if diff < expected-time.Minute || diff > expected+time.Minute {
		t.Errorf("expected expiry ~7 days, got %v", diff)
	}
}
