package packager

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/packager/mpd"
)

const maxClockOffset = 24 * time.Hour

type utcClockFetcher interface {
	FetchClock(ctx context.Context, url, method string) (body []byte, dateHeader string, err error)
}

func resolveClockOffset(ctx context.Context, timings []mpd.UTCTiming, fetcher any, now func() time.Time, baseURL string) (time.Duration, string, error) {
	var lastErr error
	for _, timing := range timings {
		offset, source, err := resolveOneClock(ctx, timing, fetcher, now, baseURL)
		if err != nil {
			lastErr = err
			continue
		}
		if offset > maxClockOffset || offset < -maxClockOffset {
			lastErr = fmt.Errorf("UTC timing offset %v exceeds safety limit", offset)
			continue
		}
		return offset, source, nil
	}
	if len(timings) == 0 {
		return 0, "", nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no supported UTC timing source")
	}
	return 0, "", lastErr
}

func resolveOneClock(ctx context.Context, timing mpd.UTCTiming, fetcher any, now func() time.Time, baseURL string) (time.Duration, string, error) {
	scheme := strings.ToLower(strings.TrimSpace(timing.Scheme))
	value := strings.TrimSpace(timing.Value)
	switch {
	case strings.Contains(scheme, ":direct:"):
		serverTime, err := parseClockTime(value)
		if err != nil {
			return 0, "", err
		}
		return serverTime.Sub(now()), "direct", nil
	case strings.Contains(scheme, ":http-xsdate:"), strings.Contains(scheme, ":http-iso:"), strings.Contains(scheme, ":http-head:"):
		clockFetcher, ok := fetcher.(utcClockFetcher)
		if !ok {
			return 0, "", fmt.Errorf("fetcher does not support UTC timing")
		}
		target, err := resolveClockURL(baseURL, value)
		if err != nil {
			return 0, "", err
		}
		method := http.MethodGet
		source := "http-xsdate"
		if strings.Contains(scheme, ":http-iso:") {
			source = "http-iso"
		}
		if strings.Contains(scheme, ":http-head:") {
			method = http.MethodHead
			source = "http-head"
		}
		before := now()
		body, dateHeader, err := clockFetcher.FetchClock(ctx, target, method)
		if err != nil {
			return 0, "", err
		}
		after := now()
		clockValue := strings.TrimSpace(string(body))
		if method == http.MethodHead {
			clockValue = dateHeader
		}
		serverTime, err := parseClockTime(clockValue)
		if err != nil {
			return 0, "", err
		}
		midpoint := before.Add(after.Sub(before) / 2)
		return serverTime.Sub(midpoint), source, nil
	default:
		return 0, "", fmt.Errorf("unsupported UTC timing scheme %q", timing.Scheme)
	}
}

func resolveClockURL(baseURL, value string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(reference).String(), nil
}

func parseClockTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, http.TimeFormat} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid UTC timing value %q", value)
}
