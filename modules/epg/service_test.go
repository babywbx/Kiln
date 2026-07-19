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
	xmlPayload, err := service.XML(channels)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, xmlPayload) {
		t.Fatalf("gzip XML differs from XML:\ngzip: %s\nxml: %s", decoded, xmlPayload)
	}
	parsed, err := epg.Parse(bytes.NewReader(decoded), "Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Channels) != 1 || parsed.Channels[0].ID != "demo-news" {
		t.Fatalf("gzip XML channels = %+v", parsed.Channels)
	}
}

func TestServiceGzipXMLReflectsRefreshAndSourceChanges(t *testing.T) {
	t.Parallel()

	responses := [][]byte{
		[]byte(`<tv><channel id="one"><display-name>One</display-name></channel><programme channel="one" start="20260713080000 +0800"><title>First</title></programme></tv>`),
		[]byte(`<tv><channel id="one"><display-name>One</display-name></channel><programme channel="one" start="20260713080000 +0800"><title>Second</title></programme></tv>`),
	}
	var calls int
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
		response := responses[calls]
		calls++
		return epg.FetchResult{Data: response}, nil
	}), nil)
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := gunzipEPG(t, mustGzipEPG(t, service, channels))
	if !strings.Contains(first, "First") {
		t.Fatalf("initial gzip XML = %s", first)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := gunzipEPG(t, mustGzipEPG(t, service, channels))
	if !strings.Contains(second, "Second") || strings.Contains(second, "First") {
		t.Fatalf("refreshed gzip XML = %s", second)
	}

	service.SetSources([]epg.Source{{ID: "replacement", Timezone: "Asia/Hong_Kong"}})
	empty := gunzipEPG(t, mustGzipEPG(t, service, channels))
	if strings.Contains(empty, `channel id="kiln-one"`) {
		t.Fatalf("removed source remained in gzip XML = %s", empty)
	}
}

func TestServiceGzipXMLKeepsChannelSelectionsIndependent(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel>` +
		`<channel id="two"><display-name>Two</display-name></channel></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"source": {Data: raw}}}, nil)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	one := gunzipEPG(t, mustGzipEPG(t, service, []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}))
	two := gunzipEPG(t, mustGzipEPG(t, service, []epg.ChannelRef{{ID: "kiln-two", EPGID: "two"}}))
	if !strings.Contains(one, `channel id="kiln-one"`) || strings.Contains(one, `channel id="kiln-two"`) {
		t.Fatalf("first channel selection = %s", one)
	}
	if !strings.Contains(two, `channel id="kiln-two"`) || strings.Contains(two, `channel id="kiln-one"`) {
		t.Fatalf("second channel selection = %s", two)
	}
}

func TestServiceGzipXMLReturnsIndependentPayloads(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"source": {Data: raw}}}, nil)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	first := mustGzipEPG(t, service, channels)
	first[0] = 0
	second := mustGzipEPG(t, service, channels)
	if got := gunzipEPG(t, second); !strings.Contains(got, `channel id="kiln-one"`) {
		t.Fatalf("mutating one payload changed another = %s", got)
	}
}

