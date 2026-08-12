package epg

import (
	"bufio"
	"bytes"
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
	Body        io.ReadCloser `json:"-"`
	Metadata    CacheMetadata `json:"metadata"`
	NotModified bool          `json:"not_modified"`
}

func (r FetchResult) Close() error {
	if r.Body == nil {
		return nil
	}
	return r.Body.Close()
}

func (r FetchResult) reader() io.Reader {
	if r.Body != nil {
		return r.Body
	}
	return bytes.NewReader(r.Data)
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
	timeout := client.Timeout
	var cancel context.CancelFunc
	var headerTimer *time.Timer
	if timeout > 0 {
		requestContext, cancelRequest := context.WithCancel(ctx)
		cancel = cancelRequest
		request = request.WithContext(requestContext)
		clientCopy := *client
		clientCopy.Timeout = 0
		client = &clientCopy
		headerTimer = time.AfterFunc(timeout, cancel)
	}
	response, err := client.Do(request)
	if headerTimer != nil {
		headerTimer.Stop()
	}
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: %w", source.ID, err)
	}
	closeBody := func() {
		_ = response.Body.Close()
		if cancel != nil {
			cancel()
		}
	}

	if response.StatusCode == http.StatusNotModified {
		closeBody()
		return FetchResult{
			Metadata:    mergeMetadata(previous, metadataFromResponse(response)),
			NotModified: true,
		}, nil
	}
	if response.StatusCode != http.StatusOK {
		closeBody()
		return FetchResult{}, &HTTPStatusError{SourceID: source.ID, Status: response.Status, Code: response.StatusCode}
	}

	reader := bufio.NewReader(response.Body)
	var peekTimer *time.Timer
	if timeout > 0 {
		peekTimer = time.AfterFunc(timeout, cancel)
	}
	magic, peekErr := reader.Peek(2)
	if peekTimer != nil {
		peekTimer.Stop()
	}
	if peekErr != nil && peekErr != io.EOF {
		closeBody()
		return FetchResult{}, fmt.Errorf("fetch EPG source %q: read body: %w", source.ID, peekErr)
	}
	body := &sourceBody{
		sourceID: source.ID, limit: f.MaxSourceBytes, reader: reader,
		closers: []io.Closer{response.Body}, cancel: cancel, readTimeout: timeout,
	}
	if body.limit <= 0 {
		body.limit = DefaultMaxSourceBytes
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	isGzip := encoding == "gzip" || (len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b)
	if isGzip && !response.Uncompressed {
		compressed, err := gzip.NewReader(reader)
		if err != nil {
			closeBody()
			return FetchResult{}, fmt.Errorf("fetch EPG source %q: open gzip: %w", source.ID, err)
		}
		body.reader = compressed
		body.closers = append([]io.Closer{compressed}, body.closers...)
	}
	metadata := metadataFromResponse(response)
	metadata.FetchedAt = time.Now().UTC()
	return FetchResult{Body: body, Metadata: metadata}, nil
}

type sourceBody struct {
	reader      io.Reader
	closers     []io.Closer
	sourceID    string
	limit       int64
	read        int64
	err         error
	cancel      context.CancelFunc
	readTimeout time.Duration
}

func (b *sourceBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	var timer *time.Timer
	if b.readTimeout > 0 {
		timer = time.AfterFunc(b.readTimeout, b.cancel)
	}
	count, err := b.reader.Read(p)
	if timer != nil {
		timer.Stop()
	}
	b.read += int64(count)
	if b.read > b.limit {
		b.err = fmt.Errorf("fetch EPG source %q: %w (%d bytes)", b.sourceID, ErrSourceTooLarge, b.limit)
		return count, b.err
	}
	if err != nil && err != io.EOF {
		b.err = fmt.Errorf("fetch EPG source %q: read body: %w", b.sourceID, err)
		return count, b.err
	}
	return count, err
}

func (b *sourceBody) Close() error {
	var closeErr error
	for _, closer := range b.closers {
		if err := closer.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	b.closers = nil
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	return closeErr
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
