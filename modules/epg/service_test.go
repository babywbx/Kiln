package epg_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/epg"
)

func TestServiceRefreshFiltersRewritesAndCompressesXMLTV(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv><channel id="368359"><display-name lang="zh">無綫新聞台</display-name><icon src=""/></channel>` +
		`<channel id="other"><display-name>Other</display-name></channel>` +
		`<programme channel="368359" start="20260713000000 +0000"><title>Morning</title></programme>` +
		`<programme channel="other" start="20260713000000 +0000"><title>Other</title></programme></tv>`)
	fetcher := &fakeSourceFetcher{results: map[string]epg.FetchResult{"hk": {Data: raw}}}
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "hk", URL: "https://example.test/epg.xml.gz", Timezone: "Asia/Hong_Kong"}},
	}, fetcher, epg.NewMemoryStore())
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	channels := []epg.ChannelRef{{ID: "demo-news", Title: "無綫新聞台", EPGID: "368359"}}
	document := service.Document(channels)
	if len(document.Channels) != 1 || document.Channels[0].ID != "demo-news" {
		t.Fatalf("channels = %+v", document.Channels)
	}
	if len(document.Programmes) != 1 || document.Programmes[0].Channel != "demo-news" {
		t.Fatalf("programmes = %+v", document.Programmes)
	}
	wantLogo := epg.LogoCandidates("無綫新聞台")[0].URL
	if got := document.Channels[0].Icons[0].Src; got != wantLogo {
		t.Fatalf("logo = %q, want %q", got, wantLogo)
	}

	compressed, err := service.GzipXML(channels)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := epg.Parse(bytes.NewReader(decoded), "Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Channels) != 1 || parsed.Channels[0].ID != "demo-news" {
		t.Fatalf("gzip XML channels = %+v", parsed.Channels)
	}
}

func TestServiceFallsBackToCachedDocumentWhenRefreshFails(t *testing.T) {
	t.Parallel()

	store := epg.NewMemoryStore()
	raw := []byte(`<tv><channel id="368366"><display-name>翡翠台</display-name></channel>` +
		`<programme channel="368366" start="20260713080000 +0800"><title>News</title></programme></tv>`)
	if err := store.Save(epg.CacheEntry{
		SourceID: "hk", Data: raw, Metadata: epg.CacheMetadata{ETag: `"cached"`},
	}); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeSourceFetcher{errors: map[string]error{"hk": errors.New("offline")}}
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "hk", Timezone: "Asia/Hong_Kong"}},
	}, fetcher, store)
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded, want upstream error for logging")
	}

	xmlData, err := service.XML([]epg.ChannelRef{{ID: "jade", EPGID: "368366"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xmlData), `channel id="jade"`) || !strings.Contains(string(xmlData), `programme start="20260713080000 +0800" channel="jade"`) {
		t.Fatalf("cached XML was not served and rewritten:\n%s", xmlData)
	}
	if got := fetcher.previousFor("hk").ETag; got != `"cached"` {
		t.Fatalf("fetch validator = %q, want cached ETag", got)
	}
	statuses := service.Statuses()
	if len(statuses) != 1 || !statuses[0].Stale || statuses[0].Error == "" {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestServiceReturnsLegalEmptyTVWithoutSources(t *testing.T) {
	t.Parallel()

	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "offline", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{errors: map[string]error{"offline": errors.New("offline")}}, nil)
	_ = service.Refresh(context.Background())
	raw, err := service.XML([]epg.ChannelRef{{ID: "one", EPGID: "missing"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := epg.Parse(bytes.NewReader(raw), "Asia/Hong_Kong"); err != nil {
		t.Fatalf("empty XML is invalid: %v\n%s", err, raw)
	}
}

func TestServiceKeepsCachedDocumentWhenFreshXMLIsMalformed(t *testing.T) {
	t.Parallel()

	store := epg.NewMemoryStore()
	if err := store.Save(epg.CacheEntry{
		SourceID: "hk",
		Data:     []byte(`<tv><channel id="368366"><display-name>翡翠台</display-name></channel></tv>`),
	}); err != nil {
		t.Fatal(err)
	}
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "hk", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"hk": {Data: []byte(`<tv><broken>`)}}}, store)
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded with malformed fresh XML")
	}
	document := service.Document([]epg.ChannelRef{{ID: "jade", EPGID: "368366"}})
	if len(document.Channels) != 1 || document.Channels[0].ID != "jade" {
		t.Fatalf("cached channel was not retained: %+v", document.Channels)
	}
}

func TestServiceRefreshIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"source": {Data: raw}}}, epg.NewMemoryStore())

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = service.Refresh(context.Background())
			_ = service.Document([]epg.ChannelRef{{ID: "kiln", EPGID: "one"}})
		}()
	}
	wait.Wait()
	if got := len(service.Document([]epg.ChannelRef{{ID: "kiln", EPGID: "one"}}).Channels); got != 1 {
		t.Fatalf("channel count = %d, want 1", got)
	}
}

func TestServiceRunRefreshesImmediatelyAndOnInterval(t *testing.T) {
	t.Parallel()

	called := make(chan struct{}, 4)
	fetcher := sourceFetcherFunc(func(_ context.Context, _ epg.Source, _ epg.CacheMetadata) (epg.FetchResult, error) {
		called <- struct{}{}
		return epg.FetchResult{Data: []byte(`<tv></tv>`)}, nil
	})
	service := epg.NewService(epg.ServiceConfig{
		Sources:         []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
		RefreshInterval: 5 * time.Millisecond,
	}, fetcher, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	for index := range 2 {
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for refresh %d", index+1)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestServiceSetSourcesPreservesRetainedStateAndRemovesDeletedSources(t *testing.T) {
	t.Parallel()

	fetcher := &fakeSourceFetcher{results: map[string]epg.FetchResult{
		"old": {Data: []byte(`<tv><channel id="one"><display-name>One</display-name></channel></tv>`)},
		"new": {Data: []byte(`<tv><channel id="two"><display-name>Two</display-name></channel></tv>`)},
	}}
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "old", Name: "Old", Timezone: "Asia/Hong_Kong"}},
	}, fetcher, epg.NewMemoryStore())
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	updated := []epg.Source{
		{ID: "old", Name: "Retained", Timezone: "Asia/Hong_Kong"},
		{ID: "new", Name: "New", Timezone: "Asia/Hong_Kong"},
	}
	service.SetSources(updated)
	updated[0].Name = "mutated"
	if got := service.Sources()[0].Name; got != "Retained" {
		t.Fatalf("Sources exposed mutable input: %q", got)
	}
	if got := len(service.Document([]epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}).Channels); got != 1 {
		t.Fatalf("retained source lost its snapshot: %d channels", got)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(service.Document([]epg.ChannelRef{{ID: "kiln-two", EPGID: "two"}}).Channels); got != 1 {
		t.Fatalf("new source was not refreshed: %d channels", got)
	}

	service.SetSources([]epg.Source{{ID: "new", Name: "New", Timezone: "Asia/Hong_Kong"}})
	if got := len(service.Document([]epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}).Channels); got != 0 {
		t.Fatalf("deleted source is still published: %d channels", got)
	}
	statuses := service.Statuses()
	if len(statuses) != 1 || statuses[0].SourceID != "new" {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestServiceSetSourcesAndRefreshAreConcurrencySafe(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel></tv>`)
	service := epg.NewService(epg.ServiceConfig{}, &fakeSourceFetcher{
		results: map[string]epg.FetchResult{"a": {Data: raw}, "b": {Data: raw}},
	}, epg.NewMemoryStore())
	var wait sync.WaitGroup
	for index := range 20 {
		wait.Add(2)
		go func(index int) {
			defer wait.Done()
			id := "a"
			if index%2 == 1 {
				id = "b"
			}
			service.SetSources([]epg.Source{{ID: id, Timezone: "Asia/Hong_Kong"}})
		}(index)
		go func() {
			defer wait.Done()
			_ = service.Refresh(context.Background())
		}()
	}
	wait.Wait()
	if got := len(service.Sources()); got != 1 {
		t.Fatalf("source count = %d, want 1", got)
	}
}

type fakeSourceFetcher struct {
	mu       sync.Mutex
	results  map[string]epg.FetchResult
	errors   map[string]error
	previous map[string]epg.CacheMetadata
}

func (f *fakeSourceFetcher) Fetch(_ context.Context, source epg.Source, previous epg.CacheMetadata) (epg.FetchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.previous == nil {
		f.previous = make(map[string]epg.CacheMetadata)
	}
	f.previous[source.ID] = previous
	if err := f.errors[source.ID]; err != nil {
		return epg.FetchResult{}, err
	}
	return f.results[source.ID], nil
}

func (f *fakeSourceFetcher) previousFor(sourceID string) epg.CacheMetadata {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.previous[sourceID]
}

type sourceFetcherFunc func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error)

func (f sourceFetcherFunc) Fetch(ctx context.Context, source epg.Source, metadata epg.CacheMetadata) (epg.FetchResult, error) {
	return f(ctx, source, metadata)
}
