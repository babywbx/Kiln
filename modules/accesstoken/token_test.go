package accesstoken

import "testing"

func TestGenerateTokenShape(t *testing.T) {
	tok, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(tok) {
		t.Fatalf("invalid token shape: %s len=%d", tok[:min(20, len(tok))], len(tok))
	}
	if len(tok) != 2+126 {
		t.Fatalf("len=%d", len(tok))
	}
	h1 := Hash(tok)
	h2 := Hash(tok)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash")
	}
	if Prefix(tok) != tok[:10] {
		t.Fatalf("prefix")
	}
}

func TestScope(t *testing.T) {
	if !AllowsChannel(ScopeAll, "a") {
		t.Fatal("all")
	}
	s := EncodeScope([]string{"a", "b"})
	if !AllowsChannel(s, "b") || AllowsChannel(s, "c") {
		t.Fatal(s)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
