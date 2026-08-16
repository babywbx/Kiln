package egress

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/babywbx/kiln/modules/pull"
	"golang.org/x/net/http/httpguts"
)

type ffmpegProxyOptions struct {
	Client                   *pull.Client
	ChannelID                string
	HeaderOrigin             string
	Headers                  map[string]string
	UserAgent                string
	InputPath                string
	Docker                   bool
	UpgradeInsecureRedirects bool
	UpgradeHTTPRequests      bool
}

type ffmpegForwardProxy struct {
	client          *pull.Client
	channelID       string
	headerOrigin    string
	headers         map[string]string
	userAgent       string
	authorization   string
	proxyURL        string
	inputPath       string
	inputURL        string
	upgradeInsecure bool
	upgradeHTTP     atomic.Bool
	listener        net.Listener
	server          *http.Server

	mu          sync.Mutex
	connections map[net.Conn]struct{}
	closed      bool
	closeOnce   sync.Once
	workers     sync.WaitGroup
}

func startFFmpegForwardProxy(options ffmpegProxyOptions) (*ffmpegForwardProxy, error) {
	if options.Client == nil {
		return nil, fmt.Errorf("dash ffmpeg requires a guarded upstream client")
	}
	if err := validateFFmpegProxyHeaders(options.Headers, options.UserAgent); err != nil {
		return nil, err
	}
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		return nil, fmt.Errorf("create ffmpeg proxy credentials: %w", err)
	}
	username := "kiln"
	password := base64.RawURLEncoding.EncodeToString(passwordBytes)
	authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))

	bindHost := "127.0.0.1"
	advertisedHost := bindHost
	if options.Docker {
		bindHost = "0.0.0.0"
		advertisedHost = "host.docker.internal"
		if router := options.Client.Router(); router != nil {
			if configured := strings.TrimSpace(router.Config().DockerProxyHost); configured != "" {
				advertisedHost = configured
			}
		}
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return nil, fmt.Errorf("start ffmpeg proxy: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	proxyAddress := &url.URL{
		Scheme: "http",
		User:   url.UserPassword(username, password),
		Host:   net.JoinHostPort(advertisedHost, fmt.Sprintf("%d", port)),
	}
	inputURL := *proxyAddress
	inputURL.User = nil
	inputURL.Path = "/__kiln_ffmpeg_input.mpd"
	proxy := &ffmpegForwardProxy{
		client:          options.Client,
		channelID:       options.ChannelID,
		headerOrigin:    options.HeaderOrigin,
		headers:         options.Headers,
		userAgent:       options.UserAgent,
		upgradeInsecure: options.UpgradeInsecureRedirects,
		authorization:   authorization,
		proxyURL:        proxyAddress.String(),
		inputPath:       options.InputPath,
		inputURL:        inputURL.String(),
		listener:        listener,
		connections:     map[net.Conn]struct{}{},
	}
	proxy.upgradeHTTP.Store(options.UpgradeHTTPRequests)
	proxy.server = &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

func validateFFmpegProxyHeaders(headers map[string]string, userAgent string) error {
	if userAgent != "" && !httpguts.ValidHeaderFieldValue(userAgent) {
		return fmt.Errorf("ffmpeg user agent contains invalid characters")
	}
	for name, value := range headers {
		if name == "" || value == "" {
			continue
		}
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("ffmpeg header %q is invalid", name)
		}
		switch strings.ToLower(name) {
		case "connection", "content-length", "host", "proxy-authorization", "proxy-connection", "transfer-encoding", "upgrade":
			return fmt.Errorf("ffmpeg header %q is not allowed", name)
		}
	}
	return nil
}

func (p *ffmpegForwardProxy) URL() string {
	return p.proxyURL
}

func (p *ffmpegForwardProxy) InputURL() string {
	if p.inputPath == "" {
		return ""
	}
	return p.inputURL
}

func (p *ffmpegForwardProxy) Env() []string {
	return []string{
		"HTTP_PROXY=" + p.proxyURL,
		"HTTPS_PROXY=" + p.proxyURL,
		"http_proxy=" + p.proxyURL,
		"https_proxy=" + p.proxyURL,
		"NO_PROXY=",
		"no_proxy=",
	}
}

