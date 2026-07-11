package version

import "strings"

var (
	Version = "0.2.0"
	Commit  = "dev"
	BuiltAt = "unknown"
)

// UserAgent returns the current Kiln build identity unless a channel overrides it.
func UserAgent(custom string) string {
	if custom = strings.TrimSpace(custom); custom != "" {
		return custom
	}
	return "Kiln/" + Version
}
