//go:build !lite

package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/epg"
	"github.com/babywbx/kiln/modules/store"
)

func (s *Server) handleEPGXML(w http.ResponseWriter, r *http.Request) {
	s.serveEPG(w, r, false)
}

func (s *Server) handleEPGGzip(w http.ResponseWriter, r *http.Request) {
	s.serveEPG(w, r, true)
}

func (s *Server) handleChannelLogo(w http.ResponseWriter, r *http.Request) {
	if s.deps.Catalog == nil {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	channel, ok := s.deps.Catalog.GetAny(r.PathValue("id"))
	if !ok {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	name := channel.EPGName
	if name == "" {
		name = channel.Title
	}
	candidates := epg.LogoCandidates(name)
	if len(candidates) == 0 {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	client := &http.Client{Timeout: 15 * time.Second}
	if s.deps.Egress != nil {
		var err error
		client, err = s.deps.Egress.ClientForChannel("", channel.ID, 15*time.Second)
		if err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
	}
	logo, err := epg.FetchLogo(r.Context(), client, candidates, epg.DefaultMaxLogoBytes)
	if err != nil {
		s.deps.Log.Warn("all channel logo sources failed", "channel", channel.ID, "err", err)
		writeAppErr(w, apperr.New(apperr.CodeUpstream, http.StatusBadGateway, "channel logo unavailable"))
		return
	}
	w.Header().Set("Content-Type", logo.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=3600, stale-if-error=86400")
	w.Header().Set("X-Kiln-Logo-Source", logo.SourceID)
	_, _ = w.Write(logo.Data)
}

func (s *Server) serveEPG(w http.ResponseWriter, r *http.Request, compressed bool) {
	channels := s.epgChannels()
	if s.epgActive() && !s.deps.Cfg.EPG.CacheEnabled() {
		if err := s.deps.EPG.Refresh(r.Context()); err != nil {
			s.deps.Log.Warn("EPG on-demand refresh failed", "err", err)
		}
	}
	payload, err := s.epgPayload(channels, compressed)
	if err != nil {
		s.deps.Log.Error("render EPG failed", "err", err)
		payload = emptyEPGPayload(compressed)
	}
	if compressed {
		w.Header().Set("Content-Type", "application/gzip")
	} else {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (s *Server) epgPayload(channels []epg.ChannelRef, compressed bool) ([]byte, error) {
	if !s.epgActive() {
		return emptyEPGPayload(compressed), nil
	}
	if compressed {
		return s.deps.EPG.GzipXML(channels)
	}
	return s.deps.EPG.XML(channels)
}

func (s *Server) epgActive() bool {
	return s.deps.EPG != nil && len(s.deps.EPG.Sources()) > 0
}

func emptyEPGPayload(compressed bool) []byte {
	service := epg.NewService(epg.ServiceConfig{}, nil, nil)
	if compressed {
		payload, _ := service.GzipXML(nil)
		return payload
	}
	payload, _ := service.XML(nil)
	return payload
}

func (s *Server) epgChannels() []epg.ChannelRef {
	if s.deps.Catalog == nil {
		return nil
	}
	channels, err := s.deps.Catalog.List(false)
	if err != nil {
		s.deps.Log.Warn("list channels for EPG failed", "err", err)
		return nil
	}
	refs := make([]epg.ChannelRef, 0, len(channels))
	for _, channel := range channels {
		refs = append(refs, epg.ChannelRef{
			ID: channel.ID, Title: channel.Title, LogoURL: channel.LogoURL,
			EPGID: channel.EPGID, EPGName: channel.EPGName, EPGSource: channel.EPGSource,
		})
	}
	return refs
}

func (s *Server) handleAdminEPGPresets(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": epg.Presets()})
}

func (s *Server) handleAdminEPGSources(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	configured, err := s.configuredEPGSources()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	statuses := []epg.SourceStatus{}
	if s.deps.EPG != nil {
		statuses = s.deps.EPG.Statuses()
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": configured, "statuses": statuses})
}

func (s *Server) handleAdminEPGMatches(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	matches := []epg.MatchResult{}
	if s.deps.EPG != nil {
		matches = s.deps.EPG.Matches(s.epgChannels())
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
}

func (s *Server) handleAdminEPGRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.epgActive() {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "enable at least one EPG source before refreshing"))
		return
	}
	err := s.deps.EPG.Refresh(r.Context())
	if err != nil {
		s.deps.Log.Warn("manual EPG refresh completed with source errors", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "statuses": s.deps.EPG.Statuses()})
}

type epgSourceRequest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Timezone string `json:"timezone"`
	Proxy    string `json:"proxy"`
	Enabled  bool   `json:"enabled"`
}

func (s *Server) handleAdminCreateEPGSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	request, ok := s.decodeEPGSourceRequest(w, r)
	if !ok {
		return
	}
	if err := s.validateEPGSourceRequest(request); err != nil {
		writeAppErr(w, err)
		return
	}
	exists, err := s.epgSourceOverrideExists(request.ID)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if exists {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "EPG source already exists"))
		return
	}
	if err := s.deps.Store.UpsertEPGSource(request.storeRow()); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	s.respondEPGSourceMutation(w, http.StatusCreated, request.ID)
}

func (s *Server) handleAdminUpdateEPGSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	request, ok := s.decodeEPGSourceRequest(w, r)
	if !ok {
		return
	}
	request.ID = strings.TrimSpace(r.PathValue("id"))
	if err := s.validateEPGSourceRequest(request); err != nil {
		writeAppErr(w, err)
		return
	}
	expected := expectedRevision(r)
	if expected <= 0 {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusPreconditionRequired, "If-Match revision required"))
		return
	}
	err := s.deps.Store.UpsertEPGSourceIfRevision(request.storeRow(), expected)
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "EPG source was updated elsewhere"))
		return
	}
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	s.respondEPGSourceMutation(w, http.StatusOK, request.ID)
}

func (s *Server) handleAdminDeleteEPGSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "EPG source id required"))
		return
	}
	expected := expectedRevision(r)
	_, preset := epg.Preset(id)
	if expected <= 0 && !preset {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusPreconditionRequired, "If-Match revision required"))
		return
	}
	var err error
	if preset {
		err = s.deps.Store.HideEPGSourceIfRevision(id, expected)
	} else {
		err = s.deps.Store.DeleteEPGSourceIfRevision(id, expected)
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "EPG source was updated elsewhere"))
		return
	}
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if _, err := s.reloadEPGSources(); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) decodeEPGSourceRequest(w http.ResponseWriter, r *http.Request) (epgSourceRequest, bool) {
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, http.StatusInternalServerError, "store unavailable"))
		return epgSourceRequest{}, false
	}
	var request epgSourceRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "invalid json body"))
		return epgSourceRequest{}, false
	}
	request.ID = strings.TrimSpace(request.ID)
	request.Name = strings.TrimSpace(request.Name)
	request.URL = strings.TrimSpace(request.URL)
	request.Timezone = strings.TrimSpace(request.Timezone)
	request.Proxy = strings.TrimSpace(request.Proxy)
	if request.Proxy == "" {
		request.Proxy = "direct"
	}
	return request, true
}

func (s *Server) validateEPGSourceRequest(request epgSourceRequest) error {
	if request.ID == "" {
		return apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "EPG source id required")
	}
	effectiveURL := request.URL
	effectiveTimezone := request.Timezone
	if preset, ok := epg.Preset(request.ID); ok {
		if effectiveURL == "" {
			effectiveURL = preset.URL
		}
		if effectiveTimezone == "" {
			effectiveTimezone = preset.Timezone
		}
	}
	parsed, err := url.ParseRequestURI(effectiveURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "EPG source URL invalid")
	}
	if effectiveTimezone != "" {
		if _, err := time.LoadLocation(effectiveTimezone); err != nil {
			return apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "EPG source timezone invalid")
		}
	}
	if request.Proxy != "auto" && s.deps.Egress != nil {
		if _, err := s.deps.Egress.ClientForProxy(request.Proxy, time.Second); err != nil {
			return apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "EPG source proxy invalid")
		}
	}
	return nil
}

func (request epgSourceRequest) storeRow() store.EPGSourceRow {
	return store.EPGSourceRow{
		ID: request.ID, Name: request.Name, URL: request.URL, Timezone: request.Timezone,
		Proxy: request.Proxy, Enabled: request.Enabled,
	}
}

func (s *Server) epgSourceOverrideExists(id string) (bool, error) {
	rows, err := s.deps.Store.ListEPGSources()
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) respondEPGSourceMutation(w http.ResponseWriter, status int, id string) {
	configured, err := s.reloadEPGSources()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	for _, source := range configured {
		if source.Source.ID == id {
			writeJSON(w, status, map[string]any{"ok": true, "source": source})
			return
		}
	}
	writeAppErr(w, apperr.ErrNotFound)
}

func (s *Server) reloadEPGSources() ([]epg.ConfiguredSource, error) {
	configured, err := s.configuredEPGSources()
	if err != nil {
		return nil, err
	}
	active := make([]epg.Source, 0, len(configured))
	for _, source := range configured {
		if source.Enabled {
			active = append(active, source.Source)
		}
	}
	if s.deps.EPG != nil {
		s.deps.EPG.SetSources(active)
	}
	return configured, nil
}

func (s *Server) configuredEPGSources() ([]epg.ConfiguredSource, error) {
	if s.deps.Store == nil {
		return nil, nil
	}
	rows, err := s.deps.Store.ListEPGSources()
	if err != nil {
		return nil, err
	}
	overrides := make([]epg.SourceOverride, 0, len(rows))
	for _, row := range rows {
		overrides = append(overrides, epg.SourceOverride{
			ID: row.ID, Name: row.Name, URL: row.URL, Timezone: row.Timezone, Proxy: row.Proxy,
			Enabled: row.Enabled, Deleted: row.Deleted,
			Revision: row.Revision, UpdatedAt: row.UpdatedAt,
		})
	}
	return epg.ConfigureSources(overrides), nil
}

func epgPublicURL(publicBase string) string {
	return strings.TrimRight(strings.TrimSpace(publicBase), "/") + "/v1/epg.xml.gz"
}
