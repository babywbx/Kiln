package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/egress"
	"github.com/babywbx/kiln/modules/filecache"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/security"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/version"
)

const llhlsWaitTimeout = 15 * time.Second

type Catalog interface {
	Get(string) (config.Channel, bool)
}

type Deps struct {
	Cfg       config.File
	Catalog   Catalog
	Sessions  *session.Manager
	Observe   *observe.Service
	Egress    *proxyegress.Router
	Log       *slog.Logger
	Allowed   map[string]struct{}
	Authorize func(*http.Request, string) error
	Token     func(*http.Request) string
}

type Handler struct {
	deps Deps
}

func New(deps Deps) *Handler {
	if deps.Observe == nil {
		deps.Observe = observe.New()
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Authorize == nil {
		deps.Authorize = func(*http.Request, string) error { return nil }
	}
	if deps.Token == nil {
		deps.Token = func(*http.Request) string { return "" }
	}
	return &Handler{deps: deps}
}

func (h *Handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.PathValue("token") == "" {
		if err := h.deps.Authorize(r, id); err != nil {
			writeAppError(w, err)
			return
		}
	}
	active, err := h.deps.Sessions.Acquire(id)
	if err != nil {
		h.deps.Observe.IncError()
		h.deps.Log.Warn("playback acquire failed", "channel", id, "err", err)
		writeAppError(w, err)
		return
	}
	_, _, _, mode := active.SourceSnapshot()
	switch mode {
	case "hls":
		h.serveHLSIndex(w, r, active)
	case "dash":
		h.serveDASHIndex(w, r, active)
	default:
		writeAppError(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "unsupported ingress"))
	}
}

func (h *Handler) serveHLSIndex(w http.ResponseWriter, r *http.Request, active *session.Session) {
	channel, sourceURL, _, _ := active.SourceSnapshot()
	body, finalURL, err := h.deps.Sessions.Pull().GetBytes(r.Context(), pull.Request{
		URL:       mergeHLSDeliveryDirectives(sourceURL, r.URL.Query()),
		UserAgent: version.UserAgent(channel.UserAgent),
		Headers:   h.deps.Sessions.HeadersFor(channel),
		ChannelID: channel.ID,
	})
	if err != nil {
		h.deps.Observe.IncError()
		writeAppError(w, err)
		return
	}
	token := h.deps.Token(r)
	prefix := "/v1/play/" + channel.ID + "/u/"
	if pathToken := r.PathValue("token"); pathToken != "" && accesstoken.Valid(pathToken) {
		prefix = "/p/" + pathToken + "/play/" + channel.ID + "/u/"
		token = ""
	}
	rewritten, err := egress.RewritePlaylist(
		string(body), finalURL, prefix, h.deps.Allowed, h.shouldRewrite(channel.ID),
	)
	if err != nil {
		h.deps.Observe.IncError()
		writeAppError(w, apperr.Internal(err))
		return
	}
	if token != "" {
		rewritten = appendTokenToPlaylistURLs(rewritten, token)
	}
	h.deps.Observe.AddBytesOut(int64(len(rewritten)))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, rewritten)
}

func (h *Handler) shouldRewrite(channelID string) egress.RewriteDecision {
	return func(absolute string) bool {
		return h.deps.Egress == nil || h.deps.Egress.ShouldRewriteURL(absolute, channelID)
	}
}

func (h *Handler) serveDASHIndex(w http.ResponseWriter, r *http.Request, active *session.Session) {
	publication, generation := active.PublicationSnapshot()
	if publication == nil {
		h.deps.Observe.IncError()
		writeAppError(w, apperr.New(apperr.CodeNotReady, http.StatusBadGateway, "playlist not ready"))
		return
	}
	body, ok := publication.Playlist(publication.Master())
	if !ok {
		h.deps.Observe.IncError()
		writeAppError(w, apperr.New(apperr.CodeNotReady, http.StatusBadGateway, "playlist not ready"))
		return
	}
	h.writePlaylist(w, r, active, body, generation)
}

