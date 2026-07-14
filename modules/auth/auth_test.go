package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/golang-jwt/jwt/v5"
)

func TestChangeCredentialsInvalidatesOldTokenAndPersistsReplacement(t *testing.T) {
	hash, err := HashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewForTest([]config.User{{
		Username: "admin", PasswordHash: hash, Role: "admin", Revision: 1, ConfigName: "admin",
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := svc.Login("admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.User
	newSession, err := svc.ChangeCredentials("admin", "old-password", "kiln-admin", "new-password", func(old string, user config.User, revision int64) error {
		if old != "admin" || revision != 1 {
			t.Fatalf("persist identity = %q revision %d", old, revision)
		}
		persisted = user
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Username != "kiln-admin" || persisted.ConfigName != "admin" {
		t.Fatalf("persisted user = %+v", persisted)
	}
	if _, err := svc.Parse(oldSession.Token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("old token error = %v, want invalid token", err)
	}
	claims, err := svc.Parse(newSession.Token)
	if err != nil || claims.Username() != "kiln-admin" || claims.AuthRevision != 2 {
		t.Fatalf("new claims = %+v, err = %v", claims, err)
	}
	if _, err := svc.Login("admin", "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old login error = %v", err)
	}
}

func TestChangeCredentialsValidatesNoOpAndPasswordByteLimit(t *testing.T) {
	hash, err := HashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	newService := func() *Service {
		svc, serviceErr := NewForTest([]config.User{{Username: "admin", PasswordHash: hash, Role: "admin"}}, time.Hour)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		return svc
	}
	if _, err := newService().ChangeCredentials("admin", "old-password", "admin", "", nil); err == nil {
		t.Fatal("no-op credential change succeeded")
	}
	password72 := strings.Repeat("a", 72)
	if _, err := newService().ChangeCredentials("admin", "old-password", "admin", password72, nil); err != nil {
		t.Fatalf("72-byte password rejected: %v", err)
	}
	password73 := strings.Repeat("界", 24) + "a"
	if len(password73) != 73 {
		t.Fatalf("test password bytes = %d", len(password73))
	}
	if _, err := newService().ChangeCredentials("admin", "old-password", "admin", password73, nil); err == nil {
		t.Fatal("73-byte password accepted")
	}
}

func TestChangeCredentialsKeepsMemoryUnchangedWhenPersistenceFails(t *testing.T) {
	hash, err := HashPassword("old-password")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewForTest([]config.User{{Username: "admin", PasswordHash: hash, Role: "admin"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("disk unavailable")
	_, err = svc.ChangeCredentials("admin", "old-password", "renamed", "", func(string, config.User, int64) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want persistence failure", err)
	}
	if _, err := svc.Login("admin", "old-password"); err != nil {
		t.Fatalf("original login changed after persistence failure: %v", err)
	}
}

func TestLoginAndParseEdDSA(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("expected bcrypt hash, got %s", hash)
	}
	svc, err := NewForTest([]config.User{{
		Username:     "alice",
		PasswordHash: hash,
		Role:         "admin",
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login("alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(res.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected JWT compact form, got %d parts", len(parts))
	}
	c, err := svc.Parse(res.Token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Username() != "alice" || c.Role != "admin" {
		t.Fatalf("%+v", c)
	}
	if c.Issuer != defaultIssuer || len(c.Audience) == 0 || c.Audience[0] != defaultAudience {
		t.Fatalf("iss/aud %+v", c)
	}
	if _, err := svc.Login("alice", "wrong"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRejectWrongAlgAndTamper(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewForTest([]config.User{{
		Username:     "alice",
		PasswordHash: hash,
		Role:         "admin",
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Login("alice", "secret")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(res.Token, ".")
	bad := parts[0] + "." + parts[1] + "." + "AAAA"
	if _, err := svc.Parse(bad); err == nil {
		t.Fatal("expected signature failure")
	}

	other, err := GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	claims := Claims{
		Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    defaultIssuer,
			Subject:   "alice",
			Audience:  jwt.ClaimStrings{defaultAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			ID:        "jti-test",
		},
	}
	forged, err := signJWT(other.Private, claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Parse(forged); err == nil {
		t.Fatal("expected foreign key rejection")
	}
}

func TestExpiredToken(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewForTest([]config.User{{
		Username:     "alice",
		PasswordHash: hash,
		Role:         "admin",
	}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-2 * time.Hour)
	tok, err := signJWT(svc.priv, Claims{
		Role: "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    defaultIssuer,
			Subject:   "alice",
			Audience:  jwt.ClaimStrings{defaultAudience},
			ExpiresAt: jwt.NewNumericDate(past.Add(time.Minute)),
			IssuedAt:  jwt.NewNumericDate(past),
			NotBefore: jwt.NewNumericDate(past),
			ID:        "expired-jti",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Parse(tok); err == nil {
		t.Fatal("expected expired")
	}
}

func TestIssuePreviewTokenScopesOneChannel(t *testing.T) {
	svc, err := NewForTest(nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, expiresAt, err := svc.IssuePreview("news", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if until := time.Until(expiresAt); until < 4*time.Minute || until > 6*time.Minute {
		t.Fatalf("unexpected expiry window: %v", until)
	}
	claims, err := svc.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != "preview" || claims.Username() != "admin-preview" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if !svc.CanAccessChannel(claims, "news") {
		t.Fatal("preview token should allow its channel")
	}
	if svc.CanAccessChannel(claims, "sports") {
		t.Fatal("preview token must not allow another channel")
	}
}

func TestAutoKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	hash, err := HashPassword("x")
	if err != nil {
		t.Fatal(err)
	}
	svc1, err := New(config.Auth{
		Users: []config.User{{Username: "u", PasswordHash: hash, Role: "admin"}},
	}, time.Hour, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc1.Login("u", "x")
	if err != nil {
		t.Fatal(err)
	}
	svc2, err := New(config.Auth{
		Users: []config.User{{Username: "u", PasswordHash: hash, Role: "admin"}},
	}, time.Hour, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Parse(res.Token); err != nil {
		t.Fatalf("persisted key should verify: %v", err)
	}
}

func TestKeyMismatch(t *testing.T) {
	a, err := GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateEd25519()
	if err != nil {
		t.Fatal(err)
	}
	privPEM, err := MarshalPrivateKeyPEM(a.Private)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := MarshalPublicKeyPEM(b.Public)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveKeys(string(privPEM), string(pubPEM), "", "", "")
	if err != ErrKeyMismatch {
		t.Fatalf("want mismatch, got %v", err)
	}
}
