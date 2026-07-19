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

func TestValidRejectsMalformedToken(t *testing.T) {
	valid := VersionPrefix + string(make([]byte, RandomLength))
	validBytes := []byte(valid)
	for i := len(VersionPrefix); i < len(validBytes); i++ {
		validBytes[i] = 'a'
	}
	valid = string(validBytes)

	for _, tc := range []struct {
		name  string
		token string
		want  bool
	}{
		{name: "valid", token: valid, want: true},
		{name: "short", token: valid[:len(valid)-1]},
		{name: "long", token: valid + "a"},
		{name: "wrong version", token: "v2" + valid[2:]},
		{name: "symbol", token: valid[:20] + "-" + valid[21:]},
		{name: "non ascii", token: valid[:20] + "界" + valid[23:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.token); got != tc.want {
				t.Fatalf("Valid() = %t, want %t", got, tc.want)
			}
		})
	}
}

func BenchmarkValid(b *testing.B) {
	token := VersionPrefix + string(make([]byte, RandomLength))
	tokenBytes := []byte(token)
	for i := len(VersionPrefix); i < len(tokenBytes); i++ {
		tokenBytes[i] = 'a'
	}
	token = string(tokenBytes)
	b.ReportAllocs()
	for b.Loop() {
		if !Valid(token) {
			b.Fatal("valid token rejected")
		}
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
