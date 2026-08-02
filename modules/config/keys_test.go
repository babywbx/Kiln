package config

import (
	"strings"
	"testing"
)

func TestParseKeysAcceptsWhatPeopleActuallyPaste(t *testing.T) {
	keys, err := ParseKeys("# a comment\n\nffeeddccbbaa99887766554433221100:00112233445566778899aabbccddeeff\n" +
		"  90a0bd01-d9f6-cbb3-9839-cd9b68fc26bc : aabbccddeeff00112233445566778899  \n")
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(keys))
	}
	if keys[1].KID != "90a0bd01-d9f6-cbb3-9839-cd9b68fc26bc" {
		t.Errorf("kid = %q", keys[1].KID)
	}
}

func TestParseKeysRejectsTheUsualMistakes(t *testing.T) {
	for name, in := range map[string]string{
		"no separator": "ffeeddccbbaa99887766554433221100 00112233445566778899aabbccddeeff",
		"short key":    "ffeeddccbbaa99887766554433221100:0011223344",
		"not hex":      "ffeeddccbbaa99887766554433221100:zzzz2233445566778899aabbccddeeff",
		"dashed key":   "ffeeddccbbaa99887766554433221100:00112233-4455-6677-8899-aabbccddeeff",
		"empty":        "\n# nothing here\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKeys(in); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func TestParseKeysRejectsConflictingDuplicateKIDs(t *testing.T) {
	_, err := ParseKeys(
		"90a0bd01-d9f6-cbb3-9839-cd9b68fc26bc:00112233445566778899aabbccddeeff\n" +
			"90A0BD01D9F6CBB39839CD9B68FC26BC:ffeeddccbbaa99887766554433221100\n",
	)
	if err == nil {
		t.Fatal("conflicting duplicate KID was accepted")
	}
	if !strings.Contains(err.Error(), "conflicting key") {
		t.Fatalf("error = %q, want a conflict explanation", err)
	}
}

func TestParseKeysDeduplicatesEquivalentKIDs(t *testing.T) {
	keys, err := ParseKeys(
		"90a0bd01-d9f6-cbb3-9839-cd9b68fc26bc:00112233445566778899aabbccddeeff\n" +
			"90A0BD01D9F6CBB39839CD9B68FC26BC:00112233445566778899AABBCCDDEEFF\n",
	)
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want one deduplicated key", len(keys))
	}
}
