//go:build !lite

package httpserver

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	if !leaf.IsCA {
		t.Fatal("self-signed certificate must be usable as its own trust anchor")
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
		Certificates: []tls.Certificate{material.Certificate},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.(*tls.Conn).Handshake()
			_ = connection.Close()
		}
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
	if err != nil {
		t.Fatalf("tls dial against the generated certificate: %v", err)
	}
	_ = client.Close()
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
