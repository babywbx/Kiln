package playback

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/egress"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/session"
)

func TestHandleUpstreamRejectsForgedURLWithoutLeakingChannelHeaders(t *testing.T) {
	receivedSecret := make(chan string, 1)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSecret <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "attacker")
	}))
	defer attacker.Close()

	handler := newSecurityTestHandler(t, attacker.URL, map[string]string{"X-Channel-Secret": "top-secret"}, 0)
	request := httptest.NewRequest(http.MethodGet, "/v1/play/news/u/forged", nil)
	request.SetPathValue("id", "news")
	request.SetPathValue("upstream", egress.EncodeUpstream(attacker.URL+"/collect"))
	response := httptest.NewRecorder()

	handler.HandleUpstream(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("forged proxy request status = %d, want %d",
			response.Code, http.StatusForbidden)
	}
	select {
	case leaked := <-receivedSecret:
		t.Fatalf("forged proxy request reached attacker with header %q", leaked)
	default:
	}
}

func TestHandleUpstreamAcceptsURLSignedByRewrittenPlaylist(t *testing.T) {
	receivedSecret := make(chan string, 1)
	var origin *httptest.Server
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n"+origin.URL+"/segment.ts?next=%2Fasset\n")
		case "/segment.ts":
			receivedSecret <- r.Header.Get("X-Channel-Secret")
			_, _ = io.WriteString(w, "segment")
		default:
			http.Error(w, r.URL.String(), http.StatusNotFound)
		}
	}))
	defer origin.Close()

	handler := newSecurityTestHandler(t, origin.URL, map[string]string{"X-Channel-Secret": "top-secret"}, 0)
	indexRequest := httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8", nil)
	indexRequest.SetPathValue("id", "news")
	indexResponse := httptest.NewRecorder()
	handler.HandleIndex(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("index status = %d, body = %s", indexResponse.Code, indexResponse.Body.String())
	}
	proxyURL := strings.TrimSpace(strings.Split(indexResponse.Body.String(), "\n")[1])
	request := httptest.NewRequest(http.MethodGet, proxyURL, nil)
	response := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/play/{id}/u/{upstream}", handler.HandleUpstream)

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("signed proxy request status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if secret := <-receivedSecret; secret != "top-secret" {
		t.Fatalf("signed proxy request header = %q, want channel header", secret)
	}

	signature := request.URL.Query().Get("sig")
	for _, test := range []struct {
		name      string
		channelID string
		absolute  string
	}{
		{name: "url", channelID: "news", absolute: origin.URL + "/other.ts"},
		{name: "channel", channelID: "sports", absolute: origin.URL + "/segment.ts"},
	} {
		t.Run("rejects tampered "+test.name, func(t *testing.T) {
			encoded := egress.EncodeUpstream(test.absolute)
			target := "/v1/play/" + test.channelID + "/u/" + encoded +
				"?sig=" + url.QueryEscape(signature)
			tamperedRequest := httptest.NewRequest(http.MethodGet, target, nil)
			tamperedRequest.SetPathValue("id", test.channelID)
			tamperedRequest.SetPathValue("upstream", encoded)
			tamperedResponse := httptest.NewRecorder()

			handler.HandleUpstream(tamperedResponse, tamperedRequest)

			if tamperedResponse.Code != http.StatusForbidden {
				t.Fatalf("tampered %s status = %d, want 403: %s",
					test.name, tamperedResponse.Code, tamperedResponse.Body.String())
			}
		})
	}
}

func TestHandleUpstreamStripsChannelHeadersFromCrossOriginAssets(t *testing.T) {
	receivedSecret := make(chan string, 1)
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSecret <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "segment")
	}))
	defer asset.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n"+asset.URL+"/segment.ts\n")
	}))
	defer origin.Close()

	handler := newSecurityTestHandler(t, origin.URL, map[string]string{"X-Channel-Secret": "top-secret"}, 0)
	indexRequest := httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8", nil)
	indexRequest.SetPathValue("id", "news")
	indexResponse := httptest.NewRecorder()
	handler.HandleIndex(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("index status = %d, body = %s", indexResponse.Code, indexResponse.Body.String())
	}
	proxyURL := strings.TrimSpace(strings.Split(indexResponse.Body.String(), "\n")[1])
	request := httptest.NewRequest(http.MethodGet, proxyURL, nil)
	response := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/play/{id}/u/{upstream}", handler.HandleUpstream)

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("signed proxy request status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if secret := <-receivedSecret; secret != "" {
		t.Fatalf("cross-origin asset received channel header %q", secret)
	}
}

