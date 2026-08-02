package version

import "strings"

var (
	Version = "1.0.0"
	Commit  = "dev"
	BuiltAt = "unknown"
)

func UserAgent(custom string) string {
	if custom = strings.TrimSpace(custom); custom != "" {
		return custom
	}
	return "Kiln/" + Version
}
