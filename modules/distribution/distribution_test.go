package distribution

import "testing"

func TestDetectRuntimeVariantUsesValidatedEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  RuntimeVariant
		valid bool
	}{
		{name: "standalone", want: VariantStandalone, valid: true},
		{name: "core", value: "core", want: VariantCore, valid: true},
		{name: "full", value: "full", want: VariantFull, valid: true},
		{name: "trimmed case", value: " Full ", want: VariantFull, valid: true},
		{name: "unknown", value: "custom", want: VariantStandalone, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(RuntimeVariantEnv, test.value)

			got, valid := DetectRuntimeVariant()

			if got != test.want || valid != test.valid {
				t.Fatalf("DetectRuntimeVariant() = %q/%v, want %q/%v",
					got, valid, test.want, test.valid)
			}
		})
	}
}
