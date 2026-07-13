package epg

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultMaxSourceBytes int64 = 64 << 20

var defaultFetchClient = &http.Client{Timeout: 30 * time.Second}

var ErrSourceTooLarge = errors.New("EPG source exceeds decompressed size limit")

type CacheMetadata struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	ContentType  string    `json:"content_type,omitempty"`
	CacheControl string    `json:"cache_control,omitempty"`
	FetchedAt    time.Time `json:"fetched_at,omitempty"`
}

type FetchResult struct {
	Data        []byte        `json:"-"`
	Metadata    CacheMetadata `json:"metadata"`
	NotModified bool          `json:"not_modified"`
}

type SourceFetcher interface {
	Fetch(context.Context, Source, CacheMetadata) (FetchResult, error)
}

type Fetcher struct {
	Client          *http.Client
	ClientForSource func(Source) (*http.Client, error)
	MaxSourceBytes  int64
	UserAgent       string
}

type HTTPStatusError struct {
	SourceID string
	Status   string
	Code     int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("fetch EPG source %q: HTTP %s", e.SourceID, e.Status)
}

func (f *Fetcher) Fetch(ctx context.Context, source Source, previous CacheMetadata) (FetchResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: %w", source.ID, err)
	}
	request.Header.Set("Accept", "application/xml, text/xml;q=0.9, */*;q=0.1")
	request.Header.Set("Accept-Encoding", "gzip")
	if f.UserAgent != "" {
		request.Header.Set("User-Agent", f.UserAgent)
	}
	if previous.ETag != "" {
		request.Header.Set("If-None-Match", previous.ETag)
	}
	if previous.LastModified != "" {
		request.Header.Set("If-Modified-Since", previous.LastModified)
	}

	client := f.Client
	if f.ClientForSource != nil {
		client, err = f.ClientForSource(source)
		if err != nil {
			return FetchResult{}, fmt.Errorf("fetch EPG source %q: resolve HTTP client: %w", source.ID, err)
		}
		if client == nil {
			return FetchResult{}, fmt.Errorf("fetch EPG source %q: resolved HTTP client is nil", source.ID)
		}
	}
	if client == nil {
		client = defaultFetchClient
	}
	response, err := client.Do(request)
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: %w", source.ID, err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified {
		return FetchResult{
			Metadata:    mergeMetadata(previous, metadataFromResponse(response)),
			NotModified: true,
		}, nil
	}
	if response.StatusCode != http.StatusOK {
		return FetchResult{}, &HTTPStatusError{SourceID: source.ID, Status: response.Status, Code: response.StatusCode}
	}

	reader := bufio.NewReader(response.Body)
	var payload io.Reader = reader
	var compressed *gzip.Reader
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	magic, _ := reader.Peek(2)
	isGzip := encoding == "gzip" || (len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b)
	if isGzip && !response.Uncompressed {
		compressed, err = gzip.NewReader(reader)
		if err != nil {
			return FetchResult{}, fmt.Errorf("fetch EPG source %q: open gzip: %w", source.ID, err)
		}
		payload = compressed
	}

	limit := f.MaxSourceBytes
	if limit <= 0 {
		limit = DefaultMaxSourceBytes
	}
	data, err := io.ReadAll(io.LimitReader(payload, limit+1))
	var closeErr error
	if compressed != nil {
		closeErr = compressed.Close()
	}
	if err != nil {
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: read body: %w", source.ID, err)
	}
	if closeErr != nil {
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: close gzip: %w", source.ID, closeErr)
	}
	if int64(len(data)) > limit {
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: %w (%d bytes)", source.ID, ErrSourceTooLarge, limit)
	}
	metadata := metadataFromResponse(response)
	metadata.FetchedAt = time.Now().UTC()
	return FetchResult{Data: data, Metadata: metadata}, nil
}

func metadataFromResponse(response *http.Response) CacheMetadata {
	return CacheMetadata{
		ETag:         response.Header.Get("ETag"),
		LastModified: response.Header.Get("Last-Modified"),
		ContentType:  response.Header.Get("Content-Type"),
		CacheControl: response.Header.Get("Cache-Control"),
	}
}

func mergeMetadata(previous, current CacheMetadata) CacheMetadata {
	if current.ETag == "" {
		current.ETag = previous.ETag
	}
	if current.LastModified == "" {
		current.LastModified = previous.LastModified
	}
	if current.ContentType == "" {
		current.ContentType = previous.ContentType
	}
	if current.CacheControl == "" {
		current.CacheControl = previous.CacheControl
	}
	if current.FetchedAt.IsZero() {
		current.FetchedAt = previous.FetchedAt
	}
	return current
}
