package security

import (
	"context"
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
