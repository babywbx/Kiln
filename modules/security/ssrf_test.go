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
		"http://127.0.0.1/test", "http://10.0.0.1/test", "http://169.254.169.254/latest",
		"http://100.64.0.1/test", "http://metadata.google.internal/test",
	} {
		if err := PublicProbeURL(context.Background(), raw, nil); err == nil {
			t.Fatalf("accepted %s", raw)
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
		"http://224.0.0.1/private",
		"http://0.0.0.0/private",
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
	}
}

func TestExplicitAllowlistPermitsLoopbackAndPrivateDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1/test",
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
	}
}

func TestMediaHostNameDoesNotRejectOrdinaryMetadataSubdomains(t *testing.T) {
	if err := MediaHostOK("https://metadata.example.com/stream.mpd", nil); err != nil {
		t.Fatalf("ordinary public hostname rejected: %v", err)
	}
}
