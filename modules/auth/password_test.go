package auth

import (
	"strings"
	"testing"
)

func TestArgon2idRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=2,p=1$") {
		t.Fatalf("unexpected params: %s", h)
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
	if err := VerifyPassword("$2a$10$invalid", "x"); err == nil {
		t.Fatal("bcrypt must not be accepted")
	}
}
