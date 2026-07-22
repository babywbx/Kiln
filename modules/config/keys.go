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

func ParseKeys(text string) ([]KeyPair, error) {
	var out []KeyPair
	seen := make(map[string]string)
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
		if err := validHex(kid, "kid", lineNo, true); err != nil {
			return nil, err
		}
		if err := validHex(key, "key", lineNo, false); err != nil {
			return nil, err
		}
		normalizedKID := normalizeHex(kid)
		normalizedKey := normalizeHex(key)
		if previous, ok := seen[normalizedKID]; ok {
			if previous != normalizedKey {
				return nil, fmt.Errorf("line %d: KID has a conflicting key", lineNo)
			}
			continue
		}
		seen[normalizedKID] = normalizedKey
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

func normalizeHex(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", ""))
}

func validHex(s, what string, lineNo int, allowDashes bool) error {
	if allowDashes {
		s = strings.ReplaceAll(s, "-", "")
	} else if strings.Contains(s, "-") {
		return fmt.Errorf("line %d: %s must not contain dashes", lineNo, what)
	}
	if len(s) != 32 {
		return fmt.Errorf("line %d: %s must be 32 hex characters, got %d", lineNo, what, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("line %d: %s is not hexadecimal", lineNo, what)
	}
	return nil
}
