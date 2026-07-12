package config

import "testing"

func TestParseKeysAcceptsWhatPeopleActuallyPaste(t *testing.T) {
	keys, err := ParseKeys("# a comment\n\nffeeddccbbaa99887766554433221100:00112233445566778899aabbccddeeff\n" +
		"  90a0bd01-d9f6-cbb3-9839-cd9b68fc26bc : aabbccddeeff00112233445566778899  \n")
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	// A dashed KID is what the MPD shows, so it has to be accepted, but it is
	// stored as it was typed; the packager normalizes it.
	if keys[1].KID != "90a0bd01-d9f6-cbb3-9839-cd9b68fc26bc" {
		t.Errorf("kid = %q", keys[1].KID)
	}
}

// The mistakes worth catching at the form, not at the first segment.
func TestParseKeysRejectsTheUsualMistakes(t *testing.T) {
	for name, in := range map[string]string{
		"no separator": "ffeeddccbbaa99887766554433221100 00112233445566778899aabbccddeeff",
		"short key":    "ffeeddccbbaa99887766554433221100:0011223344",
		"not hex":      "ffeeddccbbaa99887766554433221100:zzzz2233445566778899aabbccddeeff",
		"empty":        "\n# nothing here\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKeys(in); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

// The key must never be handed back to the page. The KID may: it is in the
// manifest already.
func TestMaskKeysHidesTheKeyButNotTheKID(t *testing.T) {
	masked := MaskKeys("ffeeddccbbaa99887766554433221100:00112233445566778899aabbccddeeff")
	if masked == "" {
		t.Fatal("masking produced nothing")
	}
	if want := "ffeeddccbbaa99887766554433221100"; !contains(masked, want) {
		t.Errorf("masked = %q, should still show the kid", masked)
	}
	if contains(masked, "00112233445566778899aabbccddeeff") {
		t.Fatalf("masked = %q, the key leaked", masked)
	}
	if !KeysUnchanged(masked) {
		t.Error("handing the masked value back must read as unchanged")
	}
	if KeysUnchanged("ffeeddccbbaa99887766554433221100:00112233445566778899aabbccddeeff") {
		t.Error("a real key must not read as unchanged")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
