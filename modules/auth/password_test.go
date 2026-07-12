package auth

import (
	"strings"
	"testing"
)

func TestBcryptRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("expected bcrypt hash, got %s", h)
	}
	if err := VerifyPassword(h, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(h, "wrong"); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestRejectUnknownHash(t *testing.T) {
	if err := VerifyPassword("not-a-hash", "x"); err == nil {
		t.Fatal("expected error")
	}
	if err := VerifyPassword("$argon2id$v=19$m=65536,t=2,p=1$invalid$invalid", "x"); err == nil {
		t.Fatal("argon2id must not be accepted")
	}
}
