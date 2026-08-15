package security

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
)

func TestPublicProbeURLRejectsSpecialDestinations(t *testing.T) {
	for _, raw := range []string{
		"http://0.1.2.3/test",
		"http://10.0.0.1/test",
		"http://100.64.0.1/test",
		"http://127.0.0.1/test",
		"http://169.254.169.254/latest",
		"http://192.0.0.9/test",
		"http://192.0.2.1/test",
		"http://192.31.196.1/test",
		"http://192.52.193.1/test",
		"http://192.88.99.2/test",
		"http://192.175.48.1/test",
		"http://198.18.0.1/test",
		"http://198.51.100.1/test",
		"http://203.0.113.1/test",
		"http://224.0.0.1/test",
		"http://240.0.0.1/test",
		"http://[64:ff9b::1]/test",
		"http://[64:ff9b:1::1]/test",
		"http://[100::1]/test",
		"http://[100:0:0:1::1]/test",
		"http://[2001:1::1]/test",
		"http://[2001:db8::1]/test",
		"http://[2002::1]/test",
		"http://[2620:4f:8000::1]/test",
		"http://[3fff::1]/test",
		"http://[5f00::1]/test",
		"http://[fe80::1]/test",
		"http://[ff02::1]/test",
		"http://metadata.google.internal/test",
	} {
		if err := PublicProbeURL(context.Background(), raw, nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		if err := MediaHostOK(raw, nil); err == nil {
			t.Fatalf("media host accepted %s", raw)
		}
	}
	if err := PublicProbeURL(context.Background(), "http://127.0.0.1/test", map[string]struct{}{"127.0.0.1": {}}); err != nil {
		t.Fatalf("allowlisted target rejected: %v", err)
	}
}

func TestPinPublicProbeURLPreservesDNSError(t *testing.T) {
	original := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.DNSError{Err: "temporary failure", IsTemporary: true}
		},
	}
	defer func() { net.DefaultResolver = original }()

	_, err := PinPublicProbeURL(context.Background(), "https://origin.example/manifest.mpd", nil)
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || !dnsErr.IsTemporary {
		t.Fatalf("DNS error = %v, want preserved temporary DNSError", err)
	}
}

func TestExplicitAllowlistCannotPermitFixedDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://100.64.0.1/private",
		"http://192.0.0.9/private",
		"http://198.18.0.1/private",
		"http://224.0.0.1/private",
		"http://0.0.0.0/private",
		"http://[64:ff9b::1]/private",
		"http://[2001:db8::1]/private",
		"http://[fe80::1]/private",
		"http://metadata.google.internal/computeMetadata/v1",
	} {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		allowed := map[string]struct{}{u.Hostname(): {}}
		if err := PublicProbeURL(context.Background(), rawURL, allowed); err == nil {
			t.Fatalf("explicit allowlist accepted fixed destination %s", rawURL)
		}
		if err := MediaHostOK(rawURL, allowed); err == nil {
			t.Fatalf("explicit allowlist accepted fixed media host %s", rawURL)
		}
	}
}

func TestExplicitAllowlistPermitsLoopbackAndPrivateDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1/test",
		"http://[::1]/test",
		"http://10.0.0.1/test",
		"http://[fd00::1]/test",
	} {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if err := PublicProbeURL(context.Background(), rawURL, map[string]struct{}{u.Hostname(): {}}); err != nil {
			t.Fatalf("explicit private target %s rejected: %v", rawURL, err)
		}
		if err := MediaHostOK(rawURL, map[string]struct{}{u.Hostname(): {}}); err != nil {
			t.Fatalf("explicit private media host %s rejected: %v", rawURL, err)
		}
	}
}

func TestMediaHostNameDoesNotRejectOrdinaryMetadataSubdomains(t *testing.T) {
	if err := MediaHostOK("https://metadata.example.com/stream.mpd", nil); err != nil {
		t.Fatalf("ordinary public hostname rejected: %v", err)
	}
}
