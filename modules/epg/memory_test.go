package epg_test

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strconv"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

type streamingSourceFetcher struct {
	data []byte
}

func (f streamingSourceFetcher) Fetch(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
	return epg.FetchResult{Body: io.NopCloser(bytes.NewReader(f.data))}, nil
}

func liveHeapBytes() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

func TestRefreshKeepsLiveHeapIndependentOfGuideSize(t *testing.T) {
	data := makeXMLTVFixture(240, 200)
	store, err := epg.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "large", Timezone: "Asia/Hong_Kong"}},
	}, streamingSourceFetcher{data: data}, store)

	baseline := liveHeapBytes()
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	retained := int64(liveHeapBytes()) - int64(baseline)
	runtime.KeepAlive(service)

	statuses := service.Statuses()
	if len(statuses) != 1 || statuses[0].ProgrammeCount != 240*200 {
		t.Fatalf("statuses = %+v, want the whole guide ingested", statuses)
	}
	budget := int64(len(data)) / 4
	if retained > budget {
		t.Fatalf("refresh retained %d bytes of heap for a %d byte guide, want at most %d",
			retained, len(data), budget)
	}
	t.Logf("guide %d bytes, retained heap %d bytes", len(data), retained)
}

func TestServingKeepsLiveHeapProportionalToSelectedChannels(t *testing.T) {
	data := makeXMLTVFixture(240, 200)
	store, err := epg.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "large", Timezone: "Asia/Hong_Kong"}},
	}, streamingSourceFetcher{data: data}, store)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	channels := make([]epg.ChannelRef, 0, 12)
	for index := range 12 {
		channels = append(channels, epg.ChannelRef{
			ID: "kiln-" + strconv.Itoa(index), EPGID: "channel-" + strconv.Itoa(index),
		})
	}
	baseline := liveHeapBytes()
	payload, err := service.GzipXML(channels)
	if err != nil {
		t.Fatal(err)
	}
	retained := int64(liveHeapBytes()) - int64(baseline)
	runtime.KeepAlive(service)
	if len(payload) == 0 {
		t.Fatal("gzip payload is empty")
	}
	budget := int64(len(data)) / 8
	if retained > budget {
		t.Fatalf("serving 12 of 240 channels retained %d bytes of heap for a %d byte guide, want at most %d",
			retained, len(data), budget)
	}
	t.Logf("guide %d bytes, payload %d bytes, retained heap %d bytes", len(data), len(payload), retained)
}
