//go:build extended

package cmaf

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func FuzzCMAF(f *testing.F) {
	for _, dir := range []string{"h264", "hevc", "cbcs"} {
		init := readFixtureForFuzz(f, dir, "init-stream0.m4s")
		segment := readFixtureForFuzz(f, dir, "chunk-stream0-00001.m4s")
		f.Add(init, segment)
		f.Add(init[:len(init)/2], segment[:len(segment)/2])
		bad := bytes.Clone(segment)
		if len(bad) >= 4 {
			bad[0], bad[1], bad[2], bad[3] = 0xff, 0xff, 0xff, 0xff
		}
		f.Add(init, bad)
	}
	f.Fuzz(func(t *testing.T, initRaw, segmentRaw []byte) {
		init, err := ParseInit(initRaw)
		if err != nil {
			return
		}
		_, _ = init.SplitParts(segmentRaw, 200*time.Millisecond)
		segment, err := init.Decrypt(segmentRaw, testKeys(t))
		if err != nil {
			return
		}
		reparsed, err := ParseInit(init.Clear)
		if err != nil {
			t.Fatalf("clear init cannot be reparsed: %v", err)
		}
		if reparsed.Track.Encrypted {
			t.Fatal("clear init remains protected")
		}
		parts, err := reparsed.SplitParts(segment.Clear, 200*time.Millisecond)
		if err != nil {
			t.Fatalf("clear segment cannot be split: %v", err)
		}
		if len(parts) == 0 {
			t.Fatal("successful split returned no parts")
		}
		if len(segment.Clear) == 0 {
			t.Fatal("successful decrypt returned empty media")
		}
		wrong, err := NewKeySet(map[string]string{"0123456789abcdef0123456789abcdef": "00112233445566778899aabbccddeeff"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := init.Decrypt(segmentRaw, wrong); err == nil {
			t.Fatal("wrong KID succeeded")
		}
	})
}

func readFixtureForFuzz(f *testing.F, dir, name string) []byte {
	f.Helper()
	data, err := os.ReadFile(fixturePath(dir, name))
	if err != nil {
		f.Fatal(err)
	}
	return data
}

func TestDecryptOwnedReservedStaysWithinAllocationBudget(t *testing.T) {
	init, err := ParseInit(readFixture(t, "h264", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	raw := readFixture(t, "h264", "chunk-stream0-00001.m4s")
	keys := testKeys(t)

	var decryptErr error
	allocs := testing.AllocsPerRun(10, func() {
		_, decryptErr = init.DecryptOwnedReserved(bytes.Clone(raw), keys, nil)
	})
	if decryptErr != nil {
		t.Fatal(decryptErr)
	}
	if allocs > 180 {
		t.Fatalf("DecryptOwnedReserved allocated %.0f objects, want at most 180", allocs)
	}
}

func TestDecryptOwnedReservedCBCSStaysWithinAllocationBudget(t *testing.T) {
	init, err := ParseInit(readFixture(t, "cbcs", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	raw := readFixture(t, "cbcs", "chunk-stream0-00001.m4s")
	keys := testKeys(t)

	var decryptErr error
	allocs := testing.AllocsPerRun(10, func() {
		_, decryptErr = init.DecryptOwnedReserved(bytes.Clone(raw), keys, nil)
	})
	if decryptErr != nil {
		t.Fatal(decryptErr)
	}
	if allocs > 120 {
		t.Fatalf("DecryptOwnedReserved CBCS allocated %.0f objects, want at most 120", allocs)
	}
}

func BenchmarkDecrypt(b *testing.B) {
	for _, dir := range []string{"h264", "hevc", "cbcs"} {
		b.Run(dir, func(b *testing.B) {
			initRaw, err := os.ReadFile(fixturePath(dir, "init-stream0.m4s"))
			if err != nil {
				b.Fatal(err)
			}
			segmentRaw, err := os.ReadFile(fixturePath(dir, "chunk-stream0-00001.m4s"))
			if err != nil {
				b.Fatal(err)
			}
			init, err := ParseInit(initRaw)
			if err != nil {
				b.Fatal(err)
			}
			keys, err := NewKeySet(map[string]string{fixtureKID: fixtureKey})
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(segmentRaw)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := init.Decrypt(segmentRaw, keys); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecryptOwned(b *testing.B) {
	for _, dir := range []string{"h264", "hevc", "cbcs"} {
		b.Run(dir, func(b *testing.B) {
			initRaw, err := os.ReadFile(fixturePath(dir, "init-stream0.m4s"))
			if err != nil {
				b.Fatal(err)
			}
			segmentRaw, err := os.ReadFile(fixturePath(dir, "chunk-stream0-00001.m4s"))
			if err != nil {
				b.Fatal(err)
			}
			init, err := ParseInit(initRaw)
			if err != nil {
				b.Fatal(err)
			}
			keys, err := NewKeySet(map[string]string{fixtureKID: fixtureKey})
			if err != nil {
				b.Fatal(err)
			}
			owned := make([]byte, len(segmentRaw))
			b.SetBytes(int64(len(segmentRaw)))
			b.ReportAllocs()
			for b.Loop() {
				copy(owned, segmentRaw)
				if _, err := init.DecryptOwnedReserved(owned, keys, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