func (h *Handler) writePlaylist(w http.ResponseWriter, r *http.Request, active *session.Session, body []byte, generation string) {
	channel, _, _, _ := active.SourceSnapshot()
	out := RewriteLocalPlaylist(body, playLivePrefix(r, channel.ID), h.deps.Token(r), generation)
	h.deps.Observe.AddBytesOut(int64(len(out)))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

func playLivePrefix(r *http.Request, channelID string) string {
	if token := r.PathValue("token"); token != "" && accesstoken.Valid(token) {
		return "/p/" + token + "/play/" + channelID + "/live/"
	}
	return "/v1/play/" + channelID + "/live/"
}

func (h *Handler) HandleLiveFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fileName := path.Base(r.PathValue("file"))
	if !safeFileName(fileName) {
		writeAppError(w, apperr.ErrNotFound)
		return
	}
	if r.PathValue("token") == "" {
		if err := h.deps.Authorize(r, id); err != nil {
			writeAppError(w, err)
			return
		}
	}
	active, ok := h.deps.Sessions.Get(id)
	if !ok {
		writeAppError(w, apperr.New(apperr.CodeNotFound, http.StatusGone, "session is not running"))
		return
	}
	publication, generation := active.PublicationSnapshot()
	requested := r.URL.Query().Get("g")
	if generation != "" && (requested == "" || requested != generation && strings.HasSuffix(fileName, ".m3u8")) {
		redirectToPublicationGeneration(w, r, generation)
		return
	}
	if requested != "" && requested != generation {
		w.Header().Set("Retry-After", "1")
		writeAppError(w, apperr.New(apperr.CodeNotFound, http.StatusGone, "publication generation is gone"))
		return
	}
	h.deps.Sessions.Touch(id)
	if publication == nil {
		writeAppError(w, apperr.ErrNotFound)
		return
	}
	if strings.HasSuffix(fileName, ".m3u8") {
		body, found, err := playlistForRequest(r, publication, fileName)
		if err != nil {
			writeAppError(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, err.Error()))
			return
		}
		if !found {
			writeAppError(w, apperr.ErrNotFound)
			return
		}
		h.writePlaylist(w, r, active, body, generation)
		return
	}
	asset, found := publication.Asset(fileName)
	if !found {
		if contextual, supportsContext := publication.(packager.ContextPublication); supportsContext {
			var err error
			waitContext, cancel := context.WithTimeout(r.Context(), llhlsWaitTimeout)
			asset, found, err = contextual.AssetContext(waitContext, fileName)
			cancel()
			if err != nil {
				if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
					return
				}
				if errors.Is(err, context.DeadlineExceeded) {
					h.deps.Observe.IncError()
					w.Header().Set("Retry-After", "1")
					writeAppError(w, apperr.New(apperr.CodeUnavailable, http.StatusServiceUnavailable, "media part not ready"))
					return
				}
				writeAppError(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, err.Error()))
				return
			}
		}
	}
	if !found {
		writeAppError(w, apperr.ErrNotFound)
		return
	}
	file, err := os.Open(asset.Path)
	if err != nil {
		writeAppError(w, apperr.ErrNotFound)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		writeAppError(w, apperr.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(fileName))
	setAssetCacheHeaders(w, asset.Immutable)
	counter := &bodyCountWriter{ResponseWriter: w}
	http.ServeContent(counter, r, fileName, asset.ModTime, file)
	filecache.DropAfterRead(file)
	h.deps.Observe.AddBytesOut(counter.written)
}

