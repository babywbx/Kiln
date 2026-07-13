package subtitle

import (
	"bytes"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

func TestDecodeSTPPSamplesReadsPayloadBoundariesAndMediaTimes(t *testing.T) {
	t.Parallel()

	firstPayload := []byte(`<tt><body><p begin="1s" dur="1s">first</p></body></tt>`)
	secondPayload := []byte(`<tt><body><p begin="1.45s" dur="1s">second</p></body></tt>`)
	raw := makeSTPPSegment(t, []mp4.FullSample{
		{
			Sample:     mp4.Sample{Dur: 45000, Size: uint32(len(firstPayload)), CompositionTimeOffset: 9000},
			DecodeTime: 90000,
			Data:       firstPayload,
		},
		{
			Sample:     mp4.Sample{Dur: 90000, Size: uint32(len(secondPayload)), CompositionTimeOffset: -4500},
			DecodeTime: 135000,
			Data:       secondPayload,
		},
	})

	samples, err := DecodeSTPPSamples(raw, STPPTrack{ID: 7, Timescale: 90000})
	if err != nil {
		t.Fatalf("DecodeSTPPSamples: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("DecodeSTPPSamples returned %d samples, want 2", len(samples))
	}
	assertSTPPSample(t, samples[0], time.Second, 1100*time.Millisecond, 500*time.Millisecond, firstPayload)
	assertSTPPSample(t, samples[1], 1500*time.Millisecond, 1450*time.Millisecond, time.Second, secondPayload)
	cues, err := ParseSTPPSample(samples[0], TimingParameters{})
	if err != nil {
		t.Fatalf("ParseSTPPSample: %v", err)
	}
	wantCue := Cue{Start: 1100 * time.Millisecond, End: 1600 * time.Millisecond, Text: "first"}
	if len(cues) != 1 || cues[0] != wantCue {
		t.Fatalf("ParseSTPPSample cues = %#v, want %#v", cues, []Cue{wantCue})
	}

	for index := range raw {
		raw[index] = 0
	}
	if !bytes.Equal(samples[0].Payload, firstPayload) {
		t.Fatal("decoded sample payload aliases the caller's media segment")
	}
}

func TestDecodeSTPPSamplesRejectsPayloadOutsideMdat(t *testing.T) {
	t.Parallel()

	payload := []byte(`<tt/>`)
	raw := makeSTPPSegment(t, []mp4.FullSample{{
		Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(payload) + 20)},
		DecodeTime: 0,
		Data:       payload,
	}})
	if _, err := DecodeSTPPSamples(raw, STPPTrack{ID: 7, Timescale: 1000}); err == nil {
		t.Fatal("DecodeSTPPSamples unexpectedly accepted a sample outside mdat")
	}
}

func TestDecodeSTPPSamplesRejectsInvalidTrackDescription(t *testing.T) {
	t.Parallel()

	if _, err := DecodeSTPPSamples(nil, STPPTrack{}); err == nil {
		t.Fatal("DecodeSTPPSamples unexpectedly accepted an invalid track")
	}
}

func makeSTPPSegment(t *testing.T, samples []mp4.FullSample) []byte {
	t.Helper()
	fragment, err := mp4.CreateFragment(1, 7)
	if err != nil {
		t.Fatalf("CreateFragment: %v", err)
	}
	for _, sample := range samples {
		fragment.AddFullSample(sample)
	}
	segment := mp4.NewMediaSegment()
	segment.AddFragment(fragment)
	var encoded bytes.Buffer
	if err := segment.Encode(&encoded); err != nil {
		t.Fatalf("encode media segment: %v", err)
	}
	return encoded.Bytes()
}

func assertSTPPSample(t *testing.T, sample STPPSample, decodeTime, start, duration time.Duration, payload []byte) {
	t.Helper()
	if sample.DecodeTime != decodeTime || sample.Start != start || sample.Duration != duration || sample.End != start+duration {
		t.Fatalf("sample timing = decode %v, [%v, %v), duration %v", sample.DecodeTime, sample.Start, sample.End, sample.Duration)
	}
	if !bytes.Equal(sample.Payload, payload) {
		t.Fatalf("sample payload = %q, want %q", sample.Payload, payload)
	}
}
