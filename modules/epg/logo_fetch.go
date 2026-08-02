package epg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const DefaultMaxLogoBytes int64 = 2 << 20

type LogoResult struct {
	SourceID    string
	ContentType string
	Data        []byte
}

func FetchLogo(ctx context.Context, client *http.Client, candidates []LogoCandidate, maxBytes int64) (LogoResult, error) {
	if client == nil {
		return LogoResult{}, errors.New("EPG logo HTTP client is nil")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLogoBytes
	}
	var failures []error
	for _, candidate := range candidates {
		parsed, err := url.Parse(candidate.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			failures = append(failures, fmt.Errorf("%s: invalid URL", candidate.SourceID))
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.URL, nil)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: create request", candidate.SourceID))
			continue
		}
		request.Header.Set("User-Agent", "Kiln Logo Resolver")
		response, err := client.Do(request)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: request failed", candidate.SourceID))
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			failures = append(failures, fmt.Errorf("%s: HTTP %d", candidate.SourceID, response.StatusCode))
			continue
		}
		if int64(len(data)) > maxBytes || len(data) == 0 {
			failures = append(failures, fmt.Errorf("%s: invalid response size", candidate.SourceID))
			continue
		}
		contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
		if contentType == "" || contentType == "application/octet-stream" {
			contentType = http.DetectContentType(data)
		}
		if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
			failures = append(failures, fmt.Errorf("%s: response is not an image", candidate.SourceID))
			continue
		}
		return LogoResult{SourceID: candidate.SourceID, ContentType: contentType, Data: data}, nil
	}
	if len(failures) == 0 {
		return LogoResult{}, errors.New("no EPG logo candidates")
	}
	return LogoResult{}, errors.Join(failures...)
}
