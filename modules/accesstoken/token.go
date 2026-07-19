package accesstoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/store"
)

const (
	VersionPrefix  = "v1"
	RandomLength   = 126
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

func Generate() (string, error) {
	var b strings.Builder
	b.Grow(len(VersionPrefix) + RandomLength)
	b.WriteString(VersionPrefix)
	max := big.NewInt(int64(len(base62Alphabet)))
	for i := 0; i < RandomLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(base62Alphabet[n.Int64()])
	}
	return b.String(), nil
}

func Valid(token string) bool {
	if len(token) != len(VersionPrefix)+RandomLength || !strings.HasPrefix(token, VersionPrefix) {
		return false
	}
	for i := len(VersionPrefix); i < len(token); i++ {
		c := token[i]
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			continue
		}
		return false
	}
	return true
}

func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func Prefix(token string) string {
	if len(token) < 10 {
		return token
	}
	return token[:10]
}

const ScopeAll = "all"

func EncodeScope(channelIDs []string) string {
	if len(channelIDs) == 0 {
		return ScopeAll
	}
	b, err := json.Marshal(channelIDs)
	if err != nil {
		return ScopeAll
	}
	return string(b)
}

func DecodeScope(s string) (all bool, ids []string, err error) {
	s = strings.TrimSpace(s)
	if s == "" || s == ScopeAll {
		return true, nil, nil
	}
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return false, nil, fmt.Errorf("invalid scope")
	}
	return false, ids, nil
}

func AllowsChannel(scopeJSON, channelID string) bool {
	all, ids, err := DecodeScope(scopeJSON)
	if err != nil {
		return false
	}
	if all {
		return true
	}
	for _, id := range ids {
		if id == channelID {
			return true
		}
	}
	return false
}

func NewRow(name, note string, channelIDs []string) (plain string, row store.AccessTokenRow, err error) {
	plain, err = Generate()
	if err != nil {
		return "", store.AccessTokenRow{}, err
	}
	id, err := randomID()
	if err != nil {
		return "", store.AccessTokenRow{}, err
	}
	row = store.AccessTokenRow{
		ID:        id,
		Name:      strings.TrimSpace(name),
		TokenHash: Hash(plain),
		Prefix:    Prefix(plain),
		ScopeJSON: EncodeScope(channelIDs),
		Enabled:   true,
		Note:      note,
		CreatedAt: time.Now().Unix(),
	}
	if row.Name == "" {
		row.Name = "link"
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
