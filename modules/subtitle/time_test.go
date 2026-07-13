package subtitle

import (
	"testing"
	"time"
)

func TestParseTimeExpressionSupportsClockOffsetFrameAndTickForms(t *testing.T) {
	t.Parallel()

	timing := TimingParameters{FrameRate: 25, SubFrameRate: 10, TickRate: 100}
	tests := map[string]time.Duration{
		"01:02:03.500":  time.Hour + 2*time.Minute + 3500*time.Millisecond,
		"00:00:10:12.5": 10500 * time.Millisecond,
		"3.5s":          3500 * time.Millisecond,
		"250ms":         250 * time.Millisecond,
		"12.5f":         500 * time.Millisecond,
		"25t":           250 * time.Millisecond,
	}

	for expression, want := range tests {
		expression, want := expression, want
		t.Run(expression, func(t *testing.T) {
			t.Parallel()
			got, err := ParseTimeExpression(expression, timing)
			if err != nil {
				t.Fatalf("ParseTimeExpression(%q): %v", expression, err)
			}
			if got != want {
				t.Fatalf("ParseTimeExpression(%q) = %v, want %v", expression, got, want)
			}
		})
	}
}

func TestParseTimeExpressionRejectsMalformedValues(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{"", "1", "00:61:00", "-1s", "1fortnight"} {
		if _, err := ParseTimeExpression(expression, TimingParameters{}); err == nil {
			t.Errorf("ParseTimeExpression(%q) unexpectedly succeeded", expression)
		}
	}
}
