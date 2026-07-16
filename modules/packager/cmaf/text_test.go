package cmaf

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"
)

func TestParseInitAndDecodeClearSTPP(t *testing.T) {
	t.Parallel()

	initRaw, trackID := makeSTPPInit(t)
	init, err := ParseInit(initRaw)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	if init.Track.Kind != KindText || init.Track.Codec != "stpp" {
		t.Fatalf("track = kind %q codec %q, want text/stpp", init.Track.Kind, init.Track.Codec)
	}
	if init.Track.ID != trackID || init.Track.Timescale != 1000 || init.Track.Encrypted {
		t.Fatalf("track = %#v", init.Track)
	}

	first := []byte(`<tt><body><p begin="5.25s" end="6.25s">first</p></body></tt>`)
	second := []byte(`<tt><body><p begin="5.9s" end="6.9s">second</p></body></tt>`)
	segmentRaw := makeTextSegment(t, trackID, nil, []mp4.FullSample{
		{
			Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(first)), CompositionTimeOffset: 250},
			DecodeTime: 5000,
			Data:       first,
		},
		{
			Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(second)), CompositionTimeOffset: -100},
			DecodeTime: 6000,
			Data:       second,
		},
	})
	decoded, err := init.DecodeText(segmentRaw, nil)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if decoded.Timescale != 1000 || decoded.BaseTime != 5000 || decoded.Duration != 2000 || len(decoded.Samples) != 2 {
		t.Fatalf("decoded text segment = %#v", decoded)
	}
	assertTextSample(t, decoded.Samples[0], 5000, 5250, 1000, first)
	assertTextSample(t, decoded.Samples[1], 6000, 5900, 1000, second)

	for index := range segmentRaw {
		segmentRaw[index] = 0
	}
	if !bytes.Equal(decoded.Samples[0].Payload, first) {
		t.Fatal("DecodeText payload aliases the caller's segment")
	}
}

func TestDecryptExposesAndPreservesEventMessages(t *testing.T) {
	t.Parallel()

	initRaw, trackID := makeSTPPInit(t)
	init, err := ParseInit(initRaw)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	payload := []byte(`<tt/>`)
	events := []*mp4.EmsgBox{
		{
			Version:               0,
			TimeScale:             1000,
			PresentationTimeDelta: 250,
			EventDuration:         500,
			ID:                    7,
			SchemeIDURI:           "urn:scte:scte35:2013:bin",
			Value:                 "splice",
			MessageData:           []byte{0xfc, 0x30, 0x01},
		},
		{
			Version:          1,
			TimeScale:        90000,
			PresentationTime: 450000,
			EventDuration:    9000,
			ID:               8,
			SchemeIDURI:      "https://aomedia.org/emsg/ID3",
			Value:            "id3",
			MessageData:      []byte("metadata"),
		},
	}
	raw := makeTextSegment(t, trackID, events, []mp4.FullSample{{
		Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(payload))},
		DecodeTime: 5000,
		Data:       payload,
	}})

	segment, err := init.Decrypt(raw, nil)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	wantEvents := []EventMessage{
		{
			Version:               0,
			Timescale:             1000,
			PresentationTimeDelta: 250,
			EventDuration:         500,
			ID:                    7,
			SchemeIDURI:           "urn:scte:scte35:2013:bin",
			Value:                 "splice",
			MessageData:           []byte{0xfc, 0x30, 0x01},
		},
		{
			Version:          1,
			Timescale:        90000,
			PresentationTime: 450000,
			EventDuration:    9000,
			ID:               8,
			SchemeIDURI:      "https://aomedia.org/emsg/ID3",
			Value:            "id3",
			MessageData:      []byte("metadata"),
		},
	}
	assertEventMessages(t, segment.Events, wantEvents)

	decodedText, err := init.DecodeText(raw, nil)
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	assertEventMessages(t, decodedText.Events, wantEvents)

	reparsed, err := mp4.DecodeFile(bytes.NewReader(segment.Clear))
	if err != nil {
		t.Fatalf("decode clear segment: %v", err)
	}
	if got := len(reparsed.Segments[0].Fragments[0].Emsgs); got != 2 {
		t.Fatalf("clear segment contains %d emsg boxes, want 2", got)
	}
}

