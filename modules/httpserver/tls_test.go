//go:build !lite

package httpserver

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func serverWithDataDir(t *testing.T, publicBase string) *Server {
	t.Helper()
	cfg := config.File{}
	cfg.Server.DataDir = t.TempDir()
	cfg.Server.PublicBaseURL = publicBase
	return &Server{deps: Deps{Cfg: cfg}}
}

func TestSelfSignedCertificateCoversLoopbackAndPublicBase(t *testing.T) {
	server := serverWithDataDir(t, "https://10.10.5.60:8080")
	server.deps.Cfg.Server.TLSListen = "console.kiln.test:8443"
	material, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "10.10.5.60", "console.kiln.test"} {
		if !slices.Contains(material.Hosts, host) {
			t.Fatalf("certificate hosts %v missing %s", material.Hosts, host)
		}
	}
	leaf := material.Certificate.Leaf
	if err := leaf.VerifyHostname("10.10.5.60"); err != nil {
		t.Fatalf("verify hostname: %v", err)
	}
	if leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatalf("self-signed certificate is CA=%t with key usage %v, want a DigitalSignature-only leaf", leaf.IsCA, leaf.KeyUsage)
	}
}

func TestSelfSignedCertificateIsReusedAcrossRestarts(t *testing.T) {
	server := serverWithDataDir(t, "https://kiln.example")
	first, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(server.deps.Cfg.Server.DataDir, "tls", "kiln.crt"),
		filepath.Join(server.deps.Cfg.Server.DataDir, "tls", "kiln.key"),
	}
	if runtime.GOOS != "windows" {
		for _, path := range paths {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	second, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Certificate.Certificate[0], second.Certificate.Certificate[0]) {
		t.Fatal("restarting must not invalidate a certificate the operator already trusted")
	}
	if runtime.GOOS == "windows" {
		return
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, want 0600", filepath.Base(path), info.Mode().Perm())
		}
	}
}

func TestSelfSignedCertificateGenerationIsSerialized(t *testing.T) {
	server := serverWithDataDir(t, "https://kiln.example")
	type result struct {
		material *tlsMaterial
		err      error
	}
	const workers = 32
	start := make(chan struct{})
	results := make(chan result, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			material, err := server.selfSignedMaterial()
			results <- result{material: material, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var first []byte
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		current := result.material.Certificate.Certificate[0]
		if first == nil {
			first = bytes.Clone(current)
			continue
		}
		if !bytes.Equal(first, current) {
			t.Fatal("concurrent generation returned different certificates")
		}
	}
	certPath := filepath.Join(server.deps.Cfg.Server.DataDir, "tls", "kiln.crt")
	keyPath := filepath.Join(server.deps.Cfg.Server.DataDir, "tls", "kiln.key")
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load generated keypair: %v", err)
	}
	if !bytes.Equal(first, pair.Certificate[0]) {
		t.Fatal("stored certificate differs from the active certificate")
	}
}

func TestSelfSignedCertificateReplacesLegacyCA(t *testing.T) {
	server := serverWithDataDir(t, "https://kiln.example")
	initial, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	legacy := *initial.Certificate.Leaf
	legacy.IsCA = true
	legacy.KeyUsage |= x509.KeyUsageCertSign
	legacyDER, err := x509.CreateCertificate(
		rand.Reader, &legacy, &legacy, initial.Certificate.Leaf.PublicKey, initial.Certificate.PrivateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(server.deps.Cfg.Server.DataDir, "tls", "kiln.crt")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: legacyDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(legacyDER, migrated.Certificate.Certificate[0]) {
		t.Fatal("legacy CA certificate was reused")
	}
	leaf := migrated.Certificate.Leaf
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Fatalf("migrated certificate is CA=%t with key usage %v", leaf.IsCA, leaf.KeyUsage)
	}
}

func TestSelfSignedCertificateIsReissuedWhenHostsChange(t *testing.T) {
	server := serverWithDataDir(t, "https://first.example")
	first, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	server.deps.Cfg.Server.PublicBaseURL = "https://second.example"
	second, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Certificate.Certificate[0], second.Certificate.Certificate[0]) {
		t.Fatal("a renamed deployment must get a certificate that covers the new host")
	}
	if !slices.Contains(second.Hosts, "second.example") {
		t.Fatalf("certificate hosts %v missing second.example", second.Hosts)
	}
}

