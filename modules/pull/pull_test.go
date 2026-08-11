package pull

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefaultRequestRejectsHostnameResolvingToPrivateAddress(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("private"))
	}))
	defer origin.Close()

	client := New(Options{})
	_, err := client.Get(context.Background(), Request{
		URL: strings.Replace(origin.URL, "127.0.0.1", "localhost", 1),
	})
	if err == nil {
		t.Fatal("default request reached a private hostname")
	}
}

func TestDefaultRequestRejectsSpecialDestinations(t *testing.T) {
	client := New(Options{})
	for _, rawURL := range []string{
		"http://10.0.0.1/private",
		"http://169.254.169.254/latest/meta-data",
		"http://100.64.0.1/private",
		"http://metadata.google.internal/computeMetadata/v1",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := client.Get(context.Background(), Request{URL: rawURL}); err == nil {
				t.Fatalf("default request accepted %s", rawURL)
			}
		})
	}
}

func TestDefaultRequestRejectsPrivateRedirectDestination(t *testing.T) {
	var origin *httptest.Server
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, strings.Replace(origin.URL, "127.0.0.1", "localhost", 1)+"/private", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "private")
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	if _, err := client.Get(context.Background(), Request{URL: origin.URL + "/redirect"}); err == nil {
		t.Fatal("redirect reached a private hostname")
	}
}

func TestCustomHeadersFollowOnlyTheAuthorizedOrigin(t *testing.T) {
	targetHeader := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHeader <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "target")
	}))
	defer target.Close()

	sameOriginHeader := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/cross":
			http.Redirect(w, r, target.URL, http.StatusFound)
		case "/final":
			sameOriginHeader <- r.Header.Get("X-Channel-Secret")
			_, _ = io.WriteString(w, "origin")
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	for _, path := range []string{"/same", "/cross"} {
		result, err := client.Get(context.Background(), Request{
			URL:          origin.URL + path,
			HeaderOrigin: origin.URL,
			Headers:      map[string]string{"X-Channel-Secret": "top-secret"},
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = result.Body.Close()
	}
	if got := <-sameOriginHeader; got != "top-secret" {
		t.Fatalf("same-origin header = %q, want channel secret", got)
	}
	if got := <-targetHeader; got != "" {
		t.Fatalf("cross-origin header = %q, want empty", got)
	}
}

func TestCustomHeadersRequireAnExplicitOrigin(t *testing.T) {
	received := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	result, err := client.Get(context.Background(), Request{
		URL:     origin.URL,
		Headers: map[string]string{"X-Channel-Secret": "top-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Body.Close()
	if got := <-received; got != "" {
		t.Fatalf("header without an authorized origin = %q, want empty", got)
	}
}

func TestCrossOriginRedirectDoesNotForwardSourceQueryInReferer(t *testing.T) {
	referer := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer <- r.Referer()
		_, _ = io.WriteString(w, "target")
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	result, err := client.Get(context.Background(), Request{URL: origin.URL + "/entry?token=source-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Body.Close()
	if got := <-referer; got != "" {
		t.Fatalf("cross-origin Referer = %q, want empty", got)
	}
}

func TestSlowSegmentOutlivesTheStallWindowWhileBytesKeepArriving(t *testing.T) {
	const chunks = 8
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range chunks {
			_, _ = w.Write(make([]byte, 4<<10))
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}, StallTimeout: 150 * time.Millisecond})
	data, _, err := client.GetBytesLimit(context.Background(), Request{URL: origin.URL}, 1<<20)
	if err != nil {
		t.Fatalf("steady transfer longer than the stall window failed: %v", err)
	}
	if len(data) != chunks*(4<<10) {
		t.Fatalf("bytes = %d, want %d", len(data), chunks*(4<<10))
	}
}

func TestStalledBodyFailsAfterTheStallWindow(t *testing.T) {
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 4<<10))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer origin.Close()
	defer close(release)

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}, StallTimeout: 100 * time.Millisecond})
	start := time.Now()
	if _, _, err := client.GetBytesLimit(context.Background(), Request{URL: origin.URL}, 1<<20); err == nil {
		t.Fatal("stalled upstream body was accepted")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stall detection took %v", elapsed)
	}
}

func TestStalledNonSuccessBodiesStayBounded(t *testing.T) {
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusFound)
		case "/final":
			_, _ = io.WriteString(w, "ok")
			return
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		w.(http.Flusher).Flush()
		<-release
	}))
	defer origin.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}, StallTimeout: 100 * time.Millisecond})
	start := time.Now()
	if _, err := client.Get(ctx, Request{URL: origin.URL + "/error"}); err == nil {
		t.Fatal("stalled upstream error body was accepted")
	}
	result, err := client.Get(ctx, Request{URL: origin.URL + "/redirect"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil || string(body) != "ok" {
		t.Fatalf("redirect result = %q, %v", body, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("non-success body handling took %v", elapsed)
	}
}

func TestGetBytesReservesOldAndNewBuffersDuringGrowth(t *testing.T) {
	body := make([]byte, 40<<10)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	var peak int64
	data, _, err := client.GetBytesLimitReserve(context.Background(), Request{URL: origin.URL}, 64<<10, func(liveBytes int64) error {
		if liveBytes > peak {
			peak = liveBytes
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(body) {
		t.Fatalf("bytes = %d, want %d", len(data), len(body))
	}
	if want := int64((32 << 10) + (64 << 10)); peak != want {
		t.Fatalf("peak reservation = %d, want %d", peak, want)
	}
}

func TestGetBytesUsesKnownContentLengthAsOneExactAllocation(t *testing.T) {
	body := make([]byte, 40<<10)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	var reservations []int64
	data, _, err := client.GetBytesLimitReserve(context.Background(), Request{URL: origin.URL}, 64<<10, func(liveBytes int64) error {
		reservations = append(reservations, liveBytes)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(body) || cap(data) != len(body) {
		t.Fatalf("buffer = len %d cap %d, want %d/%d", len(data), cap(data), len(body), len(body))
	}
	if len(reservations) != 1 || reservations[0] != int64(len(body)) {
		t.Fatalf("reservations = %v, want [%d]", reservations, len(body))
	}
}