func TestDecodeEncryptedSTPP(t *testing.T) {
	t.Parallel()

	payload := []byte(`<tt><body><p begin="1s" end="2s">encrypted</p></body></tt>`)
	initRaw, segmentRaw := makeEncryptedSTPP(t, payload)
	init, err := ParseInit(initRaw)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	if init.Track.Kind != KindText || !init.Track.Encrypted || init.Track.Scheme != "cenc" || init.Track.KID != fixtureKID {
		t.Fatalf("encrypted text track = %#v", init.Track)
	}
	if _, err := init.DecodeText(segmentRaw, nil); err == nil {
		t.Fatal("DecodeText unexpectedly decrypted stpp without its key")
	} else if unsupported, ok := Unsupported(err); !ok || unsupported.Reason != ReasonMissingKey {
		t.Fatalf("DecodeText missing-key error = %v", err)
	}
	for _, box := range []string{"sinf", "schm", "tenc"} {
		if bytes.Contains(init.Clear, []byte(box)) {
			t.Fatalf("clear text init still contains %s", box)
		}
	}
	decoded, err := init.DecodeText(segmentRaw, testKeys(t))
	if err != nil {
		t.Fatalf("DecodeText: %v", err)
	}
	if len(decoded.Samples) != 1 || !bytes.Equal(decoded.Samples[0].Payload, payload) {
		t.Fatalf("decoded encrypted payload = %#v", decoded.Samples)
	}
}

func TestDecodeTextContainsMalformedSampleBoundaryPanic(t *testing.T) {
	t.Parallel()

	initRaw, trackID := makeSTPPInit(t)
	init, err := ParseInit(initRaw)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	payload := []byte(`<tt/>`)
	raw := makeTextSegment(t, trackID, nil, []mp4.FullSample{{
		Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(payload) + 64)},
		DecodeTime: 0,
		Data:       payload,
	}})
	_, err = init.DecodeText(raw, nil)
	unsupported, ok := Unsupported(err)
	if !ok || unsupported.Reason != ReasonMalformed {
		t.Fatalf("DecodeText malformed boundary error = %v, want %s", err, ReasonMalformed)
	}
}

func TestDecodeTextRejectsTruncatedSegments(t *testing.T) {
	t.Parallel()

	initRaw, trackID := makeSTPPInit(t)
	init, err := ParseInit(initRaw)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	payload := []byte(`<tt/>`)
	raw := makeTextSegment(t, trackID, nil, []mp4.FullSample{{
		Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(payload))},
		DecodeTime: 0,
		Data:       payload,
	}})
	for _, length := range []int{0, len(raw) / 2, len(raw) - 1} {
		if _, err := init.DecodeText(raw[:length], nil); err == nil {
			t.Errorf("DecodeText accepted segment truncated to %d bytes", length)
		}
	}
}

func TestParseInitRejectsMalformedSTPPProtectionWithoutPanic(t *testing.T) {
	t.Parallel()

	initRaw, _ := makeEncryptedSTPP(t, []byte(`<tt/>`))
	decoded, err := mp4.DecodeFile(bytes.NewReader(initRaw))
	if err != nil {
		t.Fatalf("decode encrypted stpp init: %v", err)
	}
	stpp := decoded.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Stpp
	var protection *mp4.SinfBox
	for _, child := range stpp.Children {
		if sinf, ok := child.(*mp4.SinfBox); ok {
			protection = sinf
			break
		}
	}
	if protection == nil || protection.Schi == nil {
		t.Fatal("encrypted stpp fixture has no protection")
	}
	protection.Schi.Children = dropChild(protection.Schi.Children, "tenc")
	protection.Schi.Tenc = nil
	var malformed bytes.Buffer
	if err := decoded.Init.Encode(&malformed); err != nil {
		t.Fatalf("encode malformed stpp init: %v", err)
	}
	_, err = ParseInit(malformed.Bytes())
	unsupported, ok := Unsupported(err)
	if !ok || unsupported.Reason != ReasonMissingKID {
		t.Fatalf("ParseInit malformed stpp error = %v, want %s", err, ReasonMissingKID)
	}
}

func makeSTPPInit(t *testing.T) ([]byte, uint32) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(1000, "stpp", "zh")
	if err := trak.SetStppDescriptor("http://www.w3.org/ns/ttml", "", ""); err != nil {
		t.Fatalf("SetStppDescriptor: %v", err)
	}
	var encoded bytes.Buffer
	if err := init.Encode(&encoded); err != nil {
		t.Fatalf("encode stpp init: %v", err)
	}
	return encoded.Bytes(), trak.Tkhd.TrackID
}

