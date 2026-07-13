package packager

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/packager/mpd"
)

type fakeClockFetcher struct {
	body       string
	dateHeader string
	method     string
	err        error
}

func (f *fakeClockFetcher) FetchClock(_ context.Context, _ string, method string) ([]byte, string, error) {
	f.method = method
	return []byte(f.body), f.dateHeader, f.err
}

func TestResolveClockOffsetSupportsDirectAndHTTP(t *testing.T) {
	local := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	offset, source, err := resolveClockOffset(t.Context(), []mpd.UTCTiming{{
		Scheme: "urn:mpeg:dash:utc:direct:2014", Value: "2026-01-01T00:00:05Z",
	}}, nil, func() time.Time { return local }, "https://origin.example/live.mpd")
	if err != nil || offset != 5*time.Second || source != "direct" {
		t.Fatalf("direct offset=%v source=%q err=%v", offset, source, err)
	}

	fetcher := &fakeClockFetcher{body: "2026-01-01T00:00:11Z"}
	times := []time.Time{local, local.Add(2 * time.Second)}
	index := 0
	offset, source, err = resolveClockOffset(t.Context(), []mpd.UTCTiming{{
		Scheme: "urn:mpeg:dash:utc:http-xsdate:2014", Value: "/clock",
	}}, fetcher, func() time.Time {
		value := times[index]
		index++
		return value
	}, "https://origin.example/live.mpd")
	if err != nil || offset != 10*time.Second || source != "http-xsdate" || fetcher.method != http.MethodGet {
		t.Fatalf("http offset=%v source=%q method=%q err=%v", offset, source, fetcher.method, err)
	}
}

func TestResolveClockOffsetUsesDateHeaderAndRejectsWildOffset(t *testing.T) {
	local := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fetcher := &fakeClockFetcher{dateHeader: local.Add(3 * time.Second).Format(http.TimeFormat)}
	offset, source, err := resolveClockOffset(t.Context(), []mpd.UTCTiming{{
		Scheme: "urn:mpeg:dash:utc:http-head:2014", Value: "https://time.example/",
	}}, fetcher, func() time.Time { return local }, "https://origin.example/live.mpd")
	if err != nil || offset != 3*time.Second || source != "http-head" || fetcher.method != http.MethodHead {
		t.Fatalf("head offset=%v source=%q method=%q err=%v", offset, source, fetcher.method, err)
	}

	_, _, err = resolveClockOffset(t.Context(), []mpd.UTCTiming{{
		Scheme: "urn:mpeg:dash:utc:direct:2014", Value: local.Add(48 * time.Hour).Format(time.RFC3339),
	}}, nil, func() time.Time { return local }, "https://origin.example/live.mpd")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("wild offset error = %v", err)
	}
}
