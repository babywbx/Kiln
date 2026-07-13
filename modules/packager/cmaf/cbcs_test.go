package cmaf

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

func TestCbcsDecryptsToThePlaintext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		init  string
		chunk string
	}{
		{"video", "init-stream0.m4s", "chunk-stream0-00001.m4s"},
		{"audio", "init-stream1.m4s", "chunk-stream1-00001.m4s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init, err := ParseInit(readFixture(t, "cbcs", tc.init))
			if err != nil {
				t.Fatalf("ParseInit: %v", err)
			}
			if init.Track.Scheme != "cbcs" {
				t.Fatalf("scheme = %q, want cbcs; the fixture is not what this test claims", init.Track.Scheme)
			}
			if !init.Track.Encrypted || init.Track.KID != fixtureKID {
				t.Fatalf("kid = %q encrypted = %v", init.Track.KID, init.Track.Encrypted)
			}

			seg, err := init.Decrypt(readFixture(t, "cbcs", tc.chunk), testKeys(t))
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}

			got := sampleBytes(t, init.Clear, seg.Clear)
			want := sampleBytes(t,
				readFixture(t, "cbcs-clear", tc.init),
				readFixture(t, "cbcs-clear", tc.chunk))
			if len(got) != len(want) {
				t.Fatalf("decrypted %d samples, the plaintext has %d", len(got), len(want))
			}
			for i := range got {
				if !bytes.Equal(got[i], want[i]) {
					t.Fatalf("sample %d does not match the plaintext (%d vs %d bytes)",
						i, len(got[i]), len(want[i]))
				}
			}
		})
	}
}

func TestCbcsRefusesAKeyForAnotherKID(t *testing.T) {
	init, err := ParseInit(readFixture(t, "cbcs", "init-stream0.m4s"))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	keys, err := NewKeySet(map[string]string{multiVideoKID: multiVideoKey})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	if _, err := init.Decrypt(readFixture(t, "cbcs", "chunk-stream0-00001.m4s"), keys); err == nil {
		t.Fatal("a cbcs segment was decrypted with a key for a different kid")
	}
}

func sampleBytes(t *testing.T, initRaw, segRaw []byte) [][]byte {
	t.Helper()
	initFile, err := mp4.DecodeFile(bytes.NewReader(initRaw))
	if err != nil {
		t.Fatalf("decode init: %v", err)
	}
	segFile, err := mp4.DecodeFile(bytes.NewReader(segRaw))
	if err != nil {
		t.Fatalf("decode segment: %v", err)
	}
	if initFile.Init == nil || initFile.Init.Moov == nil || initFile.Init.Moov.Mvex == nil {
		t.Fatal("init segment has no mvex")
	}
	trex := initFile.Init.Moov.Mvex.Trex

	var out [][]byte
	for _, seg := range segFile.Segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(trex)
			if err != nil {
				t.Fatalf("read samples: %v", err)
			}
			for _, s := range samples {
				out = append(out, s.Data)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no samples in the segment")
	}
	return out
}