func makeTextSegment(t *testing.T, trackID uint32, events []*mp4.EmsgBox, samples []mp4.FullSample) []byte {
	t.Helper()
	fragment, err := mp4.CreateFragment(1, trackID)
	if err != nil {
		t.Fatalf("CreateFragment: %v", err)
	}
	for _, event := range events {
		fragment.AddEmsg(event)
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

func makeEncryptedSTPP(t *testing.T, payload []byte) ([]byte, []byte) {
	t.Helper()
	clearInit, trackID := makeSTPPInit(t)
	decodedInit, err := mp4.DecodeFile(bytes.NewReader(clearInit))
	if err != nil {
		t.Fatalf("decode clear stpp init: %v", err)
	}
	stpp := decodedInit.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Stpp
	if stpp == nil {
		t.Fatal("clear init has no stpp sample entry")
	}
	kid, err := hex.DecodeString(fixtureKID)
	if err != nil {
		t.Fatalf("decode fixture KID: %v", err)
	}
	tenc := &mp4.TencBox{
		Version:                0,
		DefaultIsProtected:     1,
		DefaultPerSampleIVSize: 8,
		DefaultKID:             mp4.UUID(kid),
	}
	sinf := &mp4.SinfBox{}
	sinf.AddChild(&mp4.FrmaBox{DataFormat: "stpp"})
	sinf.AddChild(&mp4.SchmBox{SchemeType: "cenc", SchemeVersion: 65536})
	schi := &mp4.SchiBox{}
	schi.AddChild(tenc)
	sinf.AddChild(schi)
	stpp.AddChild(sinf)
	var encodedInit bytes.Buffer
	if err := decodedInit.Init.Encode(&encodedInit); err != nil {
		t.Fatalf("encode encrypted stpp init: %v", err)
	}

	fragment, err := mp4.CreateFragment(1, trackID)
	if err != nil {
		t.Fatalf("CreateFragment: %v", err)
	}
	fragment.AddFullSample(mp4.FullSample{
		Sample:     mp4.Sample{Dur: 1000, Size: uint32(len(payload))},
		DecodeTime: 1000,
		Data:       bytes.Clone(payload),
	})
	key, err := hex.DecodeString(fixtureKey)
	if err != nil {
		t.Fatalf("decode fixture key: %v", err)
	}
	iv := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	// Reuse mp4ff's public full-sample protector for the encrypted text fixture.
	protectionInit := mp4.CreateEmptyInit()
	protectionTrack := protectionInit.AddEmptyTrack(1000, "audio", "und")
	if err := protectionTrack.SetAACDescriptor(aac.AAClc, 1000); err != nil {
		t.Fatalf("SetAACDescriptor: %v", err)
	}
	protection, err := mp4.InitProtect(
		protectionInit,
		key,
		iv,
		"cenc",
		mp4.UUID(kid),
		nil,
	)
	if err != nil {
		t.Fatalf("InitProtect: %v", err)
	}
	protection.Tenc = tenc
	protection.Trex = decodedInit.Init.Moov.Mvex.Trex
	if _, err := mp4.EncryptFragment(fragment, key, iv, protection); err != nil {
		t.Fatalf("EncryptFragment: %v", err)
	}
	segment := mp4.NewMediaSegment()
	segment.AddFragment(fragment)
	var encodedSegment bytes.Buffer
	if err := segment.Encode(&encodedSegment); err != nil {
		t.Fatalf("encode encrypted stpp segment: %v", err)
	}
	return encodedInit.Bytes(), encodedSegment.Bytes()
}

func assertTextSample(t *testing.T, sample TextSample, decodeTime uint64, presentationTime int64, duration uint32, payload []byte) {
	t.Helper()
	if sample.DecodeTime != decodeTime || sample.PresentationTime != presentationTime || sample.Duration != duration {
		t.Fatalf("sample timing = decode %d presentation %d duration %d", sample.DecodeTime, sample.PresentationTime, sample.Duration)
	}
	if !bytes.Equal(sample.Payload, payload) {
		t.Fatalf("sample payload = %q, want %q", sample.Payload, payload)
	}
}

func assertEventMessages(t *testing.T, got, want []EventMessage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d event messages, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Version != want[index].Version ||
			got[index].Timescale != want[index].Timescale ||
			got[index].PresentationTimeDelta != want[index].PresentationTimeDelta ||
			got[index].PresentationTime != want[index].PresentationTime ||
			got[index].EventDuration != want[index].EventDuration ||
			got[index].ID != want[index].ID ||
			got[index].SchemeIDURI != want[index].SchemeIDURI ||
			got[index].Value != want[index].Value ||
			!bytes.Equal(got[index].MessageData, want[index].MessageData) {
			t.Fatalf("event %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}
