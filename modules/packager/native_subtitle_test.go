package packager

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

func TestNativeConvertsSTPPFragmentToPublishedWebVTT(t *testing.T) {
	t.Parallel()

	initBytes, trackID := nativeSTPPInit(t)
	init, err := cmaf.ParseInit(initBytes)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	payload := []byte(`<tt xmlns="http://www.w3.org/ns/ttml"><body><div><p begin="0s" dur="1.5s">Hello &amp; 世界</p></div></body></tt>`)
	fragment := nativeSTPPSegment(t, trackID, payload)
	publisher, err := hls.New(hls.Config{Dir: t.TempDir(), PlaylistSize: 4})
	if err != nil {
		t.Fatalf("hls.New: %v", err)
	}
	representation := mpd.Representation{
		ID: "sub-zh", Type: mpd.TypeText, Codecs: "stpp", Lang: "zh",
		Addressing: mpd.Addressing{Mode: mpd.AddressingTemplateDuration, Timescale: 1000, Duration: 2000},
	}
	state := newTrackState("subtitle-zh-hant", representation, init, time.Now())
	if err := publisher.AddTrack(hls.Track{Name: state.name, Kind: hls.KindSubtitle, Lang: "zh-Hant"}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	gate := newByteGate(8 << 20)
	reservation, err := gate.acquire(context.Background(), int64(len(fragment)))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	native := &Native{
		opts: Options{Keys: cmaf.KeySet{}, DecryptPool: make(chan struct{}, 1), MaxSegmentBytes: 1 << 20},
		now:  time.Now, log: slog.Default(), pub: publisher, gate: gate, decrypt: make(chan struct{}, 1),
		texts: []*trackState{state},
	}
	segment := mpd.Segment{Number: 1, Time: 0, Duration: 2000}
	prepared := native.prepareText(context.Background(), state, segment, fragment, reservation)
	if prepared.err != nil {
		t.Fatalf("prepareText: %v", prepared.err)
	}
	if err := native.commit(state, segment, prepared); err != nil {
		t.Fatalf("commit: %v", err)
	}

	playlist, ok := publisher.Playlist("subtitle-zh-hant.m3u8")
	if !ok || !strings.Contains(string(playlist), "subtitle-zh-hant-000001.vtt") {
		t.Fatalf("subtitle playlist = %q, %v", playlist, ok)
	}
	path, ok := publisher.Asset("subtitle-zh-hant-000001.vtt")
	if !ok {
		t.Fatal("WebVTT asset missing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read WebVTT: %v", err)
	}
	for _, want := range []string{"WEBVTT", "X-TIMESTAMP-MAP", "Hello &amp; 世界"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("WebVTT missing %q:\n%s", want, data)
		}
	}
}

func nativeSTPPInit(t *testing.T) ([]byte, uint32) {
	t.Helper()
	init := mp4.CreateEmptyInit()
	track := init.AddEmptyTrack(1000, "stpp", "zh")
	if err := track.SetStppDescriptor("http://www.w3.org/ns/ttml", "", ""); err != nil {
		t.Fatalf("SetStppDescriptor: %v", err)
	}
	var encoded bytes.Buffer
	if err := init.Encode(&encoded); err != nil {
		t.Fatalf("encode init: %v", err)
	}
	return encoded.Bytes(), track.Tkhd.TrackID
}

func nativeSTPPSegment(t *testing.T, trackID uint32, payload []byte) []byte {
	t.Helper()
	fragment, err := mp4.CreateFragment(1, trackID)
	if err != nil {
		t.Fatalf("CreateFragment: %v", err)
	}
	fragment.AddFullSample(mp4.FullSample{
		Sample: mp4.Sample{Dur: 2000, Size: uint32(len(payload))},
		Data:   payload,
	})
	segment := mp4.NewMediaSegment()
	segment.AddFragment(fragment)
	var encoded bytes.Buffer
	if err := segment.Encode(&encoded); err != nil {
		t.Fatalf("encode segment: %v", err)
	}
	return encoded.Bytes()
}
