package packager

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

type subtitleLiveOrigin struct {
	*liveOrigin
	init    []byte
	segment []byte
	fetches atomic.Int64
}

func (o *subtitleLiveOrigin) manifest() []byte {
	base := o.liveOrigin.manifest()
	base = bytes.Replace(base, []byte(`minimumUpdatePeriod="PT1S"`), []byte(`minimumUpdatePeriod="PT1H"`), 1)
	text := fmt.Sprintf(`
    <AdaptationSet contentType="text" mimeType="application/mp4" codecs="stpp" lang="zh">
      <SegmentTemplate timescale="%d" initialization="s/init.m4s" media="s/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="%d" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="s0" bandwidth="10000"/>
    </AdaptationSet>`, liveTimescale, o.timelineStart, liveSegTicks, o.videoCount-1)
	return bytes.Replace(base, []byte("\n  </Period>"), append([]byte(text), []byte("\n  </Period>")...), 1)
}

func (o *subtitleLiveOrigin) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	switch {
	case strings.Contains(rawURL, "stream.mpd"):
		return o.manifest(), rawURL, nil
	case strings.Contains(rawURL, "/s/init.m4s"):
		return bytes.Clone(o.init), rawURL, nil
	case strings.Contains(rawURL, "/s/seg-"):
		o.fetches.Add(1)
		return bytes.Clone(o.segment), rawURL, nil
	default:
		return o.liveOrigin.Fetch(ctx, rawURL)
	}
}

func TestDelayedDynamicSubtitleStartsAtLiveWindow(t *testing.T) {
	base := newLiveOrigin(t)
	base.videoCount = 12
	base.audioCount = 12
	initBytes, trackID := nativeSTPPInit(t)
	origin := &subtitleLiveOrigin{
		liveOrigin: base,
		init:       initBytes,
		segment:    nativeSTPPSegment(t, trackID, []byte(`<tt xmlns="http://www.w3.org/ns/ttml"><body><div><p begin="0s" dur="1s">Live</p></div></body></tt>`)),
	}
	clock := newClock()
	clock.advance(18 * time.Second)
	job, err := StartNative(context.Background(), Options{
		ManifestURL:   "https://origin.example.com/live/stream.mpd",
		Dir:           t.TempDir(),
		Keys:          keys(t),
		Fetcher:       origin,
		StartSegments: 3,
		PlaylistSize:  8,
		ReanchorAfter: 30 * time.Second,
		Now:           clock.now,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = job.Stop() })

	if _, ok := job.Publication().Playlist(hls.MasterName); !ok {
		t.Fatal("master playlist waited for the optional subtitle")
	}

	presentation, err := mpd.Parse(origin.manifest(), "https://origin.example.com/live/stream.mpd")
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if err := job.advance(context.Background(), presentation); err != nil {
		t.Fatalf("advance: %v", err)
	}
	playlist, ok := job.Publication().Playlist("subtitle-zh-hant.m3u8")
	if !ok {
		t.Fatal("subtitle playlist was not published")
	}
	if got := strings.Count(string(playlist), ".vtt"); got != 3 {
		t.Fatalf("first subtitle playlist has %d segments, want the 3-segment live window:\n%s", got, playlist)
	}
	if got := origin.fetches.Load(); got != 3 {
		t.Fatalf("fetched %d historical subtitle segments, want 3", got)
	}
}

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