func TestHandleUpstreamRejectsSignedHostnameResolvingToPrivateAddress(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\nhttp://localhost:1/private.ts\n")
	}))
	defer origin.Close()

	handler := newSecurityTestHandler(t, origin.URL, nil, 0)
	indexRequest := httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8", nil)
	indexRequest.SetPathValue("id", "news")
	indexResponse := httptest.NewRecorder()
	handler.HandleIndex(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("index status = %d, body = %s", indexResponse.Code, indexResponse.Body.String())
	}
	proxyURL := strings.TrimSpace(strings.Split(indexResponse.Body.String(), "\n")[1])
	upstream := strings.TrimPrefix(strings.SplitN(proxyURL, "?", 2)[0], "/v1/play/news/u/")
	request := httptest.NewRequest(http.MethodGet, proxyURL, nil)
	request.SetPathValue("id", "news")
	request.SetPathValue("upstream", upstream)
	response := httptest.NewRecorder()

	handler.HandleUpstream(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("private DNS target status = %d, want 403: %s",
			response.Code, response.Body.String())
	}
}

func TestHandleIndexRejectsPrivateDNSWithoutExplicitAllowlist(t *testing.T) {
	var reached atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = io.WriteString(w, "#EXTM3U\n")
	}))
	defer origin.Close()

	privateSource := strings.Replace(origin.URL, "127.0.0.1", "localhost", 1) + "/index.m3u8"
	channel := config.Channel{ID: "news", Title: "News", Ingress: "hls", SourceURL: privateSource}
	cfg := config.File{Channels: []config.Channel{channel}}
	cat := catalog.New(cfg, nil)
	obs := observe.New()
	puller := pull.New(pull.Options{Observe: obs})
	sessions := session.NewManager(
		cat, puller, obs, t.TempDir(), config.FFmpeg{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
	)
	handler := New(Deps{
		Cfg: cfg, Catalog: cat, Sessions: sessions, Observe: obs, Allowed: cfg.AllowedHostSet(),
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8", nil)
	request.SetPathValue("id", "news")
	response := httptest.NewRecorder()

	handler.HandleIndex(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("private HLS source status = %d, want 502: %s", response.Code, response.Body.String())
	}
	if reached.Load() {
		t.Fatal("private HLS source received a request")
	}
}

func TestHandleIndexEnforcesMaxViewersWithServerIssuedLeases(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\nsegment.ts\n")
			return
		}
		_, _ = io.WriteString(w, "segment")
	}))
	defer origin.Close()

	handler := newSecurityTestHandler(t, origin.URL, nil, 1)
	request := func(target string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("User-Agent", "shared-player")
		r.SetPathValue("id", "news")
		return r
	}

	firstRedirect := httptest.NewRecorder()
	handler.HandleIndex(firstRedirect, request("/v1/play/news/index.m3u8"))
	if firstRedirect.Code != http.StatusTemporaryRedirect {
		t.Fatalf("first viewer redirect status = %d, want 307", firstRedirect.Code)
	}
	firstLeaseURL := firstRedirect.Header().Get("Location")
	firstResponse := httptest.NewRecorder()
	handler.HandleIndex(firstResponse, request(firstLeaseURL))
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first viewer status = %d, want 200", firstResponse.Code)
	}
	childURL := strings.TrimSpace(strings.Split(firstResponse.Body.String(), "\n")[1])
	child, err := url.Parse(childURL)
	if err != nil || child.Query().Get(viewerQuery) != request(firstLeaseURL).URL.Query().Get(viewerQuery) {
		t.Fatalf("child URL did not preserve viewer lease: %q", childURL)
	}

	secondRedirect := httptest.NewRecorder()
	handler.HandleIndex(secondRedirect, request("/v1/play/news/index.m3u8"))
	if secondRedirect.Code != http.StatusTemporaryRedirect {
		t.Fatalf("second viewer redirect status = %d, want 307", secondRedirect.Code)
	}
	secondLeaseURL := secondRedirect.Header().Get("Location")
	if secondLeaseURL == firstLeaseURL {
		t.Fatal("distinct viewers received the same lease")
	}
	secondResponse := httptest.NewRecorder()
	handler.HandleIndex(secondResponse, request(secondLeaseURL))
	if secondResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("second viewer status = %d, want %d: %s",
			secondResponse.Code, http.StatusTooManyRequests, secondResponse.Body.String())
	}
}

