package auth

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultIssuer   = "kiln"
	defaultAudience = "kiln"
	jwtLeeway       = 30 * time.Second
	maxTokenBytes   = 8 << 10
)

type Claims struct {
	Role       string   `json:"role"`
	ChannelIDs []string `json:"channels,omitempty"`
	jwt.RegisteredClaims
}

func (c Claims) Username() string {
	return c.Subject
}

func signJWT(priv ed25519.PrivateKey, c Claims) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", ErrInvalidSigningKey
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tok.Header["typ"] = "JWT"
	return tok.SignedString(priv)
}

func parseJWT(pub ed25519.PublicKey, token string, issuer, audience string) (Claims, error) {
	if token == "" {
		return Claims{}, ErrInvalidToken
	}
	if len(token) > maxTokenBytes {
		return Claims{}, ErrInvalidToken
	}
	if len(pub) != ed25519.PublicKeySize {
		return Claims{}, ErrInvalidSigningKey
	}
	if issuer == "" {
		issuer = defaultIssuer
	}
	if audience == "" {
		audience = defaultAudience
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(jwtLeeway),
	)

	var claims Claims
	parsed, err := parser.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodEdDSA {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pub, nil
	})
	if err != nil {
		return Claims{}, mapJWTError(err)
	}
	if !parsed.Valid {
		return Claims{}, ErrInvalidToken
	}
	if claims.Subject == "" || claims.ID == "" {
		return Claims{}, ErrInvalidToken
	}
	if claims.Role == "" {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func mapJWTError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, jwt.ErrTokenExpired) {
		return ErrExpiredToken
	}
	if errors.Is(err, jwt.ErrTokenNotValidYet) ||
		errors.Is(err, jwt.ErrTokenUsedBeforeIssued) ||
		errors.Is(err, jwt.ErrTokenInvalidIssuer) ||
		errors.Is(err, jwt.ErrTokenInvalidAudience) ||
		errors.Is(err, jwt.ErrTokenSignatureInvalid) ||
		errors.Is(err, jwt.ErrTokenMalformed) ||
		errors.Is(err, jwt.ErrTokenUnverifiable) ||
		errors.Is(err, jwt.ErrTokenInvalidClaims) {
		return ErrInvalidToken
	}
	return ErrInvalidToken
}