func TestServiceGzipXMLCacheSurvivesNotModifiedRefresh(t *testing.T) {
	metadata := epg.CacheMetadata{ETag: `"guide-v1"`}
	var calls int
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
		calls++
		if calls == 1 {
			return epg.FetchResult{
				Data:     []byte(`<tv><channel id="one"><display-name>One</display-name></channel></tv>`),
				Metadata: metadata,
			}, nil
		}
		return epg.FetchResult{NotModified: true, Metadata: metadata}, nil
	}), epg.NewMemoryStore())
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := mustGzipEPG(t, service, channels)
	refreshAllocations := testing.AllocsPerRun(5, func() {
		if err := service.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	_ = mustGzipEPG(t, service, channels)
	refreshAndGzipAllocations := testing.AllocsPerRun(5, func() {
		if err := service.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := mustGzipEPG(t, service, channels); !bytes.Equal(got, first) {
			t.Fatal("not-modified refresh changed gzip XML")
		}
	})
	if added := refreshAndGzipAllocations - refreshAllocations; added > 2 {
		t.Fatalf("GzipXML added %.0f allocations after not-modified refresh, want at most 2", added)
	}
}

func TestServiceNotModifiedRefreshReparsesChangedTimezone(t *testing.T) {
	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel>` +
		`<programme channel="one" start="20260713080000"><title>Morning</title></programme></tv>`)
	var calls int
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
		calls++
		if calls == 1 {
			return epg.FetchResult{Data: raw}, nil
		}
		return epg.FetchResult{NotModified: true}, nil
	}), epg.NewMemoryStore())
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := service.Document(channels).Programmes[0].Start.Time
	service.SetSources([]epg.Source{{ID: "source", Timezone: "UTC"}})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := service.Document(channels).Programmes[0].Start.Time
	if before.Equal(after) || after.Hour() != 8 {
		t.Fatalf("programme time before = %s, after timezone change = %s", before, after)
	}
}

func TestServiceUnchangedRefreshReusesPublishedDocument(t *testing.T) {
	raw := makeXMLTVFixture(8, 32)
	for _, test := range []struct {
		name   string
		result epg.FetchResult
	}{
		{name: "not modified", result: epg.FetchResult{NotModified: true}},
		{name: "identical body", result: epg.FetchResult{Data: raw}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			service := epg.NewService(epg.ServiceConfig{
				Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
			}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
				calls++
				if calls == 1 {
					return epg.FetchResult{Data: raw}, nil
				}
				return test.result, nil
			}), epg.NewMemoryStore())
			if err := service.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}

			allocations := testing.AllocsPerRun(5, func() {
				if err := service.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
			})
			if allocations > 64 {
				t.Fatalf("unchanged refresh allocations = %.0f, want at most 64", allocations)
			}
		})
	}
}

func TestServiceGzipXMLCacheSurvivesIdenticalRefresh(t *testing.T) {
	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
		return epg.FetchResult{Data: raw}, nil
	}), nil)
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := mustGzipEPG(t, service, channels)
	refreshAllocations := testing.AllocsPerRun(5, func() {
		if err := service.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	_ = mustGzipEPG(t, service, channels)
	refreshAndGzipAllocations := testing.AllocsPerRun(5, func() {
		if err := service.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := mustGzipEPG(t, service, channels); !bytes.Equal(got, first) {
			t.Fatal("identical refresh changed gzip XML")
		}
	})
	if added := refreshAndGzipAllocations - refreshAllocations; added > 2 {
		t.Fatalf("GzipXML added %.0f allocations after identical refresh, want at most 2", added)
	}
}

func TestServiceFallbackDoesNotReplaceNewerMemoryDocument(t *testing.T) {
	oldRaw := []byte(`<tv><channel id="one"><display-name>Old</display-name></channel></tv>`)
	newRaw := []byte(`<tv><channel id="one"><display-name>New</display-name></channel></tv>`)
	store := failingSaveCacheStore{entry: epg.CacheEntry{
		SourceID: "source",
		Data:     oldRaw,
		Metadata: epg.CacheMetadata{ETag: `"old"`},
	}}
	var calls int
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
		calls++
		if calls == 1 {
			return epg.FetchResult{Data: newRaw, Metadata: epg.CacheMetadata{ETag: `"new"`}}, nil
		}
		return epg.FetchResult{}, errors.New("upstream unavailable")
	}), store)
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("initial refresh succeeded despite cache save failure")
	}
	warm := gunzipEPG(t, mustGzipEPG(t, service, channels))
	if !strings.Contains(warm, "New") {
		t.Fatalf("initial gzip XML = %s", warm)
	}
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("fallback refresh succeeded despite upstream failure")
	}

	xmlPayload, err := service.XML(channels)
	if err != nil {
		t.Fatal(err)
	}
	gzipPayload := gunzipEPG(t, mustGzipEPG(t, service, channels))
	if !strings.Contains(string(xmlPayload), "New") || !strings.Contains(gzipPayload, "New") {
		t.Fatalf("fallback diverged XML and gzip:\nxml: %s\ngzip: %s", xmlPayload, gzipPayload)
	}
}

func TestServiceNotModifiedPublishesRevalidatedCachedDocument(t *testing.T) {
	oldRaw := []byte(`<tv><channel id="one"><display-name>Old</display-name></channel></tv>`)
	newRaw := []byte(`<tv><channel id="one"><display-name>New</display-name></channel></tv>`)
	store := failingSaveCacheStore{entry: epg.CacheEntry{
		SourceID: "source",
		Data:     oldRaw,
		Metadata: epg.CacheMetadata{ETag: `"old"`},
	}}
	var calls int
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, sourceFetcherFunc(func(context.Context, epg.Source, epg.CacheMetadata) (epg.FetchResult, error) {
		calls++
		if calls == 1 {
			return epg.FetchResult{Data: newRaw, Metadata: epg.CacheMetadata{ETag: `"new"`}}, nil
		}
		return epg.FetchResult{NotModified: true, Metadata: epg.CacheMetadata{ETag: `"old"`}}, nil
	}), store)
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}

	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("initial refresh succeeded despite cache save failure")
	}
	if got := gunzipEPG(t, mustGzipEPG(t, service, channels)); !strings.Contains(got, "New") {
		t.Fatalf("initial gzip XML = %s", got)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := gunzipEPG(t, mustGzipEPG(t, service, channels)); !strings.Contains(got, "Old") {
		t.Fatalf("revalidated gzip XML = %s", got)
	}
}

func TestServiceRepeatedGzipXMLAvoidsRebuildingDocument(t *testing.T) {
	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel>` +
		`<programme channel="one" start="20260713080000 +0800"><title>First</title></programme></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"source": {Data: raw}}}, nil)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	channels := []epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}}
	if _, err := service.GzipXML(channels); err != nil {
		t.Fatal(err)
	}

	allocations := testing.AllocsPerRun(5, func() {
		if _, err := service.GzipXML(channels); err != nil {
			t.Fatal(err)
		}
	})
	if allocations > 2 {
		t.Fatalf("cached GzipXML allocations = %.0f, want at most 2", allocations)
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

func TestServiceRejectsCachedDocumentAboveSourceLimit(t *testing.T) {
	t.Parallel()

	store := epg.NewMemoryStore()
	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel><!-- padding padding padding --></tv>`)
	if err := store.Save(epg.CacheEntry{SourceID: "limited", Data: raw}); err != nil {
		t.Fatal(err)
	}
	service := epg.NewService(epg.ServiceConfig{
		Sources:        []epg.Source{{ID: "limited", Timezone: "Asia/Hong_Kong"}},
		MaxSourceBytes: 32,
	}, &fakeSourceFetcher{errors: map[string]error{"limited": errors.New("offline")}}, store)

	err := service.Refresh(context.Background())
	if !errors.Is(err, epg.ErrSourceTooLarge) {
		t.Fatalf("Refresh error = %v, want ErrSourceTooLarge", err)
	}
	statuses := service.Statuses()
	if len(statuses) != 1 || statuses[0].Available {
		t.Fatalf("oversized cache became available: %+v", statuses)
	}
}

