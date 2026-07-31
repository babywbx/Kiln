package proxyegress

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestPinnedTransportUsesSeparateTLSNamesForHTTPSProxyAndTarget(t *testing.T) {
	proxyCertificate, proxyLeaf := testCertificate(t, "127.0.0.1")
	targetCertificate, targetLeaf := testCertificate(t, "localhost")

	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy method = %s, want CONNECT", r.Method)
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		connection, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack proxy connection: %v", err)
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buffered.Flush(); err != nil {
			t.Errorf("flush CONNECT response: %v", err)
			return
		}

		target := tls.Server(connection, &tls.Config{
			Certificates: []tls.Certificate{targetCertificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := target.HandshakeContext(context.Background()); err != nil {
			t.Errorf("target TLS handshake: %v", err)
			return
		}
		defer func() { _ = target.Close() }()
		request, err := http.ReadRequest(bufio.NewReader(target))
		if err != nil {
			t.Errorf("read tunneled request: %v", err)
			return
		}
		_ = request.Body.Close()
		_, _ = io.WriteString(target, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}))
	proxy.TLS = &tls.Config{
		Certificates: []tls.Certificate{proxyCertificate},
		MinVersion:   tls.VersionTLS12,
	}
	proxy.StartTLS()
	defer proxy.Close()

	roots := x509.NewCertPool()
	roots.AddCert(proxyLeaf)
	roots.AddCert(targetLeaf)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: NewPinnedTransport(&http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		}, nil, "", map[string]struct{}{"localhost": {}}),
	}
	response, err := client.Get("https://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func testCertificate(t *testing.T, host string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}
