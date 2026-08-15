//go:build !lite

package httpserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	tlsSourceFile       = "file"
	tlsSourceSelfSigned = "self-signed"
	selfSignedLifetime  = 10 * 365 * 24 * time.Hour
	selfSignedRenewAt   = 30 * 24 * time.Hour
)

type tlsMaterial struct {
	Certificate tls.Certificate
	Source      string
	NotAfter    time.Time
	Hosts       []string
}

func (s *Server) tlsStatus() map[string]any {
	enabled, err := s.tlsEnabled()
	status := map[string]any{
		"tls_enabled":      enabled,
		"tls_listen":       strings.TrimSpace(s.deps.Cfg.Server.TLSListen),
		"tls_split_listen": strings.TrimSpace(s.deps.Cfg.Server.TLSListen) != "",
		"tls_certificate_path": filepath.Join(
			s.deps.Cfg.Server.DataDir, "tls", "kiln.crt",
		),
	}
	if certFile := strings.TrimSpace(s.deps.Cfg.Server.TLSCertFile); certFile != "" {
		status["tls_certificate_path"] = certFile
	}
	if err != nil {
		status["tls_certificate_error"] = err.Error()
		return status
	}
	material, err := s.tlsMaterial()
	if err != nil {
		status["tls_certificate_error"] = err.Error()
		return status
	}
	status["tls_certificate_source"] = material.Source
	status["tls_certificate_expires_at"] = material.NotAfter.UTC().Format(time.RFC3339)
	status["tls_certificate_hosts"] = material.Hosts
	return status
}

func (s *Server) tlsEnabled() (bool, error) {
	if s.deps.Store != nil {
		snapshot, err := s.deps.Store.GetRuntimeSettingsSnapshot()
		if err != nil {
			return false, fmt.Errorf("read tls setting: %w", err)
		}
		if value, ok := snapshot.Values["tls_enabled"]; ok {
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return false, fmt.Errorf("parse tls setting: %w", err)
			}
			return enabled, nil
		}
	}
	return s.deps.Cfg.Server.TLSEnabled, nil
}

func (s *Server) tlsMaterial() (*tlsMaterial, error) {
	cfg := s.deps.Cfg.Server
	certFile, keyFile := strings.TrimSpace(cfg.TLSCertFile), strings.TrimSpace(cfg.TLSKeyFile)
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("tls_cert_file and tls_key_file must be set together")
	}
	if certFile != "" {
		s.tlsMu.Lock()
		defer s.tlsMu.Unlock()
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls keypair: %w", err)
		}
		material, err := describeCertificate(certificate, tlsSourceFile)
		if err != nil {
			return nil, err
		}
		s.activeTLS = material
		return material, nil
	}
	return s.selfSignedMaterial()
}

func (s *Server) selfSignedMaterial() (*tlsMaterial, error) {
	s.tlsMu.Lock()
	defer s.tlsMu.Unlock()

	dir := filepath.Join(s.deps.Cfg.Server.DataDir, "tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create tls dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict tls dir: %w", err)
	}
	certPath, keyPath := filepath.Join(dir, "kiln.crt"), filepath.Join(dir, "kiln.key")
	hosts := s.certificateHosts()

	if certificate, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		material, err := describeCertificate(certificate, tlsSourceSelfSigned)
		if err == nil && !material.Certificate.Leaf.IsCA &&
			material.Certificate.Leaf.KeyUsage&x509.KeyUsageCertSign == 0 &&
			time.Until(material.NotAfter) > selfSignedRenewAt && coversHosts(material.Hosts, hosts) {
			if err := restrictTLSFiles(certPath, keyPath); err != nil {
				return nil, err
			}
			s.activeTLS = material
			return material, nil
		}
	}

	certPEM, keyPEM, err := generateSelfSigned(hosts)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write tls certificate: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write tls key: %w", err)
	}
	if err := restrictTLSFiles(certPath, keyPath); err != nil {
		return nil, err
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	material, err := describeCertificate(certificate, tlsSourceSelfSigned)
	if err != nil {
		return nil, err
	}
	s.activeTLS = material
	return material, nil
}

func (s *Server) activeTLSCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.tlsMu.RLock()
	defer s.tlsMu.RUnlock()
	if s.activeTLS == nil {
		return nil, fmt.Errorf("tls certificate is unavailable")
	}
	certificate := s.activeTLS.Certificate
	return &certificate, nil
}

func (s *Server) certificateHosts() []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	publicBase := s.deps.Cfg.Server.PublicBaseURL
	if s.deps.Catalog != nil {
		publicBase = s.deps.Catalog.PublicBase()
	}
	if base := strings.TrimSpace(publicBase); base != "" {
		if parsed, err := url.Parse(base); err == nil && parsed.Hostname() != "" {
			hosts = append(hosts, parsed.Hostname())
		}
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(s.deps.Cfg.Server.TLSListen)); err == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			hosts = append(hosts, host)
		}
	}
	hosts = append(hosts, s.deps.Cfg.Security.PublicHosts...)
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok || network.IP.IsLoopback() || network.IP.IsLinkLocalUnicast() {
				continue
			}
			hosts = append(hosts, network.IP.String())
		}
	}
	unique := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" && !slices.Contains(unique, host) {
			unique = append(unique, host)
		}
	}
	slices.Sort(unique)
	return unique
}

func restrictTLSFiles(paths ...string) error {
	for _, path := range paths {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("restrict %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func generateSelfSigned(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Kiln", Organization: []string{"Kiln"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(selfSignedLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func describeCertificate(certificate tls.Certificate, source string) (*tlsMaterial, error) {
	if len(certificate.Certificate) == 0 {
		return nil, fmt.Errorf("tls certificate is empty")
	}
	leaf := certificate.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("parse tls certificate: %w", err)
		}
		leaf = parsed
		certificate.Leaf = parsed
	}
	hosts := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	slices.Sort(hosts)
	return &tlsMaterial{
		Certificate: certificate,
		Source:      source,
		NotAfter:    leaf.NotAfter,
		Hosts:       hosts,
	}, nil
}

func coversHosts(have, want []string) bool {
	for _, host := range want {
		if !slices.Contains(have, host) {
			return false
		}
	}
	return true
}