func TestHandleIndexDoesNotKeepLeaseWhenUpstreamFails(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n")
	}))
	defer origin.Close()

	handler := newSecurityTestHandler(t, origin.URL, nil, 1)
	request := func(target string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.SetPathValue("id", "news")
		return r
	}
	leaseURL := func() string {
		response := httptest.NewRecorder()
		handler.HandleIndex(response, request("/v1/play/news/index.m3u8"))
		if response.Code != http.StatusTemporaryRedirect {
			t.Fatalf("lease redirect status = %d, want 307", response.Code)
		}
		return response.Header().Get("Location")
	}

	failed := httptest.NewRecorder()
	handler.HandleIndex(failed, request(leaseURL()))
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("failed upstream status = %d, want 502", failed.Code)
	}

	fail.Store(false)
	recovered := httptest.NewRecorder()
	handler.HandleIndex(recovered, request(leaseURL()))
	if recovered.Code != http.StatusOK {
		t.Fatalf("viewer after failed upstream status = %d, want 200", recovered.Code)
	}
}

func TestViewerLeaseOnlyTouchesLocalPlaybackURLs(t *testing.T) {
	playlist := "#EXTM3U\n/v1/play/news/u/local\nhttps://cdn.example/signed.ts?key=value\n"
	got := appendViewerToPlaylistURLs(playlist, "lease")
	if !strings.Contains(got, "/v1/play/news/u/local?viewer=lease") {
		t.Fatalf("local playback URL is missing viewer lease: %s", got)
	}
	if !strings.Contains(got, "https://cdn.example/signed.ts?key=value\n") {
		t.Fatalf("external signed URL was modified: %s", got)
	}
}

func TestSessionTokenDoesNotTouchProtocolRelativeURLs(t *testing.T) {
	playlist := "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"//cdn.example/key.bin\"\n//cdn.example/segment.ts\n/v1/play/news/u/local\n"
	got := appendTokenToPlaylistURLs(playlist, "session-secret")
	if strings.Contains(got, "//cdn.example/key.bin?token=") || strings.Contains(got, "//cdn.example/segment.ts?token=") {
		t.Fatalf("protocol-relative URL received session token: %s", got)
	}
	if !strings.Contains(got, "/v1/play/news/u/local?token=session-secret") {
		t.Fatalf("local playback URL is missing session token: %s", got)
	}
}

func TestHandleIndexNeverAddsSessionTokenToExternalPlaylistURLs(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"https://cdn.example/key.bin?sig=key\"\nhttps://cdn.example/segment.ts?sig=segment\n")
	}))
	defer origin.Close()

	for _, policy := range []proxyegress.PlaylistPolicy{
		proxyegress.PolicyAuto,
		proxyegress.PolicyPassthrough,
	} {
		t.Run(string(policy), func(t *testing.T) {
			handler := newSecurityTestHandler(t, origin.URL, nil, 0)
			router, err := proxyegress.NewRouter(proxyegress.Config{PlaylistPolicy: policy})
			if err != nil {
				t.Fatal(err)
			}
			handler.deps.Egress = router
			handler.deps.Token = func(r *http.Request) string { return r.URL.Query().Get("token") }

			request := httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8?token=session-secret", nil)
			request.SetPathValue("id", "news")
			response := httptest.NewRecorder()
			handler.HandleIndex(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("index status = %d, want 200: %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			if strings.Contains(body, "session-secret") {
				t.Fatalf("external playlist URL contains session token: %s", body)
			}
			for _, want := range []string{
				`URI="https://cdn.example/key.bin?sig=key"`,
				"https://cdn.example/segment.ts?sig=segment",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("external URL changed, missing %q in %s", want, body)
				}
			}
		})
	}
}

func TestMaxViewersForcesPlaylistProxying(t *testing.T) {
	handler := newSecurityTestHandler(t, "https://origin.example", nil, 1)
	router, err := proxyegress.NewRouter(proxyegress.Config{
		PlaylistPolicy: proxyegress.PolicyPassthrough,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.deps.Egress = router

	if !handler.shouldRewrite("news")("https://cdn.example/segment.ts") {
		t.Fatal("max_viewers channel allowed playback to bypass viewer leases")
	}
}

func newSecurityTestHandler(
	t *testing.T,
	baseURL string,
	headers map[string]string,
	maxViewers int,
) *Handler {
	t.Helper()
	channel := config.Channel{
		ID: "news", Title: "News", Ingress: "hls", SourceURL: baseURL + "/index.m3u8",
		Headers: headers, MaxViewers: maxViewers,
	}
	cfg := config.File{Channels: []config.Channel{channel}}
	cat := catalog.New(cfg, nil)
	obs := observe.New()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Security.AllowedHosts = []string{parsed.Hostname()}
	allowed := map[string]struct{}{parsed.Hostname(): {}}
	puller := pull.New(pull.Options{Observe: obs, Allowed: allowed})
	sessions := session.NewManager(
		cat, puller, obs, t.TempDir(), config.FFmpeg{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
	)
	return New(Deps{
		Cfg: cfg, Catalog: cat, Sessions: sessions, Observe: obs, Allowed: allowed,
	})
}