func TestServiceRejectsFetcherResultAboveSourceLimit(t *testing.T) {
	t.Parallel()

	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel><!-- padding padding padding --></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources:        []epg.Source{{ID: "limited", Timezone: "Asia/Hong_Kong"}},
		MaxSourceBytes: 32,
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"limited": {Data: raw}}}, nil)

	err := service.Refresh(context.Background())
	if !errors.Is(err, epg.ErrSourceTooLarge) {
		t.Fatalf("Refresh error = %v, want ErrSourceTooLarge", err)
	}
	if statuses := service.Statuses(); len(statuses) != 1 || statuses[0].Available {
		t.Fatalf("oversized fetch became available: %+v", statuses)
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
			channels := []epg.ChannelRef{{ID: "kiln", EPGID: "one"}}
			_ = service.Document(channels)
			_, _ = service.GzipXML(channels)
		}()
	}
	wait.Wait()
	if got := len(service.Document([]epg.ChannelRef{{ID: "kiln", EPGID: "one"}}).Channels); got != 1 {
		t.Fatalf("channel count = %d, want 1", got)
	}
}

func TestServiceRefreshHonorsMaxRefreshConcurrency(t *testing.T) {
	t.Parallel()

	started := make(chan string, 3)
	release := make(chan struct{}, 3)
	fetcher := sourceFetcherFunc(func(_ context.Context, source epg.Source, _ epg.CacheMetadata) (epg.FetchResult, error) {
		started <- source.ID
		<-release
		return epg.FetchResult{Data: []byte(`<tv></tv>`)}, nil
	})
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{
			{ID: "one", Timezone: "Asia/Hong_Kong"},
			{ID: "two", Timezone: "Asia/Hong_Kong"},
			{ID: "three", Timezone: "Asia/Hong_Kong"},
		},
		MaxRefreshConcurrency: 2,
	}, fetcher, nil)
	done := make(chan error, 1)
	go func() { done <- service.Refresh(context.Background()) }()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for initial refreshes")
		}
	}
	select {
	case id := <-started:
		t.Fatalf("source %q started above the configured concurrency limit", id)
	case <-time.After(25 * time.Millisecond):
	}

	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queued source did not start after capacity became available")
	}
	release <- struct{}{}
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Refresh did not finish")
	}
}

func TestServiceRefreshWithoutLimitStartsAllSources(t *testing.T) {
	t.Parallel()

	started := make(chan string, 3)
	release := make(chan struct{}, 3)
	fetcher := sourceFetcherFunc(func(_ context.Context, source epg.Source, _ epg.CacheMetadata) (epg.FetchResult, error) {
		started <- source.ID
		<-release
		return epg.FetchResult{Data: []byte(`<tv></tv>`)}, nil
	})
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{
			{ID: "one", Timezone: "Asia/Hong_Kong"},
			{ID: "two", Timezone: "Asia/Hong_Kong"},
			{ID: "three", Timezone: "Asia/Hong_Kong"},
		},
	}, fetcher, nil)
	done := make(chan error, 1)
	go func() { done <- service.Refresh(context.Background()) }()

	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("default refresh did not start every source in parallel")
		}
	}
	for range 3 {
		release <- struct{}{}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Refresh did not finish")
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

type failingSaveCacheStore struct {
	entry epg.CacheEntry
}

func (s failingSaveCacheStore) Load(sourceID string) (epg.CacheEntry, bool, error) {
	if sourceID != s.entry.SourceID {
		return epg.CacheEntry{}, false, nil
	}
	return s.entry, true, nil
}

func (failingSaveCacheStore) Save(epg.CacheEntry) error {
	return errors.New("cache unavailable")
}

func mustGzipEPG(t *testing.T, service *epg.Service, channels []epg.ChannelRef) []byte {
	t.Helper()
	payload, err := service.GzipXML(channels)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func gunzipEPG(t *testing.T, payload []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}
