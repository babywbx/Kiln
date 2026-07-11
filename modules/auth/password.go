package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 2
	argon2Memory  = 64 * 1024
	argon2Threads = 1
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

var (
	errEmptyPassword = errors.New("empty password")
	errEmptyHash     = errors.New("empty password hash")
	errBadHashFormat = errors.New("unrecognized password hash format")
	errPasswordMatch = errors.New("password mismatch")
)

func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", errEmptyPassword
	}
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(plain), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return encodeArgon2id(salt, sum, argon2Time, argon2Memory, argon2Threads), nil
}

func VerifyPassword(hash, plain string) error {
	if hash == "" {
		return errEmptyHash
	}
	if plain == "" {
		return errEmptyPassword
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		return errBadHashFormat
	}
	return verifyArgon2id(hash, plain)
}

type argon2Params struct {
	time    uint32
	memory  uint32
	threads uint8
	salt    []byte
	key     []byte
}

func encodeArgon2id(salt, key []byte, time, memory uint32, threads uint8) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, time, threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func parseArgon2id(hash string) (argon2Params, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argon2Params{}, errBadHashFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argon2Params{}, errBadHashFormat
	}
	if version != argon2.Version {
		return argon2Params{}, errBadHashFormat
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return argon2Params{}, errBadHashFormat
	}
	if memory == 0 || time == 0 || threads == 0 {
		return argon2Params{}, errBadHashFormat
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return argon2Params{}, errBadHashFormat
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return argon2Params{}, errBadHashFormat
	}
	return argon2Params{time: time, memory: memory, threads: threads, salt: salt, key: key}, nil
}

func verifyArgon2id(hash, plain string) error {
	p, err := parseArgon2id(hash)
	if err != nil {
		return err
	}
	sum := argon2.IDKey([]byte(plain), p.salt, p.time, p.memory, p.threads, uint32(len(p.key)))
	if subtle.ConstantTimeCompare(sum, p.key) != 1 {
		return errPasswordMatch
	}
	return nil
}
