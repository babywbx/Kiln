package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidCredentials = apperr.New(apperr.CodeUnauthorized, 401, "invalid username or password")
	ErrInvalidToken       = apperr.New(apperr.CodeUnauthorized, 401, "invalid token")
	ErrExpiredToken       = apperr.New(apperr.CodeUnauthorized, 401, "token expired")
	ErrForbiddenChannel   = apperr.New(apperr.CodeForbidden, 403, "channel not allowed")
)

type Service struct {
	priv     ed25519.PrivateKey
	pub      ed25519.PublicKey
	ttl      time.Duration
	issuer   string
	audience string
	users    map[string]config.User
	mu       sync.RWMutex
}

type LoginResult struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
}

type Options struct {
	DataDir string
	Keys    *KeyMaterial
}

func New(cfg config.Auth, ttl time.Duration, opts Options) (*Service, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	var km KeyMaterial
	var err error
	if opts.Keys != nil {
		km = *opts.Keys
	} else {
		km, err = ResolveKeys(
			cfg.TokenPrivateKey,
			cfg.TokenPublicKey,
			cfg.TokenPrivateKeyFile,
			cfg.TokenPublicKeyFile,
			opts.DataDir,
		)
		if err != nil {
			return nil, err
		}
	}
	if len(km.Private) != ed25519.PrivateKeySize || len(km.Public) != ed25519.PublicKeySize {
		return nil, ErrInvalidSigningKey
	}
	issuer := strings.TrimSpace(cfg.TokenIssuer)
	if issuer == "" {
		issuer = defaultIssuer
	}
	audience := strings.TrimSpace(cfg.TokenAudience)
	if audience == "" {
		audience = defaultAudience
	}
	m := make(map[string]config.User, len(cfg.Users))
	for _, u := range cfg.Users {
		m[u.Username] = u
	}
	return &Service{
		priv:     km.Private,
		pub:      km.Public,
		ttl:      ttl,
		issuer:   issuer,
		audience: audience,
		users:    m,
	}, nil
}

func NewForTest(users []config.User, ttl time.Duration) (*Service, error) {
	km, err := GenerateEd25519()
	if err != nil {
		return nil, err
	}
	return New(config.Auth{
		TokenIssuer:   defaultIssuer,
		TokenAudience: defaultAudience,
		Users:         users,
	}, ttl, Options{Keys: &km})
}

func (s *Service) Login(username, password string) (LoginResult, error) {
	s.mu.RLock()
	u, ok := s.users[username]
	s.mu.RUnlock()
	if !ok {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := VerifyPassword(u.PasswordHash, password); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	now := time.Now().UTC()
	exp := now.Add(s.ttl)
	jti, err := randomJTI()
	if err != nil {
		return LoginResult{}, apperr.Internal(err)
	}
	claims := Claims{
		Role:       u.Role,
		ChannelIDs: append([]string(nil), u.ChannelIDs...),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   u.Username,
			Audience:  jwt.ClaimStrings{s.audience},
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        jti,
		},
	}
	token, err := signJWT(s.priv, claims)
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

// IssuePreview creates a short-lived token scoped to one channel.
func (s *Service) IssuePreview(channelID string, ttl time.Duration) (string, time.Time, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", time.Time{}, apperr.New(apperr.CodeInvalid, 400, "channel id required")
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	jti, err := randomJTI()
	if err != nil {
		return "", time.Time{}, apperr.Internal(err)
	}
	claims := Claims{
		Role:       "preview",
		ChannelIDs: []string{channelID},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   "admin-preview",
			Audience:  jwt.ClaimStrings{s.audience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        jti,
		},
	}
	token, err := signJWT(s.priv, claims)
	if err != nil {
		return "", time.Time{}, apperr.Internal(err)
	}
	return token, expiresAt, nil
}

func (s *Service) Parse(token string) (Claims, error) {
	return parseJWT(s.pub, strings.TrimSpace(token), s.issuer, s.audience)
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

func randomJTI() (string, error) {
	b := make([]byte, 16)
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