func (p *ffmpegForwardProxy) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		connections := make([]net.Conn, 0, len(p.connections))
		for connection := range p.connections {
			connections = append(connections, connection)
		}
		p.mu.Unlock()
		_ = p.server.Close()
		_ = p.listener.Close()
		for _, connection := range connections {
			_ = connection.Close()
		}
		p.workers.Wait()
	})
}

func (p *ffmpegForwardProxy) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if !p.authorized(request) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="Kiln FFmpeg"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if request.Method == http.MethodConnect {
		p.serveConnect(w, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if p.inputPath != "" && request.URL != nil && request.URL.String() == p.inputURL {
		w.Header().Set("Content-Type", "application/dash+xml")
		http.ServeFile(w, request, p.inputPath)
		return
	}
	if request.URL == nil || request.URL.Hostname() == "" ||
		(request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		http.Error(w, "invalid proxy target", http.StatusBadRequest)
		return
	}
	userAgent := p.userAgent
	if userAgent == "" {
		userAgent = request.UserAgent()
	}
	targetURL := *request.URL
	if p.upgradeHTTP.Load() {
		p.client.UpgradeInsecureURL(&targetURL)
	}
	result, err := p.client.Do(request.Context(), request.Method, pull.Request{
		URL:                      targetURL.String(),
		UserAgent:                userAgent,
		Headers:                  p.headers,
		ForwardHeaders:           copyProxyRequestHeaders(request.Header, p.headers),
		HeaderOrigin:             p.headerOrigin,
		ChannelID:                p.channelID,
		StopRedirect:             !p.upgradeInsecure,
		UpgradeInsecureRedirects: p.upgradeInsecure,
	})
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer result.Body.Close()
	copyProxyResponseHeaders(w.Header(), result.Header)
	w.WriteHeader(result.StatusCode)
	_, _ = io.Copy(w, result.Body)
}

func (p *ffmpegForwardProxy) disableHTTPUpgrades() {
	if p != nil {
		// ponytail: job-wide latch; track generations if needed.
		p.upgradeHTTP.Store(false)
	}
}

func (p *ffmpegForwardProxy) authorized(request *http.Request) bool {
	provided := request.Header.Get("Proxy-Authorization")
	return len(provided) == len(p.authorization) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(p.authorization)) == 1
}

func (p *ffmpegForwardProxy) serveConnect(w http.ResponseWriter, request *http.Request) {
	authority := request.Host
	if authority == "" && request.URL != nil {
		authority = request.URL.Host
	}
	target, err := url.Parse("https://" + authority)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Path != "" {
		http.Error(w, "invalid CONNECT target", http.StatusBadRequest)
		return
	}
	if target.Port() == "" {
		target.Host = net.JoinHostPort(target.Hostname(), "443")
	}
	if hasFFmpegCustomHeaders(p.headers) && !sameURLOrigin(target.String(), p.headerOrigin) {
		http.Error(w, "CONNECT target crosses the authorized header origin", http.StatusForbidden)
		return
	}
	upstream, err := p.client.DialPinned(request.Context(), target.String(), p.channelID)
	if err != nil {
		http.Error(w, "upstream connection failed", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "CONNECT is unavailable", http.StatusInternalServerError)
		return
	}
	clientConnection, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = clientConnection.Close()
		_ = upstream.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		_ = clientConnection.Close()
		_ = upstream.Close()
		return
	}
	p.relay(&proxyClientConn{Conn: clientConnection, reader: buffered.Reader}, upstream)
}

func (p *ffmpegForwardProxy) relay(client, upstream net.Conn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	p.connections[client] = struct{}{}
	p.connections[upstream] = struct{}{}
	p.workers.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.workers.Done()
		done := make(chan struct{}, 2)
		go func() {
			_, _ = io.Copy(upstream, client)
			done <- struct{}{}
		}()
		go func() {
			_, _ = io.Copy(client, upstream)
			done <- struct{}{}
		}()
		<-done
		_ = client.Close()
		_ = upstream.Close()
		<-done
		p.mu.Lock()
		delete(p.connections, client)
		delete(p.connections, upstream)
		p.mu.Unlock()
	}()
}

type proxyClientConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyClientConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}

func copyProxyResponseHeaders(destination, source http.Header) {
	for name, values := range source {
		switch strings.ToLower(name) {
		case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}
