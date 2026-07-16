package session_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/session"
)

func BenchmarkManagerTouchActivePublication(b *testing.B) {
	const channelID = "news"
	cfg := config.File{
		Channels: []config.Channel{{
			ID:        channelID,
			SourceURL: "https://example.test/live.mpd",
			Ingress:   "dash",
			Keys:      "00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100",
		}},
	}
	manager := session.NewManager(
		catalog.New(cfg, nil),
		nil,
		observe.New(),
		b.TempDir(),
		config.FFmpeg{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)
	job := newFakeJob()
	job.stats = packager.Stats{
		SegmentsPublished: 10,
		CacheBytes:        1 << 20,
		CacheItems:        8,
	}
	manager.SetPackager(&fakePackager{results: []startResult{{job: job}}})
	if _, err := manager.Acquire(channelID); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { manager.StopChannel(channelID) })

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		manager.Touch(channelID)
	}
}
