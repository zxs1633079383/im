package auth_test

import (
	"testing"

	"im-server/internal/auth"
)

func TestPassword_HashAndCheck(t *testing.T) {
	plain := "s3cr3tP@ssword!"
	hash, err := auth.HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword error: %v", err)
	}
	if hash == plain {
		t.Fatal("hash must not equal plaintext")
	}

	if err := auth.CheckPassword(hash, plain); err != nil {
		t.Fatalf("CheckPassword should succeed: %v", err)
	}
}

func TestPassword_WrongPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correctPassword")
	if err := auth.CheckPassword(hash, "wrongPassword"); err == nil {
		t.Fatal("CheckPassword should fail for wrong password")
	}
}

func TestPassword_EmptyPlaintext(t *testing.T) {
	_, err := auth.HashPassword("")
	if err == nil {
		t.Fatal("HashPassword should fail for empty password")
	}
}

func TestPassword_DifferentHashEachTime(t *testing.T) {
	plain := "samePassword"
	h1, _ := auth.HashPassword(plain)
	h2, _ := auth.HashPassword(plain)
	if h1 == h2 {
		t.Fatal("two hashes of the same password should differ (salt)")
	}
}
