package auth

import (
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

func TestLoginAndParse(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(config.Auth{
		TokenSecret: "unit-test-secret",
		Users: []config.User{{
			Username:     "alice",
			PasswordHash: hash,
			Role:         "admin",
		}},
	}, time.Hour)
	res, err := svc.Login("alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.Parse(res.Token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "alice" || c.Role != "admin" {
		t.Fatalf("%+v", c)
	}
	if _, err := svc.Login("alice", "wrong"); err == nil {
		t.Fatal("expected error")
	}
}
