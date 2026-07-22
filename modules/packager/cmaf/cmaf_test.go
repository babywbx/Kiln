package cmaf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

const (
	fixtureKey = "00112233445566778899aabbccddeeff"
	fixtureKID = "ffeeddccbbaa99887766554433221100"

	multiVideoKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	multiVideoKID = "11111111111111111111111111111111"
	multiAudioKey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	multiAudioKID = "22222222222222222222222222222222"
)

type fixture struct {
	name   string
	dir    string
	init   string
	chunks []string
	codec  string
	kind   Kind
}

var fixtures = []fixture{
	{
		name:   "hevc_video",
		dir:    "hevc",
		init:   "init-stream0.m4s",
		chunks: []string{"chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s", "chunk-stream0-00003.m4s"},
		codec:  "hvc1.1.6.L60.90",
		kind:   KindVideo,
	},
	{
		name:   "hevc_audio",
		dir:    "hevc",
		init:   "init-stream1.m4s",
		chunks: []string{"chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"},
		codec:  "mp4a.40.2",
		kind:   KindAudio,
	},
	{
		name:   "hev1_video",
		dir:    "hev1",
		init:   "init-stream0.m4s",
		chunks: []string{"chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s", "chunk-stream0-00003.m4s"},
		codec:  "hvc1.1.6.L60.90",
		kind:   KindVideo,
	},
	{
		name:   "hev1_audio",
		dir:    "hev1",
		init:   "init-stream1.m4s",
		chunks: []string{"chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"},
		codec:  "mp4a.40.2",
		kind:   KindAudio,
	},
	{
		name:   "h264_video",
		dir:    "h264",
		init:   "init-stream0.m4s",
		chunks: []string{"chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s"},
		codec:  "avc1.42C00C",
		kind:   KindVideo,
	},
	{
		name:   "h264_audio",
		dir:    "h264",
		init:   "init-stream1.m4s",
		chunks: []string{"chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"},
		codec:  "mp4a.40.2",
		kind:   KindAudio,
	},
}

func fixturePath(dir, name string) string {
	return filepath.Join("..", "..", "..", "testdata", "cenc", dir, name)
}

func readFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixturePath(dir, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func testKeys(t *testing.T) KeySet {
	t.Helper()
	ks, err := NewKeySet(map[string]string{fixtureKID: fixtureKey})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	return ks
}

// The fixture MPDs carry no default_KID, so a KID that only comes from the
// init segment's tenc box is the whole point.
func TestParseInitTakesKIDFromTenc(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			init, err := ParseInit(readFixture(t, f.dir, f.init))
			if err != nil {
				t.Fatalf("ParseInit: %v", err)
			}
			if !init.Track.Encrypted {
				t.Fatal("track should be encrypted")
			}
			if init.Track.KID != fixtureKID {
				t.Errorf("KID = %s, want %s", init.Track.KID, fixtureKID)
			}
			if init.Track.Scheme != "cenc" {
				t.Errorf("scheme = %s, want cenc", init.Track.Scheme)
			}
			if init.Track.Kind != f.kind {
				t.Errorf("kind = %s, want %s", init.Track.Kind, f.kind)
			}
			if init.Track.Codec != f.codec {
				t.Errorf("codec = %s, want %s", init.Track.Codec, f.codec)
			}
			if init.Track.Timescale == 0 {
				t.Error("timescale is zero")
			}
		})
	}
}

func TestClearInitDropsProtection(t *testing.T) {
	init, err := ParseInit(readFixture(t, "hevc", "init-stream0.m4s"))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	for _, box := range []string{"encv", "enca", "sinf", "pssh", "tenc", "schm"} {
		if bytes.Contains(init.Clear, []byte(box)) {
			t.Errorf("clear init still contains %s box", box)
		}
	}
	f, err := mp4.DecodeFile(bytes.NewReader(init.Clear))
	if err != nil {
		t.Fatalf("clear init does not decode: %v", err)
	}
	stsd := f.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	if stsd.HvcX == nil || stsd.HvcX.Type() != "hvc1" {
		t.Fatalf("clear init sample entry is not hvc1")
	}
}

func TestDecryptDropsEncryptionBoxes(t *testing.T) {
	keys := testKeys(t)
	init, err := ParseInit(readFixture(t, "hevc", "init-stream0.m4s"))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	raw := readFixture(t, "hevc", "chunk-stream0-00001.m4s")
	seg, err := init.Decrypt(raw, keys)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	for _, box := range []string{"senc", "saiz", "saio"} {
		if bytes.Contains(seg.Clear, []byte(box)) {
			t.Errorf("clear segment still contains %s box", box)
		}
	}
	if bytes.Equal(seg.Clear, raw) {
		t.Error("clear segment is byte-identical to the encrypted one")
	}
	if seg.Duration == 0 {
		t.Error("segment duration is zero")
	}
}

