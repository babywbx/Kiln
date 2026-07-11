package config

import (
	"bufio"
	"fmt"
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
	var out []KeyPair
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%s:%d: expected kid:key", path, lineNo)
		}
		kid := strings.TrimSpace(parts[0])
		key := strings.TrimSpace(parts[1])
		if kid == "" || key == "" {
			return nil, fmt.Errorf("%s:%d: empty kid or key", path, lineNo)
		}
		out = append(out, KeyPair{KID: kid, Key: key})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no keys found", path)
	}
	return out, nil
}
