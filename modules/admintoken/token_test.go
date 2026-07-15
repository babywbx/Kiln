package admintoken

import "testing"

func TestTokenShapeAndScopes(t *testing.T) {
	token, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(token) || token[:len(Prefix)] != "kiln_v1_" || len(Hash(token)) != 64 ||
		DisplayPrefix(token) != token[:len(Prefix)+8] {
		t.Fatalf("unexpected token shape")
	}
	scopes, err := NormalizeScopes([]string{"refresh", "read", "read"})
	if err != nil || len(scopes) != 2 || scopes[0] != "read" || scopes[1] != "refresh" {
		t.Fatalf("scopes = %v, err = %v", scopes, err)
	}
	if _, err := NormalizeScopes([]string{"owner"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
}
