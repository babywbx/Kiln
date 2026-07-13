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

const maskedKey = "********************************"

func KeysUnchanged(submitted string) bool {
	return strings.Contains(submitted, maskedKey)
}