func (h *Handler) HandleUpstream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	absolute, err := egress.DecodeUpstream(r.PathValue("upstream"))
	if err != nil {
		writeAppError(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "bad upstream"))
		return
	}
	if err := security.MediaHostOK(absolute, h.deps.Allowed); err != nil {
		writeAppError(w, apperr.New(apperr.CodeForbidden, http.StatusForbidden, "upstream host not allowed"))
		return
	}
	absolute = mergeHLSDeliveryDirectives(absolute, r.URL.Query())
	if r.PathValue("token") == "" {
		if err := h.deps.Authorize(r, id); err != nil {
			writeAppError(w, err)
			return
		}
	}
	channel, ok := h.deps.Catalog.Get(id)
	if !ok {
		writeAppError(w, session.ErrNotFound)
		return
	}
	if _, err := h.deps.Sessions.Acquire(id); err != nil {
		writeAppError(w, err)
		return
	}
	response, err := h.deps.Sessions.Pull().Get(r.Context(), pull.Request{
		URL: absolute, UserAgent: version.UserAgent(channel.UserAgent),
		Headers: h.deps.Sessions.HeadersFor(channel), ChannelID: id,
	})
	if err != nil {
		h.deps.Observe.IncError()
		writeAppError(w, err)
		return
	}
	defer response.Body.Close()
	contentType := response.ContentType
	if contentType == "" {
		contentType = contentTypeFor(absolute)
	}
	if strings.Contains(contentType, "mpegurl") || strings.HasSuffix(strings.Split(absolute, "?")[0], ".m3u8") {
		maxPlaylistBytes := h.deps.Cfg.Security.MaxPlaylistBytes
		if maxPlaylistBytes <= 0 {
			maxPlaylistBytes = 8 << 20
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, maxPlaylistBytes+1))
		if err != nil {
			writeAppError(w, apperr.Wrap(apperr.CodeUpstream, http.StatusBadGateway, "read playlist failed", err))
			return
		}
		if int64(len(body)) > maxPlaylistBytes {
			writeAppError(w, apperr.New(apperr.CodeUpstream, http.StatusBadGateway, "playlist too large"))
			return
		}
		token := h.deps.Token(r)
		prefix := "/v1/play/" + id + "/u/"
		if pathToken := r.PathValue("token"); pathToken != "" && accesstoken.Valid(pathToken) {
			prefix = "/p/" + pathToken + "/play/" + id + "/u/"
			token = ""
		}
		out, err := egress.RewritePlaylist(string(body), response.FinalURL, prefix, h.deps.Allowed, h.shouldRewrite(id))
		if err != nil {
			writeAppError(w, apperr.Internal(err))
			return
		}
		if token != "" {
			out = appendTokenToPlaylistURLs(out, token)
		}
		h.deps.Observe.AddBytesOut(int64(len(out)))
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, out)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	stream := &commitTrackingWriter{ResponseWriter: w}
	written, copyErr := io.Copy(stream, response.Body)
	h.deps.Observe.AddBytesOut(written)
	if copyErr != nil {
		h.deps.Observe.IncError()
		h.deps.Log.Warn("stream upstream body failed", "channel", id, "err", copyErr)
		if !stream.committed {
			writeAppError(w, apperr.Wrap(apperr.CodeUpstream, http.StatusBadGateway, "read upstream body failed", copyErr))
			return
		}
		panic(http.ErrAbortHandler)
	}
}

