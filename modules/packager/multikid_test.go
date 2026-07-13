package packager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/packager/cmaf"
)

const (
	multiVideoKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	multiVideoKID = "11111111111111111111111111111111"
	multiAudioKey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	multiAudioKID = "22222222222222222222222222222222"
)

func multiKeys(t *testing.T) cmaf.KeySet {
	t.Helper()
	ks, err := cmaf.NewKeySet(map[string]string{
		multiVideoKID: multiVideoKey,
		multiAudioKID: multiAudioKey,
	})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	return ks
}

func multiKeyPairs() []config.KeyPair {
	return []config.KeyPair{
		{KID: multiVideoKID, Key: multiVideoKey},
		{KID: multiAudioKID, Key: multiAudioKey},
	}
}

type breakSegments struct{ inner Fetcher }

func (f *breakSegments) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	if strings.Contains(url, "chunk-") {
		return nil, "", errors.New("upstream is gone")
	}
	return f.inner.Fetch(ctx, url)
}

type spyPackager struct{ started bool }

func (p *spyPackager) Start(context.Context, Request) (Job, error) {
	p.started = true
	return nil, errors.New("spy ffmpeg")
}

func TestNativeServesMultiKID(t *testing.T) {
	origin := startOrigin(t, "multikid")
	job, err := StartNative(context.Background(), Options{
		ManifestURL:   origin.URL + "/stream.mpd",
		Dir:           t.TempDir(),
		Keys:          multiKeys(t),
		Fetcher:       &httpFetcher{client: origin.Client(), hits: map[string]int{}},
		StartSegments: 1,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	defer func() { _ = job.Stop() }()

	waitForStaticCompletion(t, job)
	if err := job.Err(); err != nil {
		t.Fatalf("job error: %v", err)
	}
	if job.plan.FallbackAllowed {
		t.Error("a multi-KID plan still allows falling back to ffmpeg")
	}
	if s := job.Stats(); s.SegmentsPublished == 0 || s.KeyMismatches != 0 {
		t.Errorf("stats = %+v, want segments published and no key mismatch", s)
	}
}

func TestMultiKIDStartFailureDoesNotFallBackToFFmpeg(t *testing.T) {
	origin := startOrigin(t, "multikid")
	spy := &spyPackager{}
	native := NewNativeAdapter(func(Request) Fetcher {
		return &breakSegments{inner: &httpFetcher{client: origin.Client(), hits: map[string]int{}}}
	}, 8, time.Minute)
	native.StartSegments = 1

	pkg := NewAdaptivePackager(native, spy, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := pkg.Start(context.Background(), Request{
		ChannelID: "multikid",
		SourceURL: origin.URL + "/stream.mpd",
		Keys:      multiKeyPairs(),
		WorkDir:   t.TempDir(),
		Engine:    StrategyAuto,
	})
	if err == nil {
		t.Fatal("expected the start to fail")
	}
	if spy.started {
		t.Fatal("a multi-KID source was handed to ffmpeg, which would decode it incorrectly")
	}
}

func TestSingleKIDStartFailureFallsBackToFFmpeg(t *testing.T) {
	origin := startOrigin(t, "h264")
	spy := &spyPackager{}
	native := NewNativeAdapter(func(Request) Fetcher {
		return &breakSegments{inner: &httpFetcher{client: origin.Client(), hits: map[string]int{}}}
	}, 8, time.Minute)
	native.StartSegments = 1

	pkg := NewAdaptivePackager(native, spy, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := pkg.Start(context.Background(), Request{
		ChannelID: "h264",
		SourceURL: origin.URL + "/stream.mpd",
		Keys:      []config.KeyPair{{KID: fixtureKID, Key: fixtureKey}},
		WorkDir:   t.TempDir(),
		Engine:    StrategyAuto,
	})
	if err == nil {
		t.Fatal("expected the spy ffmpeg to fail the start")
	}
	if !spy.started {
		t.Fatal("a plain fetch failure on a single-KID source should still try ffmpeg")
	}
}
