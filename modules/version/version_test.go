package version

import "testing"

func TestUserAgentFollowsBuildVersionUnlessOverridden(t *testing.T) {
	original := Version
	Version = "0.3.0"
	t.Cleanup(func() { Version = original })

	if got := UserAgent(""); got != "Kiln/0.3.0" {
		t.Fatalf("default user agent = %q", got)
	}
	if got := UserAgent("  partner-box/7  "); got != "partner-box/7" {
		t.Fatalf("custom user agent = %q", got)
	}
}