func TestSelfSignedCertificateServesTLS(t *testing.T) {
	server := serverWithDataDir(t, "https://localhost")
	material, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: server.activeTLSCertificate,
		MinVersion:     tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	served := dialTLSCertificate(t, listener, material)
	if !bytes.Equal(served, material.Certificate.Certificate[0]) {
		t.Fatal("listener did not serve the active certificate")
	}
}

func TestSelfSignedCertificateRotationUpdatesTLSListener(t *testing.T) {
	server := serverWithDataDir(t, "https://first.example")
	first, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: server.activeTLSCertificate,
		MinVersion:     tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if served := dialTLSCertificate(t, listener, first); !bytes.Equal(served, first.Certificate.Certificate[0]) {
		t.Fatal("listener did not serve the initial certificate")
	}

	server.deps.Cfg.Server.PublicBaseURL = "https://second.example"
	status := server.tlsStatus()
	if statusErr, ok := status["tls_certificate_error"]; ok {
		t.Fatalf("rotate certificate from settings status: %v", statusErr)
	}
	server.tlsMu.RLock()
	rotated := server.activeTLS
	server.tlsMu.RUnlock()
	if rotated == nil {
		t.Fatal("rotation did not install an active certificate")
	}
	if bytes.Equal(first.Certificate.Certificate[0], rotated.Certificate.Certificate[0]) {
		t.Fatal("host change did not rotate the certificate")
	}
	if err := rotated.Certificate.Leaf.VerifyHostname("second.example"); err != nil {
		t.Fatalf("rotated certificate does not cover the new host: %v", err)
	}
	if served := dialTLSCertificate(t, listener, rotated); !bytes.Equal(served, rotated.Certificate.Certificate[0]) {
		t.Fatal("listener kept serving the certificate replaced by the settings request")
	}
}

func dialTLSCertificate(t *testing.T, listener net.Listener, material *tlsMaterial) []byte {
	t.Helper()
	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			accepted <- acceptErr
			return
		}
		handshakeErr := connection.(*tls.Conn).Handshake()
		_ = connection.Close()
		accepted <- handshakeErr
	}()

	roots := x509.NewCertPool()
	roots.AddCert(material.Certificate.Leaf)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port), &tls.Config{
		RootCAs:    roots,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
	})
	acceptErr := <-accepted
	if err != nil {
		t.Fatalf("tls dial against the generated certificate: %v", err)
	}
	if acceptErr != nil {
		t.Fatalf("serve generated certificate: %v", acceptErr)
	}
	served := bytes.Clone(client.ConnectionState().PeerCertificates[0].Raw)
	_ = client.Close()
	return served
}

func TestTLSStaysOffWhenNothingEnablesIt(t *testing.T) {
	server := serverWithDataDir(t, "https://localhost")
	enabled, err := server.tlsEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("tls must stay off until the operator turns it on")
	}
	server.deps.Cfg.Server.TLSEnabled = true
	enabled, err = server.tlsEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("config must enable tls when no stored override exists")
	}
}

func TestSelfSignedCertificateUsesRuntimePublicBase(t *testing.T) {
	server := serverWithDataDir(t, "https://config.example")
	db, err := store.Open(server.deps.Cfg.Server.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting("public_base_url", "https://runtime.example"); err != nil {
		t.Fatal(err)
	}
	server.deps.Store = db
	server.deps.Catalog = catalog.New(server.deps.Cfg, db)
	material, err := server.selfSignedMaterial()
	if err != nil {
		t.Fatal(err)
	}
	if err := material.Certificate.Leaf.VerifyHostname("runtime.example"); err != nil {
		t.Fatalf("runtime public base missing from certificate: %v", err)
	}
}

func TestTLSRejectsAHalfConfiguredKeypair(t *testing.T) {
	server := serverWithDataDir(t, "https://localhost")
	server.deps.Cfg.Server.TLSCertFile = "/tmp/kiln-missing.crt"
	if _, err := server.tlsMaterial(); err == nil {
		t.Fatal("a certificate without its key must fail loudly instead of silently self-signing")
	}
}
