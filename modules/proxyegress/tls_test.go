package proxyegress

import (
	"crypto/tls"
	"testing"
)

func TestUpstreamTLSConfigOffersRSAKeyExchangeForLegacyEdges(t *testing.T) {
	config := upstreamTLSConfig(true)
	if config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("min version = %x", config.MinVersion)
	}
	offered := map[uint16]bool{}
	for _, suite := range config.CipherSuites {
		offered[suite] = true
	}
	for _, suite := range rsaKeyExchangeSuites {
		if !offered[suite] {
			t.Fatalf("%s missing: an edge that offers nothing else answers with a handshake failure", tls.CipherSuiteName(suite))
		}
	}
	for _, suite := range tls.CipherSuites() {
		if !offered[suite.ID] {
			t.Fatalf("%s missing: forward secrecy must stay available", tls.CipherSuiteName(suite.ID))
		}
	}
	if config.CipherSuites[0] != tls.CipherSuites()[0].ID {
		t.Fatal("the legacy suites must trail the modern ones")
	}
}

func TestUpstreamTLSConfigKeepsGoDefaultsWhenRSAKeyExchangeIsRefused(t *testing.T) {
	config := upstreamTLSConfig(false)
	if config.CipherSuites != nil {
		t.Fatalf("cipher suites = %v, want the go defaults", config.CipherSuites)
	}
}

func TestSetUpstreamRSAKeyExchangeSwitchesTheOfferedSuites(t *testing.T) {
	t.Cleanup(func() { SetUpstreamRSAKeyExchange(true) })
	SetUpstreamRSAKeyExchange(false)
	if UpstreamTLSConfig().CipherSuites != nil {
		t.Fatal("refusing rsa key exchange must reach the transports")
	}
	SetUpstreamRSAKeyExchange(true)
	if UpstreamTLSConfig().CipherSuites == nil {
		t.Fatal("allowing rsa key exchange must reach the transports")
	}
}
