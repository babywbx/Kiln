package cmaf

import (
	"bytes"
	"math"
	"os"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

type partFixtureFragment struct {
	events  []*mp4.EmsgBox
	samples []mp4.FullSample
}

func TestSplitPartsProducesDecodableVideoFragments(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream0.m4s")
	duration := init.Track.Timescale / 10
	base := uint64(12_000)
	samples := []mp4.FullSample{
		partSample(base, duration, mp4.SyncSampleFlags, "one"),
		partSample(base+uint64(duration), duration, mp4.NonSyncSampleFlags, "two"),
		partSample(base+2*uint64(duration), duration, mp4.NonSyncSampleFlags, "three"),
		partSample(base+3*uint64(duration), duration, mp4.SyncSampleFlags, "four"),
	}
	event := &mp4.EmsgBox{
		Version:          1,
		TimeScale:        init.Track.Timescale,
		PresentationTime: base,
		EventDuration:    duration,
		ID:               11,
		SchemeIDURI:      "https://aomedia.org/emsg/ID3",
		Value:            "metadata",
		MessageData:      []byte("event payload"),
	}
	raw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{events: []*mp4.EmsgBox{event}, samples: samples}})

	parts, err := init.SplitParts(raw, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("SplitParts: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if !parts[0].Independent || parts[1].Independent {
		t.Fatalf("independent flags = [%t %t], want [true false]", parts[0].Independent, parts[1].Independent)
	}
	if parts[0].BaseTime != base || parts[1].BaseTime != base+2*uint64(duration) {
		t.Fatalf("base times = [%d %d]", parts[0].BaseTime, parts[1].BaseTime)
	}
	if parts[0].Duration != 2*uint64(duration) || parts[1].Duration != 2*uint64(duration) {
		t.Fatalf("durations = [%d %d]", parts[0].Duration, parts[1].Duration)
	}
	if len(parts[0].Events) != 1 || len(parts[1].Events) != 0 {
		t.Fatalf("event counts = [%d %d], want [1 0]", len(parts[0].Events), len(parts[1].Events))
	}

	wantSamples := samples
	var gotSamples []mp4.FullSample
	var totalDuration uint64
	for index, part := range parts {
		decoded := decodePart(t, part.Data, trackTrex(init.di, init.Track.ID))
		if len(decoded.Segments) != 1 || len(decoded.Segments[0].Fragments) != 1 {
			t.Fatalf("part %d is not one media fragment", index)
		}
		fragment := decoded.Segments[0].Fragments[0]
		fragmentSamples, err := fragment.GetFullSamples(trackTrex(init.di, init.Track.ID))
		if err != nil {
			t.Fatalf("part %d samples: %v", index, err)
		}
		gotSamples = append(gotSamples, fragmentSamples...)
		totalDuration += parts[index].Duration
		if got := len(fragment.Emsgs); got != len(parts[index].Events) {
			t.Fatalf("part %d encoded %d events, metadata reports %d", index, got, len(parts[index].Events))
		}
	}
	assertPartSamples(t, gotSamples, wantSamples)
	if totalDuration != 4*uint64(duration) {
		t.Fatalf("total duration = %d, want %d", totalDuration, 4*uint64(duration))
	}

	encodedPart := bytes.Clone(parts[0].Data)
	eventData := bytes.Clone(parts[0].Events[0].MessageData)
	for index := range raw {
		raw[index] = 0
	}
	if !bytes.Equal(parts[0].Data, encodedPart) || !bytes.Equal(parts[0].Events[0].MessageData, eventData) {
		t.Fatal("part output aliases the caller-owned input")
	}
}

func TestSplitPartsFromSequenceUsesCallerSequenceAcrossSegments(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream0.m4s")
	duration := init.Track.Timescale / 10
	firstRaw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{samples: []mp4.FullSample{
		partSample(12_000, duration, mp4.SyncSampleFlags, "one"),
		partSample(12_000+uint64(duration), duration, mp4.NonSyncSampleFlags, "two"),
		partSample(12_000+2*uint64(duration), duration, mp4.SyncSampleFlags, "three"),
		partSample(12_000+3*uint64(duration), duration, mp4.NonSyncSampleFlags, "four"),
	}}})
	secondRaw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{samples: []mp4.FullSample{
		partSample(12_000+4*uint64(duration), duration, mp4.SyncSampleFlags, "five"),
		partSample(12_000+5*uint64(duration), duration, mp4.NonSyncSampleFlags, "six"),
	}}})

	first, err := init.SplitPartsFromSequence(firstRaw, 200*time.Millisecond, 41)
	if err != nil {
		t.Fatalf("first SplitPartsFromSequence: %v", err)
	}
	second, err := init.SplitPartsFromSequence(secondRaw, 200*time.Millisecond, 43)
	if err != nil {
		t.Fatalf("second SplitPartsFromSequence: %v", err)
	}
	parts := append(first, second...)
	wantSequences := []uint32{41, 42, 43}
	if len(parts) != len(wantSequences) {
		t.Fatalf("got %d parts, want %d", len(parts), len(wantSequences))
	}
	for index, part := range parts {
		decoded := decodePart(t, part.Data, trackTrex(init.di, init.Track.ID))
		got := decoded.Segments[0].Fragments[0].Moof.Mfhd.SequenceNumber
		if got != wantSequences[index] {
			t.Errorf("part %d sequence = %d, want %d", index, got, wantSequences[index])
		}
	}
}