func RewriteLocalPlaylist(body []byte, prefix, token, generation string) []byte {
	lines := strings.Split(string(body), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "#"):
			lines[index] = rewriteTagURI(line, prefix, token, generation)
		default:
			lines[index] = localURL(trimmed, prefix, token, generation)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteTagURI(line, prefix, token, generation string) string {
	const key = `URI="`
	index := strings.Index(line, key)
	if index < 0 {
		return line
	}
	start := index + len(key)
	end := strings.IndexByte(line[start:], '"')
	if end < 0 {
		return line
	}
	reference := line[start : start+end]
	if reference == "" {
		return line
	}
	return line[:start] + localURL(reference, prefix, token, generation) + line[start+end:]
}

func localURL(reference, prefix, token, generation string) string {
	name := strings.TrimSpace(reference)
	if name != path.Base(name) || !safeFileName(name) {
		return reference
	}
	result := prefix + name
	query := url.Values{}
	if generation != "" {
		query.Set("g", generation)
	}
	if token != "" && !strings.HasPrefix(prefix, "/p/") {
		query.Set("token", token)
	}
	if encoded := query.Encode(); encoded != "" {
		result += "?" + encoded
	}
	return result
}

func ParseHLSPlaylistRequest(r *http.Request) (packager.PlaylistRequest, bool, error) {
	query := r.URL.Query()
	request := packager.PlaylistRequest{}
	lowLatency := false
	if raw, present := query["_HLS_skip"]; present {
		lowLatency = true
		value := ""
		if len(raw) > 0 {
			value = strings.ToUpper(strings.TrimSpace(raw[0]))
		}
		if value != "YES" && value != "V2" {
			return request, true, fmt.Errorf("_HLS_skip must be YES or v2")
		}
		request.Skip = true
	}
	parseUint := func(key string) (*uint64, error) {
		raw, present := query[key]
		if !present {
			return nil, nil
		}
		lowLatency = true
		if len(raw) == 0 || strings.TrimSpace(raw[0]) == "" {
			return nil, fmt.Errorf("%s requires an unsigned integer", key)
		}
		value, err := strconv.ParseUint(raw[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s requires an unsigned integer", key)
		}
		return &value, nil
	}
	var err error
	request.MSN, err = parseUint("_HLS_msn")
	if err != nil {
		return request, lowLatency, err
	}
	request.Part, err = parseUint("_HLS_part")
	if err != nil {
		return request, lowLatency, err
	}
	if request.Part != nil && request.MSN == nil {
		return request, lowLatency, fmt.Errorf("_HLS_part requires _HLS_msn")
	}
	return request, lowLatency, nil
}

func playlistForRequest(r *http.Request, publication packager.Publication, name string) ([]byte, bool, error) {
	request, lowLatency, err := ParseHLSPlaylistRequest(r)
	if err != nil {
		return nil, false, err
	}
	contextual, supportsContext := publication.(packager.ContextPublication)
	if !lowLatency || !supportsContext {
		body, ok := publication.Playlist(name)
		return body, ok, nil
	}
	waitContext, cancel := context.WithTimeout(r.Context(), llhlsWaitTimeout)
	view, ok, err := contextual.PlaylistContext(waitContext, name, request)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
		body, found := publication.Playlist(name)
		return body, found, nil
	}
	if err != nil {
		return nil, ok, err
	}
	return view.Body, ok, nil
}

func redirectToPublicationGeneration(w http.ResponseWriter, r *http.Request, generation string) {
	target := *r.URL
	query := target.Query()
	query.Set("g", generation)
	target.RawQuery = query.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
}

func setAssetCacheHeaders(w http.ResponseWriter, immutable bool) {
	w.Header().Del("Pragma")
	if !immutable {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
}

func contentTypeFor(name string) string {
	name = strings.ToLower(strings.Split(name, "?")[0])
	switch {
	case strings.HasSuffix(name, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(name, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(name, ".m4s"), strings.HasSuffix(name, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(name, ".aac"):
		return "audio/aac"
	case strings.HasSuffix(name, ".vtt"):
		return "text/vtt"
	default:
		return "application/octet-stream"
	}
}

func appendTokenToPlaylistURLs(playlist, token string) string {
	lines := strings.Split(playlist, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, `URI="`) {
				lines[index] = injectTokenInURITag(trimmed, token)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "/") {
			lines[index] = appendQuery(trimmed, "token", token)
		}
	}
	return strings.Join(lines, "\n")
}

func injectTokenInURITag(tag, token string) string {
	const key = `URI="`
	index := strings.Index(tag, key)
	if index < 0 {
		return tag
	}
	start := index + len(key)
	end := strings.Index(tag[start:], `"`)
	if end < 0 {
		return tag
	}
	uri := tag[start : start+end]
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "/") {
		uri = appendQuery(uri, "token", token)
	}
	return tag[:start] + uri + tag[start+end:]
}

func appendQuery(raw, key, value string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		separator := "?"
		if strings.Contains(raw, "?") {
			separator = "&"
		}
		return raw + separator + key + "=" + url.QueryEscape(value)
	}
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func mergeHLSDeliveryDirectives(raw string, requestQuery url.Values) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	for _, key := range []string{"_HLS_msn", "_HLS_part", "_HLS_skip"} {
		if value := requestQuery.Get(key); value != "" {
			query.Set(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func safeFileName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.Contains(name, "/") && !strings.Contains(name, "\\") && !strings.Contains(name, "..")
}

func writeAppError(w http.ResponseWriter, err error) {
	code, status, message := apperr.PublicMessage(err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": string(code), "message": message},
	})
}

type bodyCountWriter struct {
	http.ResponseWriter
	written int64
}

func (w *bodyCountWriter) Write(body []byte) (int, error) {
	written, err := w.ResponseWriter.Write(body)
	w.written += int64(written)
	return written, err
}

type commitTrackingWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *commitTrackingWriter) Write(body []byte) (int, error) {
	written, err := w.ResponseWriter.Write(body)
	if written > 0 {
		w.committed = true
	}
	return written, err
}
