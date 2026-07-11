package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrMissingSigningKey = errors.New("missing Ed25519 signing key")
	ErrInvalidSigningKey = errors.New("invalid Ed25519 signing key")
	ErrKeyMismatch       = errors.New("public key does not match private key")
)

type KeyMaterial struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

func ResolveKeys(privatePEM, publicPEM, privateFile, publicFile, dataDir string) (KeyMaterial, error) {
	privPEM := strings.TrimSpace(privatePEM)
	if privPEM == "" && strings.TrimSpace(privateFile) != "" {
		b, err := os.ReadFile(privateFile)
		if err != nil {
			return KeyMaterial{}, fmt.Errorf("read private key file: %w", err)
		}
		privPEM = string(b)
	}
	if privPEM == "" && dataDir != "" {
		path := filepath.Join(dataDir, "auth", "ed25519.pem")
		b, err := os.ReadFile(path)
		if err == nil {
			privPEM = string(b)
		} else if os.IsNotExist(err) {
			km, genErr := GenerateEd25519()
			if genErr != nil {
				return KeyMaterial{}, genErr
			}
			if err := WritePrivateKeyPEM(path, km.Private); err != nil {
				return KeyMaterial{}, err
			}
			pubPath := filepath.Join(dataDir, "auth", "ed25519.pub.pem")
			if err := WritePublicKeyPEM(pubPath, km.Public); err != nil {
				return KeyMaterial{}, err
			}
			return km, nil
		} else {
			return KeyMaterial{}, fmt.Errorf("read auto private key: %w", err)
		}
	}
	if privPEM == "" {
		return KeyMaterial{}, ErrMissingSigningKey
	}
	priv, err := ParsePrivateKeyPEM(privPEM)
	if err != nil {
		return KeyMaterial{}, err
	}
	pub := priv.Public().(ed25519.PublicKey)

	pubPEM := strings.TrimSpace(publicPEM)
	if pubPEM == "" && strings.TrimSpace(publicFile) != "" {
		b, err := os.ReadFile(publicFile)
		if err != nil {
			return KeyMaterial{}, fmt.Errorf("read public key file: %w", err)
		}
		pubPEM = string(b)
	}
	if pubPEM != "" {
		want, err := ParsePublicKeyPEM(pubPEM)
		if err != nil {
			return KeyMaterial{}, err
		}
		if !pub.Equal(want) {
			return KeyMaterial{}, ErrKeyMismatch
		}
		pub = want
	}
	return KeyMaterial{Private: priv, Public: pub}, nil
}

func GenerateEd25519() (KeyMaterial, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyMaterial{}, err
	}
	return KeyMaterial{Private: priv, Public: pub}, nil
}

func ParsePrivateKeyPEM(pemText string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, ErrInvalidSigningKey
	}
	switch block.Type {
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, ErrInvalidSigningKey
		}
		priv, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, ErrInvalidSigningKey
		}
		if len(priv) != ed25519.PrivateKeySize {
			return nil, ErrInvalidSigningKey
		}
		return priv, nil
	case "ED25519 PRIVATE KEY":
		if len(block.Bytes) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(block.Bytes), nil
		}
		if len(block.Bytes) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(block.Bytes), nil
		}
		return nil, ErrInvalidSigningKey
	default:
		return nil, ErrInvalidSigningKey
	}
}

func ParsePublicKeyPEM(pemText string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, ErrInvalidSigningKey
	}
	switch block.Type {
	case "PUBLIC KEY":
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, ErrInvalidSigningKey
		}
		pub, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, ErrInvalidSigningKey
		}
		if len(pub) != ed25519.PublicKeySize {
			return nil, ErrInvalidSigningKey
		}
		return pub, nil
	case "ED25519 PUBLIC KEY":
		if len(block.Bytes) != ed25519.PublicKeySize {
			return nil, ErrInvalidSigningKey
		}
		return ed25519.PublicKey(block.Bytes), nil
	default:
		return nil, ErrInvalidSigningKey
	}
}

func MarshalPrivateKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidSigningKey
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func MarshalPublicKeyPEM(pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrInvalidSigningKey
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func WritePrivateKeyPEM(path string, priv ed25519.PrivateKey) error {
	b, err := MarshalPrivateKeyPEM(priv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func WritePublicKeyPEM(path string, pub ed25519.PublicKey) error {
	b, err := MarshalPublicKeyPEM(pub)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func PublicFromPrivate(priv ed25519.PrivateKey) (ed25519.PublicKey, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, ErrInvalidSigningKey
	}
	return priv.Public().(ed25519.PublicKey), nil
}