func TestSplitPartsFromSequenceRejectsSequenceOverflow(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream0.m4s")
	duration := init.Track.Timescale / 10
	raw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{samples: []mp4.FullSample{
		partSample(12_000, duration, mp4.SyncSampleFlags, "one"),
		partSample(12_000+uint64(duration), duration, mp4.NonSyncSampleFlags, "two"),
	}}})

	if _, err := init.SplitPartsFromSequence(raw, 100*time.Millisecond, math.MaxUint32); err == nil {
		t.Fatal("part sequence overflow unexpectedly succeeded")
	}
}

func TestRewritePartSequenceUpdatesStagedFragmentInPlace(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream0.m4s")
	raw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{
		samples: []mp4.FullSample{partSample(0, init.Track.Timescale/10, mp4.SyncSampleFlags, "one")},
	}})
	parts, err := init.SplitParts(raw, 200*time.Millisecond)
	if err != nil || len(parts) != 1 {
		t.Fatalf("SplitParts = %d, %v", len(parts), err)
	}
	path := t.TempDir() + "/part.m4s"
	if err := os.WriteFile(path, parts[0].Data, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := RewritePartSequence(file, 77); err != nil {
		file.Close()
		t.Fatalf("RewritePartSequence: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Segments[0].Fragments[0].Moof.Mfhd.SequenceNumber; got != 77 {
		t.Fatalf("sequence = %d, want 77", got)
	}
}

func TestSplitPartsMarksEveryAudioPartIndependent(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream1.m4s")
	duration := init.Track.Timescale / 10
	base := uint64(48_000)
	samples := []mp4.FullSample{
		partSample(base, duration, mp4.NonSyncSampleFlags, "audio-one"),
		partSample(base+uint64(duration), duration, mp4.NonSyncSampleFlags, "audio-two"),
		partSample(base+2*uint64(duration), duration, mp4.NonSyncSampleFlags, "audio-three"),
	}
	raw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{samples: samples}})

	parts, err := init.SplitParts(raw, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("SplitParts: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	for index, part := range parts {
		if !part.Independent {
			t.Errorf("audio part %d is not independent", index)
		}
	}
}

func TestSplitPartsKeepsEventsWithTheirSourceFragment(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream1.m4s")
	duration := init.Track.Timescale / 10
	base := uint64(96_000)
	event := &mp4.EmsgBox{
		Version:               0,
		TimeScale:             1000,
		PresentationTimeDelta: 25,
		EventDuration:         100,
		ID:                    19,
		SchemeIDURI:           "urn:scte:scte35:2013:bin",
		Value:                 "splice",
		MessageData:           []byte{0xfc, 0x30, 0x01},
	}
	raw := encodePartFixture(t, init.Track.ID, []partFixtureFragment{
		{samples: []mp4.FullSample{
			partSample(base, duration, 0, "one"),
			partSample(base+uint64(duration), duration, 0, "two"),
		}},
		{events: []*mp4.EmsgBox{event}, samples: []mp4.FullSample{
			partSample(base+2*uint64(duration), duration, 0, "three"),
			partSample(base+3*uint64(duration), duration, 0, "four"),
		}},
	})

	parts, err := init.SplitParts(raw, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("SplitParts: %v", err)
	}
	if len(parts) != 2 || len(parts[0].Events) != 0 || len(parts[1].Events) != 1 {
		t.Fatalf("part event counts = %v", []int{len(parts[0].Events), len(parts[1].Events)})
	}
	if got := parts[1].Events[0]; got.ID != event.ID || !bytes.Equal(got.MessageData, event.MessageData) {
		t.Fatalf("event = %#v, want ID %d and original payload", got, event.ID)
	}
}

