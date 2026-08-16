package pull

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/proxyegress"
)

func TestClientReusesPinnedConnections(t *testing.T) {
	const parallel = 8
	var connections atomic.Int32
	arrived := [2]chan struct{}{make(chan struct{}, parallel), make(chan struct{}, parallel)}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wave, err := strconv.Atoi(r.URL.Query().Get("wave"))
		if err != nil || wave < 0 || wave >= len(arrived) {
			http.Error(w, "bad wave", http.StatusBadRequest)
			return
		}
		arrived[wave] <- struct{}{}
		<-release[wave]
		_, _ = io.WriteString(w, "ok")
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	origin.Start()
	defer origin.Close()
	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	for wave := range 2 {
		var requests sync.WaitGroup
		requests.Add(parallel)
		for range parallel {
			go func() {
				defer requests.Done()
				result, err := client.Get(context.Background(), Request{
					URL: origin.URL + "?wave=" + strconv.Itoa(wave), ChannelID: "channel",
				})
				if err != nil {
					t.Errorf("wave %d: %v", wave, err)
					return
				}
				_, _ = io.Copy(io.Discard, result.Body)
				_ = result.Body.Close()
			}()
		}
		timer := time.NewTimer(5 * time.Second)
		for range parallel {
			select {
			case <-arrived[wave]:
			case <-timer.C:
				close(release[wave])
				requests.Wait()
				t.Fatalf("wave %d did not reach %d concurrent requests", wave, parallel)
			}
		}
		timer.Stop()
		close(release[wave])
		requests.Wait()
	}
	if connections.Load() != parallel {
		t.Fatalf("upstream connections = %d, want %d reused across both waves", connections.Load(), parallel)
	}
}

func TestClientSharedTransportKeepsChannelRouting(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	defer origin.Close()
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	router, err := proxyegress.NewRouter(proxyegress.Config{
		Default:  proxyegress.Direct,
		Profiles: []proxyegress.Profile{{ID: "proxy", URL: proxy.URL}},
		Rules: []proxyegress.Rule{{
			Kind: proxyegress.KindChannel, Pattern: "proxied", ProxyID: "proxy",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}, Router: router})
	if _, err := client.Get(context.Background(), Request{URL: origin.URL, ChannelID: "proxied"}); !errors.Is(err, proxyegress.ErrUnpinnableProxyTarget) {
		t.Fatalf("proxied channel error = %v, want unpinnable target", err)
	}
	result, err := client.Get(context.Background(), Request{URL: origin.URL, ChannelID: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(result.Body)
	_ = result.Body.Close()
	if err != nil || string(body) != "direct" {
		t.Fatalf("direct channel response = %q, %v; want direct", body, err)
	}
	if got := proxyHits.Load(); got != 0 {
		t.Fatalf("unsafe proxy received %d requests", got)
	}
}

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

func TestMediaErrorPreservationIsNarrowAndOptIn(t *testing.T) {
	const upstreamBody = "upstream-secret-body"
	var connections atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := strconv.Atoi(r.URL.Query().Get("status"))
		if err != nil {
			http.Error(w, "bad status", http.StatusBadRequest)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes */10")
		w.Header().Set("ETag", `"error-v1"`)
		w.Header().Set("X-Origin-Secret", "do-not-forward")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	origin.Start()
	defer origin.Close()

	tests := []struct {
		name       string
		status     int
		preserve   bool
		wantResult bool
		wantRange  string
	}{
		{name: "default not found", status: http.StatusNotFound},
		{name: "preserved not found", status: http.StatusNotFound, preserve: true, wantResult: true},
		{name: "preserved gone", status: http.StatusGone, preserve: true, wantResult: true},
		{name: "preserved range", status: http.StatusRequestedRangeNotSatisfiable, preserve: true, wantResult: true, wantRange: "bytes */10"},
		{name: "other error", status: http.StatusUnauthorized, preserve: true},
	}
	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := client.Get(context.Background(), Request{
				URL: origin.URL + "?status=" + strconv.Itoa(test.status), PreserveMediaErrors: test.preserve,
			})
			if !test.wantResult {
				if err == nil {
					_ = result.Body.Close()
					t.Fatalf("status %d unexpectedly returned a result", test.status)
				}
				appErr, ok := apperr.As(err)
				if !ok || appErr.HTTPStatus != http.StatusBadGateway {
					t.Fatalf("status %d error = %v, want controlled 502", test.status, err)
				}
				if got, want := err.Error(), "upstream status "+strconv.Itoa(test.status); got != want {
					t.Fatalf("status %d error = %q, want %q", test.status, got, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer result.Body.Close()
			body, readErr := io.ReadAll(result.Body)
			if readErr != nil || len(body) != 0 {
				t.Fatalf("status %d body = %q, err=%v", test.status, body, readErr)
			}
			if result.StatusCode != test.status || result.ContentLength != -1 || result.ContentType != "" {
				t.Fatalf("status %d result = %+v", test.status, result)
			}
			if got := result.Header.Get("Content-Range"); got != test.wantRange {
				t.Fatalf("status %d Content-Range = %q, want %q", test.status, got, test.wantRange)
			}
			for _, name := range []string{"Accept-Ranges", "ETag", "X-Origin-Secret"} {
				if got := result.Header.Get(name); got != "" {
					t.Fatalf("status %d leaked %s %q", test.status, name, got)
				}
			}
		})
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("upstream connections = %d, want 1", got)
	}
}

func TestPreservedMediaErrorDoesNotWaitForTricklingBody(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(errorBodyDrainLimit))
		w.WriteHeader(http.StatusNotFound)
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write([]byte("x")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer origin.Close()
	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	started := time.Now()
	result, err := client.Get(context.Background(), Request{URL: origin.URL, PreserveMediaErrors: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", result.StatusCode)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("preserved media error took %s", elapsed)
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
