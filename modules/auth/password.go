package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

var (
	errEmptyPassword = errors.New("empty password")
	errEmptyHash     = errors.New("empty password hash")
)

func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", errEmptyPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, plain string) error {
	if hash == "" {
		return errEmptyHash
	}
	if plain == "" {
		return errEmptyPassword
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}
