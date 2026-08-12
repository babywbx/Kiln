package epg_test

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

var (
	benchmarkEPGDocument *epg.Document
	benchmarkEPGPayload  []byte
)

func BenchmarkServiceOutput(b *testing.B) {
	service, channels := benchmarkEPGService(b, 32, 96)
	benchmarks := []struct {
		name string
		run  func() error
	}{
		{
			name: "Document",
			run: func() error {
				benchmarkEPGDocument = service.Document(channels)
				return nil
			},
		},
		{
			name: "XML",
			run: func() error {
				var err error
				benchmarkEPGPayload, err = service.XML(channels)
				return err
			},
		},
		{
			name: "GzipXML",
			run: func() error {
				var err error
				benchmarkEPGPayload, err = service.GzipXML(channels)
				return err
			},
		},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := benchmark.run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	coldChannels := slices.Clone(channels)
	slices.Reverse(coldChannels)
	b.Run("GzipXMLCold", func(b *testing.B) {
		b.ReportAllocs()
		useColdChannels := false
		for b.Loop() {
			current := channels
			if useColdChannels {
				current = coldChannels
			}
			var err error
			benchmarkEPGPayload, err = service.GzipXML(current)
			if err != nil {
				b.Fatal(err)
			}
			useColdChannels = !useColdChannels
		}
	})
}

func BenchmarkServiceRefreshUnchanged(b *testing.B) {
	data := makeXMLTVFixture(32, 512)
	for _, benchmark := range []struct {
		name        string
		notModified bool
	}{
		{name: "IdenticalBody"},
		{name: "NotModified", notModified: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			fetcher := &benchmarkRefreshFetcher{data: data}
			service := epg.NewService(epg.ServiceConfig{
				Sources: []epg.Source{{ID: "benchmark", Timezone: "Asia/Hong_Kong"}},
			}, fetcher, newTestStore(b))
			if err := service.Refresh(context.Background()); err != nil {
				b.Fatal(err)
			}
			fetcher.notModified = benchmark.notModified

			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				if err := service.Refresh(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkEPGService(b *testing.B, channelCount, programmesPerChannel int) (*epg.Service, []epg.ChannelRef) {
	b.Helper()
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "benchmark", Timezone: "Asia/Hong_Kong"}},
	}, benchmarkSourceFetcher{data: makeXMLTVFixture(channelCount, programmesPerChannel)}, nil)
	if err := service.Refresh(context.Background()); err != nil {
		b.Fatal(err)
	}
	channels := make([]epg.ChannelRef, channelCount)
	for index := range channelCount {
		channels[index] = epg.ChannelRef{
			ID:    "kiln-channel-" + strconv.Itoa(index),
			EPGID: "channel-" + strconv.Itoa(index),
		}
	}
	return service, channels
}

type benchmarkSourceFetcher struct {
	data []byte
}

func (f benchmarkSourceFetcher) Fetch(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
	return epg.FetchResult{Data: f.data}, nil
}

type benchmarkRefreshFetcher struct {
	data        []byte
	notModified bool
}

func (f *benchmarkRefreshFetcher) Fetch(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
	return epg.FetchResult{Data: f.data, NotModified: f.notModified}, nil
}
