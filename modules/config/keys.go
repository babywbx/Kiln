package config

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type KeyPair struct {
	KID string
	Key string
}

func LoadKeysFile(path string) ([]KeyPair, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	keys, err := ParseKeys(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return keys, nil
}

// ParseKeys reads kid:key lines. It is the one parser for both the keys file and
// the keys typed into the admin form, so the two can never drift.
func ParseKeys(text string) ([]KeyPair, error) {
	var out []KeyPair
	sc := bufio.NewScanner(strings.NewReader(text))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kid, key, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: expected kid:key", lineNo)
		}
		kid = strings.TrimSpace(kid)
		key = strings.TrimSpace(key)
		if kid == "" || key == "" {
			return nil, fmt.Errorf("line %d: empty kid or key", lineNo)
		}
		if err := validHex(kid, "kid", lineNo); err != nil {
			return nil, err
		}
		if err := validHex(key, "key", lineNo); err != nil {
			return nil, err
		}
		out = append(out, KeyPair{KID: kid, Key: key})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no keys found")
	}
	return out, nil
}

// validHex catches the mistakes people actually make when pasting a key: a
// dashed KID copied out of an MPD, or a truncated value.
func validHex(s, what string, lineNo int) error {
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return fmt.Errorf("line %d: %s must be 32 hex characters, got %d", lineNo, what, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("line %d: %s is not hexadecimal", lineNo, what)
	}
	return nil
}

// MaskKeys renders keys for display. The KID is public — it is in the manifest —
// but the key itself never leaves the server, so an admin can see which keys are
// configured without the page ever holding one.
func MaskKeys(text string) string {
	keys, err := ParseKeys(text)
	if err != nil {
		return ""
	}
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k.KID+":"+maskedKey)
	}
	return strings.Join(lines, "\n")
}

// maskedKey is what the form shows in place of a key. A submitted value that
// still contains it means the admin did not touch the field.
const maskedKey = "********************************"

// KeysUnchanged reports whether a submitted keys blob is just the masked form
// handed back, in which case the stored keys must be kept.
func KeysUnchanged(submitted string) bool {
	return strings.Contains(submitted, maskedKey)
}