func TestDecryptMatchesMP4FFReferenceBytes(t *testing.T) {
	keys := testKeys(t)
	for _, tc := range []struct {
		dir    string
		stream int
	}{
		{"h264", 0}, {"h264", 1},
		{"hevc", 0}, {"hevc", 1},
		{"cbcs", 0}, {"cbcs", 1},
	} {
		t.Run(fmt.Sprintf("%s/stream%d", tc.dir, tc.stream), func(t *testing.T) {
			initName := fmt.Sprintf("init-stream%d.m4s", tc.stream)
			chunkName := fmt.Sprintf("chunk-stream%d-00001.m4s", tc.stream)
			initRaw := readFixture(t, tc.dir, initName)
			init, err := ParseInit(initRaw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := init.Decrypt(readFixture(t, tc.dir, chunkName), keys)
			if err != nil {
				t.Fatal(err)
			}

			referenceInit, err := mp4.DecodeFile(bytes.NewReader(initRaw))
			if err != nil {
				t.Fatal(err)
			}
			decryptInfo, err := mp4.DecryptInit(referenceInit.Init)
			if err != nil {
				t.Fatal(err)
			}
			referenceFile, err := mp4.DecodeFile(bytes.NewReader(
				readFixture(t, tc.dir, chunkName),
			))
			if err != nil {
				t.Fatal(err)
			}
			reference := referenceFile.Segments[0]
			if err := mp4.DecryptSegmentWithKeys(reference, decryptInfo, nil, keys, true); err != nil {
				t.Fatal(err)
			}
			var want bytes.Buffer
			if err := reference.Encode(&want); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Clear, want.Bytes()) {
				t.Fatal("clear segment differs from mp4ff reference bytes")
			}
		})
	}
}