func TestSplitPartsRejectsInvalidInputsWithoutPanicking(t *testing.T) {
	t.Parallel()

	init := mustParseFixtureInit(t, "h264", "init-stream1.m4s")
	duration := init.Track.Timescale / 10
	base := uint64(1_000)
	valid := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{samples: []mp4.FullSample{
		partSample(base, duration, 0, "one"),
	}}})

	for _, target := range []time.Duration{0, -time.Millisecond} {
		if _, err := init.SplitParts(valid, target); err == nil {
			t.Errorf("target %s unexpectedly succeeded", target)
		}
	}
	if _, err := init.SplitParts(nil, time.Second); err == nil {
		t.Error("empty segment unexpectedly succeeded")
	}
	if _, err := init.SplitParts([]byte{0, 0, 0, 1, 0xff}, time.Second); err == nil {
		t.Error("malformed box stream unexpectedly succeeded")
	}
	var nilInit *Init
	if _, err := nilInit.SplitParts(valid, time.Second); err == nil {
		t.Error("nil init unexpectedly succeeded")
	}

	gapped := encodePartFixture(t, init.Track.ID, []partFixtureFragment{
		{samples: []mp4.FullSample{partSample(base, duration, 0, "one")}},
		{samples: []mp4.FullSample{partSample(base+uint64(duration)+1, duration, 0, "two")}},
	})
	if _, err := init.SplitParts(gapped, time.Second); err == nil {
		t.Error("segment with discontinuous DTS unexpectedly succeeded")
	}
	emptyFragment := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{}})
	if _, err := init.SplitParts(emptyFragment, time.Second); err == nil {
		t.Error("empty fragment unexpectedly succeeded")
	}
	wrongTrack := encodePartFixture(t, init.Track.ID+1, []partFixtureFragment{{samples: []mp4.FullSample{
		partSample(base, duration, 0, "wrong-track"),
	}}})
	if _, err := init.SplitParts(wrongTrack, time.Second); err == nil {
		t.Error("fragment for another track unexpectedly succeeded")
	}
	overflowingDTS := encodePartFixture(t, init.Track.ID, []partFixtureFragment{{samples: []mp4.FullSample{
		partSample(math.MaxUint64-uint64(duration)+1, duration, 0, "overflow"),
	}}})
	if _, err := init.SplitParts(overflowingDTS, time.Second); err == nil {
		t.Error("sample whose decode time overflows unexpectedly succeeded")
	}

	tooLarge := *init
	tooLarge.Track.Timescale = math.MaxUint32
	if _, err := tooLarge.SplitParts(valid, time.Duration(math.MaxInt64)); err == nil {
		t.Error("target duration that overflows the track timescale unexpectedly succeeded")
	}
}

func mustParseFixtureInit(t *testing.T, dir, name string) *Init {
	t.Helper()
	init, err := ParseInit(readFixture(t, dir, name))
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	if init.Track.Timescale < 10 {
		t.Fatalf("fixture timescale %d is too small", init.Track.Timescale)
	}
	return init
}

func partSample(decodeTime uint64, duration uint32, flags uint32, payload string) mp4.FullSample {
	data := []byte(payload)
	return mp4.FullSample{
		Sample: mp4.Sample{
			Flags: flags,
			Dur:   duration,
			Size:  uint32(len(data)),
		},
		DecodeTime: decodeTime,
		Data:       data,
	}
}

func encodePartFixture(t *testing.T, trackID uint32, fragments []partFixtureFragment) []byte {
	t.Helper()
	segment := mp4.NewMediaSegment()
	for index, spec := range fragments {
		fragment, err := mp4.CreateFragment(uint32(index+1), trackID)
		if err != nil {
			t.Fatalf("CreateFragment: %v", err)
		}
		for _, event := range spec.events {
			fragment.AddEmsg(event)
		}
		for _, sample := range spec.samples {
			fragment.AddFullSample(sample)
		}
		segment.AddFragment(fragment)
	}
	var encoded bytes.Buffer
	if err := segment.Encode(&encoded); err != nil {
		t.Fatalf("encode segment: %v", err)
	}
	return encoded.Bytes()
}

func decodePart(t *testing.T, raw []byte, trex *mp4.TrexBox) *mp4.File {
	t.Helper()
	decoded, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode part: %v", err)
	}
	if len(decoded.Segments) != 1 || len(decoded.Segments[0].Fragments) != 1 {
		t.Fatalf("decoded part has %d segments", len(decoded.Segments))
	}
	if _, err := decoded.Segments[0].Fragments[0].GetFullSamples(trex); err != nil {
		t.Fatalf("decode samples: %v", err)
	}
	return decoded
}

func assertPartSamples(t *testing.T, got, want []mp4.FullSample) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].DecodeTime != want[index].DecodeTime ||
			got[index].Dur != want[index].Dur ||
			got[index].Flags != want[index].Flags ||
			got[index].CompositionTimeOffset != want[index].CompositionTimeOffset ||
			!bytes.Equal(got[index].Data, want[index].Data) {
			t.Errorf("sample %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}
