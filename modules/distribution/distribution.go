package distribution

import (
	"os"
	"strings"
)

const RuntimeVariantEnv = "KILN_RUNTIME_VARIANT"

type RuntimeVariant string

const (
	VariantStandalone RuntimeVariant = "standalone"
	VariantCore       RuntimeVariant = "core"
	VariantFull       RuntimeVariant = "full"
)

func DetectRuntimeVariant() (RuntimeVariant, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(RuntimeVariantEnv))) {
	case "":
		return VariantStandalone, true
	case string(VariantCore):
		return VariantCore, true
	case string(VariantFull):
		return VariantFull, true
	default:
		return VariantStandalone, false
	}
}