func TestDecryptPreservesCallerInput(t *testing.T) {
	init, err := ParseInit(readFixture(t, "hevc", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	raw := readFixture(t, "hevc", "chunk-stream0-00001.m4s")
	want := bytes.Clone(raw)
	if _, err := init.Decrypt(raw, testKeys(t)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatal("Decrypt mutated its caller-owned input")
	}
}

func TestDecryptOwnedReservedUsesTheEncodedSize(t *testing.T) {
	init, err := ParseInit(readFixture(t, "h264", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	raw := readFixture(t, "h264", "chunk-stream0-00001.m4s")
	var reserved int64
	seg, err := init.DecryptOwnedReserved(raw, testKeys(t), func(size int64) error {
		reserved = size
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reserved != int64(len(seg.Clear)) {
		t.Fatalf("reserved %d bytes, encoded %d", reserved, len(seg.Clear))
	}
}

func TestDecryptOwnedToMatchesBufferedOutput(t *testing.T) {
	for _, fixture := range []string{"h264", "hevc", "cbcs"} {
		t.Run(fixture, func(t *testing.T) {
			init, err := ParseInit(readFixture(t, fixture, "init-stream0.m4s"))
			if err != nil {
				t.Fatal(err)
			}
			raw := readFixture(t, fixture, "chunk-stream0-00001.m4s")
			buffered, err := init.DecryptOwnedReserved(bytes.Clone(raw), testKeys(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			var streamed bytes.Buffer
			metadata, err := init.DecryptOwnedTo(bytes.Clone(raw), testKeys(t), &streamed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(streamed.Bytes(), buffered.Clear) {
				t.Fatal("streamed clear segment differs from buffered output")
			}
			if metadata.BaseTime != buffered.BaseTime || metadata.Duration != buffered.Duration ||
				!reflect.DeepEqual(metadata.Events, buffered.Events) {
				t.Fatalf("streamed metadata = %+v, want %+v", metadata, buffered)
			}
		})
	}
}

func TestDecryptOwnedToReturnsWriterFailure(t *testing.T) {
	init, err := ParseInit(readFixture(t, "h264", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("disk full")
	writer := writerFunc(func([]byte) (int, error) { return 0, want })

	segment, err := init.DecryptOwnedTo(
		readFixture(t, "h264", "chunk-stream0-00001.m4s"), testKeys(t), writer,
	)

	if segment != nil {
		t.Fatal("writer failure returned segment metadata")
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(data []byte) (int, error) {
	return write(data)
}

func TestDecryptOwnedReservedReturnsReservationError(t *testing.T) {
	init, err := ParseInit(readFixture(t, "h264", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("memory budget exhausted")
	seg, err := init.DecryptOwnedReserved(
		readFixture(t, "h264", "chunk-stream0-00001.m4s"),
		testKeys(t),
		func(int64) error { return want },
	)
	if seg != nil {
		t.Fatal("reservation failure returned a segment")
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestDecryptOwnedReservedAccountsForEverySidx(t *testing.T) {
	initRaw, trackID := makeSTPPInit(t)
	init, err := ParseInit(initRaw)
	if err != nil {
		t.Fatal(err)
	}
	raw := makeTextSegment(t, trackID, nil, []mp4.FullSample{{
		Sample: mp4.Sample{Dur: 1000, Size: 1},
		Data:   []byte{0},
	}})
	file, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	file.Segments[0].AddSidx(mp4.CreateSidx(0))
	file.Segments[0].AddSidx(mp4.CreateSidx(0))
	var encoded bytes.Buffer
	if err := file.Segments[0].Encode(&encoded); err != nil {
		t.Fatal(err)
	}

	var reserved int64
	seg, err := init.DecryptOwnedReserved(encoded.Bytes(), nil, func(size int64) error {
		reserved = size
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reserved != int64(len(seg.Clear)) {
		t.Fatalf("reserved %d bytes, encoded %d", reserved, len(seg.Clear))
	}
}

func TestOwnedDecodeAliasesTheCiphertextMdat(t *testing.T) {
	raw := readFixture(t, "hevc", "chunk-stream0-00001.m4s")
	f, err := decodeOwnedSegment(raw)
	if err != nil {
		t.Fatal(err)
	}
	mdat := f.Segments[0].Fragments[0].Mdat
	start := mdat.PayloadAbsoluteOffset()
	if len(mdat.Data) == 0 {
		t.Fatal("decoded mdat is empty")
	}
	if &mdat.Data[0] != &raw[start] {
		t.Fatal("owned decode copied mdat instead of aliasing the input")
	}
}

// A KID with no key must fail. The ffmpeg path silently falls back to the
// first key here, which is exactly the behaviour the native path must not have.
func TestDecryptStrictKIDRefusesWrongKey(t *testing.T) {
	init, err := ParseInit(readFixture(t, "hevc", "init-stream0.m4s"))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	keys, err := NewKeySet(map[string]string{
		"0123456789abcdef0123456789abcdef": fixtureKey,
	})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	_, err = init.Decrypt(readFixture(t, "hevc", "chunk-stream0-00001.m4s"), keys)
	if err == nil {
		t.Fatal("expected decrypt to fail for an unknown KID")
	}
	u, ok := Unsupported(err)
	if !ok || u.Reason != ReasonMissingKey {
		t.Fatalf("err = %v, want reason %s", err, ReasonMissingKey)
	}
}

// The hev1 fixture is a real hev1-tagged source. Safari and AVPlayer reject
// hev1 in fMP4, so it has to come out of the rewrite as hvc1.
func TestHev1BecomesHvc1(t *testing.T) {
	init, err := ParseInit(readFixture(t, "hev1", "init-stream0.m4s"))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	if !strings.HasPrefix(init.Track.Codec, "hvc1.") {
		t.Fatalf("codec = %s, want hvc1 prefix", init.Track.Codec)
	}
	f, err := mp4.DecodeFile(bytes.NewReader(init.Clear))
	if err != nil {
		t.Fatalf("decode rewritten init: %v", err)
	}
	if got := f.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.HvcX.Type(); got != "hvc1" {
		t.Fatalf("sample entry = %s, want hvc1", got)
	}
}

// hev1 carrying its parameter sets inband only cannot be rewritten without
// touching samples, so it must be reported as unsupported, not mangled. ffmpeg
// always writes them into hvcC, so this one input has to be synthesized.
func TestHev1WithInbandParameterSetsIsUnsupported(t *testing.T) {
	raw := stripParameterSets(t, readFixture(t, "hev1", "init-stream0.m4s"))
	_, err := ParseInit(raw)
	u, ok := Unsupported(err)
	if !ok || u.Reason != ReasonInbandParamSets {
		t.Fatalf("err = %v, want reason %s", err, ReasonInbandParamSets)
	}
}

func stripParameterSets(t *testing.T, raw []byte) []byte {
	t.Helper()
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode init: %v", err)
	}
	stsd := f.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	entry := stsd.Encv
	if entry == nil {
		entry = stsd.HvcX
	}
	if entry == nil || entry.HvcC == nil {
		t.Fatal("no hevc sample entry in fixture")
	}
	entry.HvcC.NaluArrays = nil
	var buf bytes.Buffer
	if err := f.Init.Encode(&buf); err != nil {
		t.Fatalf("encode init: %v", err)
	}
	return buf.Bytes()
}

// A stream whose tracks carry different KIDs is the case only the native path
// can serve: each track has to be decrypted with its own key.
func TestMultiKIDDecryptsEachTrackWithItsOwnKey(t *testing.T) {
	keys, err := NewKeySet(map[string]string{
		multiVideoKID: multiVideoKey,
		multiAudioKID: multiAudioKey,
	})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	for _, tc := range []struct {
		stream int
		kid    string
	}{{0, multiVideoKID}, {1, multiAudioKID}} {
		initRaw := readFixture(t, "multikid", fmt.Sprintf("init-stream%d.m4s", tc.stream))
		init, err := ParseInit(initRaw)
		if err != nil {
			t.Fatalf("ParseInit stream%d: %v", tc.stream, err)
		}
		if init.Track.KID != tc.kid {
			t.Fatalf("stream%d KID = %s, want %s", tc.stream, init.Track.KID, tc.kid)
		}
		chunk := readFixture(t, "multikid", fmt.Sprintf("chunk-stream%d-00001.m4s", tc.stream))
		if _, err := init.Decrypt(chunk, keys); err != nil {
			t.Fatalf("Decrypt stream%d: %v", tc.stream, err)
		}
	}
}

// Handing only one of the two keys must fail loudly. This is precisely where
// ffmpeg would quietly use the key it has and emit garbage.
func TestMultiKIDRefusesAPartialKeySet(t *testing.T) {
	keys, err := NewKeySet(map[string]string{multiVideoKID: multiVideoKey})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	init, err := ParseInit(readFixture(t, "multikid", "init-stream1.m4s"))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	_, err = init.Decrypt(readFixture(t, "multikid", "chunk-stream1-00001.m4s"), keys)
	u, ok := Unsupported(err)
	if !ok || u.Reason != ReasonMissingKey {
		t.Fatalf("err = %v, want reason %s", err, ReasonMissingKey)
	}
}

// TestDecryptMatchesFFmpeg is the correctness anchor: our decrypted output and
// ffmpeg's decryption of the same fixture must decode to identical frames.
// Structural checks cannot prove this, since CENC leaves NAL headers in clear.
func TestDecryptMatchesFFmpeg(t *testing.T) {
	requireFFmpeg(t)
	keys := testKeys(t)
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			dir := t.TempDir()

			var encrypted bytes.Buffer
			var clear bytes.Buffer
			initRaw := readFixture(t, f.dir, f.init)
			init, err := ParseInit(initRaw)
			if err != nil {
				t.Fatalf("ParseInit: %v", err)
			}
			encrypted.Write(initRaw)
			clear.Write(init.Clear)
			for _, name := range f.chunks {
				chunk := readFixture(t, f.dir, name)
				seg, err := init.Decrypt(chunk, keys)
				if err != nil {
					t.Fatalf("Decrypt %s: %v", name, err)
				}
				encrypted.Write(chunk)
				clear.Write(seg.Clear)
			}

			encPath := filepath.Join(dir, "encrypted.mp4")
			clearPath := filepath.Join(dir, "clear.mp4")
			if err := os.WriteFile(encPath, encrypted.Bytes(), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := os.WriteFile(clearPath, clear.Bytes(), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			want := frameCRC(t, encPath, fixtureKey)
			got := frameCRC(t, clearPath, "")
			if want == "" {
				t.Fatal("ffmpeg reference decryption produced no frames")
			}
			if got != want {
				t.Errorf("decoded frames differ from ffmpeg reference\nours:\n%s\nffmpeg:\n%s", got, want)
			}
		})
	}
}

func frameCRC(t *testing.T, path, key string) string {
	t.Helper()
	args := []string{"-v", "error", "-nostdin"}
	if key != "" {
		args = append(args, "-decryption_key", key)
	}
	args = append(args, "-i", path, "-f", "framecrc", "-")
	cmd := exec.Command("ffmpeg", args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, errBuf.String())
	}
	if errBuf.Len() > 0 {
		t.Fatalf("ffmpeg reported decode errors for %s:\n%s", filepath.Base(path), errBuf.String())
	}
	return out.String()
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return
	}
	if os.Getenv("KILN_REQUIRE_MEDIA_ORACLE") == "1" {
		t.Fatal("ffmpeg is required by KILN_REQUIRE_MEDIA_ORACLE=1")
	}
	t.Skip("ffmpeg not available")
}
