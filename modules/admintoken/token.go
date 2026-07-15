package admintoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/store"
)

type Scope string

const (
	ScopeRead    Scope = "read"
	ScopeWrite   Scope = "write"
	ScopeDelete  Scope = "delete"
	ScopeRefresh Scope = "refresh"
)

var AllScopes = []Scope{ScopeRead, ScopeWrite, ScopeDelete, ScopeRefresh}

const (
	Prefix         = "kiln_v1_"
	randomLength   = 48
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var tokenPattern = regexp.MustCompile(`^kiln_v1_[0-9A-Za-z]{48}$`)

func Generate() (string, error) {
	var b strings.Builder
	b.Grow(len(Prefix) + randomLength)
	b.WriteString(Prefix)
	max := big.NewInt(int64(len(base62Alphabet)))
	for range randomLength {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(base62Alphabet[n.Int64()])
	}
	return b.String(), nil
}

func Valid(token string) bool {
	return tokenPattern.MatchString(token)
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func DisplayPrefix(token string) string {
	const visible = len(Prefix) + 8
	if len(token) <= visible {
		return token
	}
	return token[:visible]
}

func NormalizeScopes(values []string) ([]string, error) {
	seen := make(map[Scope]struct{}, len(values))
	for _, value := range values {
		scope := Scope(strings.ToLower(strings.TrimSpace(value)))
		if !slices.Contains(AllScopes, scope) {
			return nil, fmt.Errorf("unknown API token permission %q", value)
		}
		seen[scope] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("at least one API token permission is required")
	}
	out := make([]string, 0, len(seen))
	for _, scope := range AllScopes {
		if _, ok := seen[scope]; ok {
			out = append(out, string(scope))
		}
	}
	return out, nil
}

func EncodeScopes(values []string) (string, error) {
	normalized, err := NormalizeScopes(values)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DecodeScopes(raw string) []string {
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil
	}
	normalized, err := NormalizeScopes(values)
	if err != nil {
		return nil
	}
	return normalized
}

func Allows(scopes []string, required Scope) bool {
	return slices.Contains(scopes, string(required))
}

func NewRow(name, note, createdBy string, scopes []string, expiresAt int64) (string, store.AdminAPITokenRow, error) {
	plain, err := Generate()
	if err != nil {
		return "", store.AdminAPITokenRow{}, err
	}
	id, err := randomID()
	if err != nil {
		return "", store.AdminAPITokenRow{}, err
	}
	scopeJSON, err := EncodeScopes(scopes)
	if err != nil {
		return "", store.AdminAPITokenRow{}, err
	}
	now := time.Now().Unix()
	row := store.AdminAPITokenRow{
		ID: id, Name: strings.TrimSpace(name), Note: strings.TrimSpace(note),
		TokenHash: Hash(plain), Prefix: DisplayPrefix(plain), ScopeJSON: scopeJSON,
		Enabled: true, CreatedBy: strings.TrimSpace(createdBy), CreatedAt: now,
		ExpiresAt: expiresAt, Revision: 1, UpdatedAt: now,
	}
	if row.Name == "" {
		row.Name = "API token"
	}
	return plain, row, nil
}

func randomID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
