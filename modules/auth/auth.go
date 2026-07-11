package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = apperr.New(apperr.CodeUnauthorized, 401, "invalid username or password")
	ErrInvalidToken       = apperr.New(apperr.CodeUnauthorized, 401, "invalid token")
	ErrExpiredToken       = apperr.New(apperr.CodeUnauthorized, 401, "token expired")
	ErrForbiddenChannel   = apperr.New(apperr.CodeForbidden, 403, "channel not allowed")
)

type Service struct {
	secret []byte
	ttl    time.Duration
	users  map[string]config.User
	mu     sync.RWMutex
}

type Claims struct {
	Username   string   `json:"u"`
	Role       string   `json:"r"`
	ChannelIDs []string `json:"c,omitempty"`
	Exp        int64    `json:"exp"`
	Iat        int64    `json:"iat"`
	Jti        string   `json:"jti"`
}

type LoginResult struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
}

func New(cfg config.Auth, ttl time.Duration) *Service {
	m := make(map[string]config.User, len(cfg.Users))
	for _, u := range cfg.Users {
		m[u.Username] = u
	}
	return &Service{secret: []byte(cfg.TokenSecret), ttl: ttl, users: m}
}

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Service) Login(username, password string) (LoginResult, error) {
	s.mu.RLock()
	u, ok := s.users[username]
	s.mu.RUnlock()
	if !ok {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	now := time.Now()
	exp := now.Add(s.ttl)
	jti, err := randomJTI()
	if err != nil {
		return LoginResult{}, apperr.Internal(err)
	}
	claims := Claims{
		Username:   u.Username,
		Role:       u.Role,
		ChannelIDs: append([]string(nil), u.ChannelIDs...),
		Exp:        exp.Unix(),
		Iat:        now.Unix(),
		Jti:        jti,
	}
	token, err := s.sign(claims)
	if err != nil {
		return LoginResult{}, apperr.Internal(err)
	}
	return LoginResult{
		Token:     token,
		ExpiresAt: exp,
		Username:  u.Username,
		Role:      u.Role,
	}, nil
}

func (s *Service) Parse(token string) (Claims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Claims{}, ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Claims{}, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrInvalidToken
	}
	now := time.Now().Unix()
	if c.Exp > 0 && now > c.Exp {
		return Claims{}, ErrExpiredToken
	}
	if c.Iat > 0 && now+60 < c.Iat {
		return Claims{}, ErrInvalidToken
	}
	if c.Username == "" || c.Jti == "" {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

func (s *Service) CanAccessChannel(c Claims, channelID string) bool {
	if c.Role == "admin" || len(c.ChannelIDs) == 0 {
		return true
	}
	for _, id := range c.ChannelIDs {
		if id == channelID {
			return true
		}
	}
	return false
}

func (s *Service) sign(c Claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func randomJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func RandomSecret(n int) (string, error) {
	if n < 32 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func BearerToken(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func FormatUserError(err error) string {
	if errors.Is(err, ErrInvalidCredentials) {
		return "invalid username or password"
	}
	if errors.Is(err, ErrExpiredToken) {
		return "token expired"
	}
	if errors.Is(err, ErrInvalidToken) {
		return "invalid token"
	}
	if errors.Is(err, ErrForbiddenChannel) {
		return "channel not allowed"
	}
	return fmt.Sprintf("auth error")
}
