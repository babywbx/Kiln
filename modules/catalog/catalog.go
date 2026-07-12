package catalog

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

type Service struct {
	cfg config.File
	db  *store.DB
	mu  sync.RWMutex
}

type ChannelView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Group     string `json:"group,omitempty"`
	LogoURL   string `json:"logo_url,omitempty"`
	Ingress   string `json:"ingress"`
	OnDemand  bool   `json:"on_demand"`
	Autostart bool   `json:"autostart"`
	Upstream  string `json:"upstream"`
	Path      string `json:"path,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	KeysFile  string `json:"keys_file,omitempty"`
	PreferH   int    `json:"prefer_height,omitempty"`
	SortOrder int    `json:"sort_order"`
	Revision  int64  `json:"revision,omitempty"`
	PlayURL   string `json:"play_url,omitempty"`
}

func New(cfg config.File, db *store.DB) *Service {
	return &Service{cfg: cfg, db: db}
}

func (s *Service) Config() config.File { return s.cfg }
func (s *Service) DB() *store.DB       { return s.db }

func (s *Service) PublicBase() string {
	if s.db != nil {
		if v, ok, err := s.db.GetSetting("public_base_url"); err == nil && ok && strings.TrimSpace(v) != "" {
			return strings.TrimRight(strings.TrimSpace(v), "/")
		}
	}
	return s.cfg.Server.PublicBaseURL
}

func (s *Service) List(includeDisabled bool) ([]config.Channel, error) {
	if s.db == nil {
		if includeDisabled {
			return append([]config.Channel(nil), s.cfg.Channels...), nil
		}
		return s.cfg.ActiveChannels(), nil
	}
	return s.db.ListChannels(includeDisabled)
}

func (s *Service) ListViews(publicBase string, includeDisabled bool) ([]ChannelView, error) {
	if s.db != nil {
		rows, err := s.db.ListChannelRows(includeDisabled)
		if err != nil {
			return nil, err
		}
		out := make([]ChannelView, 0, len(rows))
		for _, row := range rows {
			ch := row.Channel
			title := ch.Title
			if title == "" {
				title = ch.ID
			}
			view := ChannelView{
				ID: ch.ID, Title: title, Group: ch.Group, LogoURL: ch.LogoURL,
				Ingress: ch.Ingress, OnDemand: ch.OnDemand, Autostart: ch.Autostart, Upstream: ch.Upstream,
				Path: ch.Path, Disabled: ch.Disabled, KeysFile: ch.KeysFile, PreferH: ch.PreferHeight,
				SortOrder: row.SortOrder, Revision: row.Revision,
			}
			if publicBase != "" && !ch.Disabled {
				view.PlayURL = publicBase + "/v1/play/" + ch.ID + "/index.m3u8"
			}
			out = append(out, view)
		}
		return out, nil
	}
	chs, err := s.List(includeDisabled)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelView, 0, len(chs))
	for i, ch := range chs {
		title := ch.Title
		if title == "" {
			title = ch.ID
		}
		view := ChannelView{
			ID: ch.ID, Title: title, Group: ch.Group, LogoURL: ch.LogoURL,
			Ingress: ch.Ingress, OnDemand: ch.OnDemand, Autostart: ch.Autostart, Upstream: ch.Upstream,
			Path: ch.Path, Disabled: ch.Disabled, KeysFile: ch.KeysFile, PreferH: ch.PreferHeight,
			SortOrder: i,
		}
		if publicBase != "" && !ch.Disabled {
			view.PlayURL = publicBase + "/v1/play/" + ch.ID + "/index.m3u8"
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Service) Reorder(ids []string) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	return s.db.ReorderChannels(ids)
}

func (s *Service) ReorderIfRevisions(ids []string, revisions map[string]int64) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	return s.db.ReorderChannelsIfRevisions(ids, revisions)
}

func (s *Service) Get(id string) (config.Channel, bool) {
	if s.db != nil {
		ch, ok, err := s.db.GetChannel(id)
		if err != nil || !ok || ch.Disabled {
			return config.Channel{}, false
		}
		return ch, true
	}
	ch, ok := s.cfg.ChannelByID(id)
	if !ok || ch.Disabled {
		return config.Channel{}, false
	}
	return ch, true
}

func (s *Service) GetAny(id string) (config.Channel, bool) {
	if s.db != nil {
		ch, ok, err := s.db.GetChannel(id)
		if err != nil || !ok {
			return config.Channel{}, false
		}
		return ch, true
	}
	return s.cfg.ChannelByID(id)
}

func (s *Service) Upsert(ch config.Channel) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	ch = normalizeChannel(ch)
	if err := store.ValidateChannel(ch, s.cfg.Upstreams); err != nil {
		return err
	}
	return s.db.UpsertChannel(ch)
}

func (s *Service) UpsertIfRevision(ch config.Channel, expectedRevision int64) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	ch = normalizeChannel(ch)
	if err := store.ValidateChannel(ch, s.cfg.Upstreams); err != nil {
		return err
	}
	return s.db.UpsertChannelIfRevision(ch, expectedRevision)
}

func (s *Service) UpsertBatchIfRevisions(channels []config.Channel, revisions map[string]int64) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	normalized := make([]config.Channel, 0, len(channels))
	for _, ch := range channels {
		ch = normalizeChannel(ch)
		if err := store.ValidateChannel(ch, s.cfg.Upstreams); err != nil {
			return err
		}
		normalized = append(normalized, ch)
	}
	return s.db.UpsertChannelsIfRevisions(normalized, revisions)
}

func normalizeChannel(ch config.Channel) config.Channel {
	if ch.Ingress == "" {
		ch.Ingress = "hls"
	}
	ch.Ingress = strings.ToLower(ch.Ingress)
	if ch.IdleTimeoutSec <= 0 {
		ch.IdleTimeoutSec = 90
	}
	if ch.Ingress == "dash" {
		ch.RestartOnFailure = true
	}
	if !ch.OnDemand && !ch.Autostart {
		ch.OnDemand = true
	}
	return ch
}

func (s *Service) Delete(id string) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	return s.db.DeleteChannel(id)
}

func (s *Service) DeleteIfRevision(id string, expectedRevision int64) error {
	if s.db == nil {
		return fmt.Errorf("store not available")
	}
	return s.db.DeleteChannelIfRevision(id, expectedRevision)
}

func (s *Service) SourceURL(ch config.Channel) (string, error) {
	up, ok := s.cfg.UpstreamByID(ch.Upstream)
	if !ok {
		return "", apperr.New(apperr.CodeInternal, 500, "unknown upstream")
	}
	return JoinURL(up.BaseURL, ch.Path), nil
}

func (s *Service) Upstream(ch config.Channel) (config.Upstream, error) {
	up, ok := s.cfg.UpstreamByID(ch.Upstream)
	if !ok {
		return config.Upstream{}, apperr.New(apperr.CodeInternal, 500, "unknown upstream")
	}
	return up, nil
}

func (s *Service) Upstreams() []config.Upstream {
	return append([]config.Upstream(nil), s.cfg.Upstreams...)
}

func (s *Service) M3U(channels []config.Channel, publicBase, playPathPrefix, token string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, ch := range channels {
		if ch.Disabled {
			continue
		}
		title := ch.Title
		if title == "" {
			title = ch.ID
		}
		b.WriteString("#EXTINF:-1")
		if ch.Group != "" {
			b.WriteString(fmt.Sprintf(` group-title="%s"`, escapeAttr(ch.Group)))
		}
		if ch.LogoURL != "" {
			b.WriteString(fmt.Sprintf(` tvg-logo="%s"`, escapeAttr(ch.LogoURL)))
		}
		b.WriteString(fmt.Sprintf(` tvg-id="%s",%s`, escapeAttr(ch.ID), title))
		b.WriteByte('\n')
		u := publicBase + playPathPrefix + ch.ID + "/index.m3u8"
		if token != "" {
			u += "?token=" + url.QueryEscape(token)
		}
		b.WriteString(u)
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *Service) FilterByIDs(all []config.Channel, ids []string) []config.Channel {
	if len(ids) == 0 {
		return all
	}
	allow := map[string]struct{}{}
	for _, id := range ids {
		allow[id] = struct{}{}
	}
	out := make([]config.Channel, 0, len(ids))
	for _, ch := range all {
		if _, ok := allow[ch.ID]; ok {
			out = append(out, ch)
		}
	}
	return out
}

func escapeAttr(s string) string {
	return strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ").Replace(s)
}

func JoinURL(base, p string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return base + p
}

func ResolveRef(baseURL, ref string) (string, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(ref, "/") {
		u.Path = ref
		u.RawQuery = ""
		u.Fragment = ""
		return u.String(), nil
	}
	dir := path.Dir(u.Path)
	if dir == "." {
		dir = "/"
	}
	u.Path = path.Join(dir, ref)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
