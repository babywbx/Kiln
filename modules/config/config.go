package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type File struct {
	Server    Server         `json:"server" toml:"server"`
	Auth      Auth           `json:"auth" toml:"auth"`
	Security  Security       `json:"security" toml:"security"`
	Upstreams []Upstream     `json:"upstreams" toml:"upstreams"`
	Channels  []Channel      `json:"channels" toml:"channels"`
	Observe   Observe        `json:"observe" toml:"observe"`
	FFmpeg    FFmpeg         `json:"ffmpeg" toml:"ffmpeg"`
	Packager  Packager       `json:"packager" toml:"packager"`
	Logging   Logging        `json:"logging" toml:"logging"`
	Proxies   []ProxyProfile `json:"proxies" toml:"proxies"`
	Egress    Egress         `json:"egress" toml:"egress"`
	EPG       EPG            `json:"epg" toml:"epg"`
}

type ProxyProfile struct {
	ID       string `json:"id" toml:"id"`
	Name     string `json:"name" toml:"name"`
	URL      string `json:"url" toml:"url"`
	Disabled bool   `json:"disabled" toml:"disabled"`
}

type Egress struct {
	Default         string       `json:"default" toml:"default"`
	PlaylistPolicy  string       `json:"playlist_policy" toml:"playlist_policy"`
	DockerProxyHost string       `json:"docker_proxy_host" toml:"docker_proxy_host"`
	Rules           []EgressRule `json:"rules" toml:"rules"`
}

type EgressRule struct {
	ID       string `json:"id" toml:"id"`
	Priority int    `json:"priority" toml:"priority"`
	Kind     string `json:"kind" toml:"kind"`
	Pattern  string `json:"pattern" toml:"pattern"`
	Proxy    string `json:"proxy" toml:"proxy"`
	Disabled bool   `json:"disabled" toml:"disabled"`
}

type EPG struct {
	Enabled            bool        `json:"enabled" toml:"enabled"`
	Cache              *bool       `json:"cache,omitempty" toml:"cache,omitempty"`
	CacheDir           string      `json:"cache_dir" toml:"cache_dir"`
	RefreshIntervalMin int         `json:"refresh_interval_min" toml:"refresh_interval_min"`
	MaxSourceBytes     int64       `json:"max_source_bytes" toml:"max_source_bytes"`
	DefaultTimezone    string      `json:"default_timezone" toml:"default_timezone"`
	ServeTimezone      string      `json:"serve_timezone" toml:"serve_timezone"`
	Sources            []EPGSource `json:"sources" toml:"sources"`
}

func (e EPG) CacheEnabled() bool {
	return e.Cache == nil || *e.Cache
}

type EPGSource struct {
	ID       string `json:"id" toml:"id"`
	Name     string `json:"name,omitempty" toml:"name,omitempty"`
	URL      string `json:"url,omitempty" toml:"url,omitempty"`
	Timezone string `json:"timezone,omitempty" toml:"timezone,omitempty"`
	Proxy    string `json:"proxy,omitempty" toml:"proxy,omitempty"`
	Enabled  bool   `json:"enabled" toml:"enabled"`
}

type Server struct {
	Listen        string `json:"listen" toml:"listen"`
	PublicBaseURL string `json:"public_base_url" toml:"public_base_url"`
	DataDir       string `json:"data_dir" toml:"data_dir"`
	ReadTimeout   int    `json:"read_timeout_sec" toml:"read_timeout_sec"`
	WriteTimeout  int    `json:"write_timeout_sec" toml:"write_timeout_sec"`
	IdleTimeout   int    `json:"idle_timeout_sec" toml:"idle_timeout_sec"`
	// MemoryLimitMB is Go's soft memory limit. Without it the runtime keeps
	// freed pages mapped, so resident memory tracks the peak rather than what is
	// actually live, which for a 4K channel is a large difference. It is soft:
	// the process is never killed for exceeding it, the collector just works
	// harder. 0 leaves the runtime default.
	MemoryLimitMB int `json:"memory_limit_mb" toml:"memory_limit_mb"`
}

type Auth struct {
	TokenPrivateKeyFile string `json:"token_private_key_file" toml:"token_private_key_file"`
	TokenPublicKeyFile  string `json:"token_public_key_file" toml:"token_public_key_file"`
	TokenPrivateKey     string `json:"token_private_key" toml:"token_private_key"`
	TokenPublicKey      string `json:"token_public_key" toml:"token_public_key"`
	TokenIssuer         string `json:"token_issuer" toml:"token_issuer"`
	TokenAudience       string `json:"token_audience" toml:"token_audience"`
	TokenTTLHours       int    `json:"token_ttl_hours" toml:"token_ttl_hours"`
	LoginRatePerMin     int    `json:"login_rate_per_min" toml:"login_rate_per_min"`
	Users               []User `json:"users" toml:"users"`
}

type User struct {
	Username     string   `json:"username" toml:"username"`
	PasswordHash string   `json:"password_hash" toml:"password_hash"`
	Role         string   `json:"role" toml:"role"`
	ChannelIDs   []string `json:"channel_ids" toml:"channel_ids"`
}

type Security struct {
	PlayRequireAuth  bool     `json:"play_require_auth" toml:"play_require_auth"`
	AllowedHosts     []string `json:"allowed_hosts" toml:"allowed_hosts"`
	MaxPlaylistBytes int64    `json:"max_playlist_bytes" toml:"max_playlist_bytes"`
	MaxBodyBytes     int64    `json:"max_body_bytes" toml:"max_body_bytes"`
	CORSOrigins      []string `json:"cors_origins" toml:"cors_origins"`
	PublicHosts      []string `json:"public_hosts" toml:"public_hosts"`
}

type Upstream struct {
	ID      string            `json:"id" toml:"id"`
	BaseURL string            `json:"base_url" toml:"base_url"`
	Proxy   string            `json:"proxy" toml:"proxy"`
	Headers map[string]string `json:"headers" toml:"headers"`
}

type Channel struct {
	ID             string `json:"id" toml:"id"`
	Title          string `json:"title" toml:"title"`
	Group          string `json:"group" toml:"group"`
	LogoURL        string `json:"logo_url" toml:"logo_url"`
	EPGID          string `json:"epg_id" toml:"epg_id"`
	EPGName        string `json:"epg_name" toml:"epg_name"`
	EPGSource      string `json:"epg_source" toml:"epg_source"`
	Upstream       string `json:"upstream" toml:"upstream"`
	Path           string `json:"path" toml:"path"`
	Ingress        string `json:"ingress" toml:"ingress"`
	Disabled       bool   `json:"disabled" toml:"disabled"`
	OnDemand       bool   `json:"on_demand" toml:"on_demand"`
	Autostart      bool   `json:"autostart" toml:"autostart"`
	IdleTimeoutSec int    `json:"idle_timeout_sec" toml:"idle_timeout_sec"`
	MaxViewers     int    `json:"max_viewers" toml:"max_viewers"`
	KeysFile       string `json:"keys_file" toml:"keys_file"`
	// Keys holds kid:key lines inline. A deployed server often has no convenient
	// path to drop a file at, so the admin form writes them here instead. It
	// takes precedence over KeysFile when both are set.
	Keys                    string            `json:"keys" toml:"keys"`
	UserAgent               string            `json:"user_agent" toml:"user_agent"`
	Headers                 map[string]string `json:"headers" toml:"headers"`
	RestartOnFailure        bool              `json:"restart_on_failure" toml:"restart_on_failure"`
	PreferHeight            int               `json:"prefer_height" toml:"prefer_height"`
	PreferredAudioLanguages []string          `json:"preferred_audio_languages,omitempty" toml:"preferred_audio_languages,omitempty"`
	// Packager overrides the global engine strategy for this channel.
	Packager string `json:"packager" toml:"packager"`
}

type Observe struct {
	Enabled          bool    `json:"enabled" toml:"enabled"`
	OTLPEndpoint     string  `json:"otlp_endpoint,omitempty" toml:"otlp_endpoint,omitempty"`
	OTLPInsecure     bool    `json:"otlp_insecure,omitempty" toml:"otlp_insecure,omitempty"`
	TraceSampleRatio float64 `json:"trace_sample_ratio,omitempty" toml:"trace_sample_ratio,omitempty"`
	ServiceName      string  `json:"service_name,omitempty" toml:"service_name,omitempty"`
}

type FFmpegMode string

const (
	FFmpegModeNative FFmpegMode = "native"
	FFmpegModeDocker FFmpegMode = "docker"
)

func (m FFmpegMode) Valid() bool {
	return m == FFmpegModeNative || m == FFmpegModeDocker
}

func (m FFmpegMode) IsDocker() bool {
	return m == FFmpegModeDocker
}

type FFmpeg struct {
	Binary       string     `json:"binary" toml:"binary"`
	Mode         FFmpegMode `json:"mode" toml:"mode"`
	DockerImage  string     `json:"docker_image" toml:"docker_image"`
	HLSTime      int        `json:"hls_time" toml:"hls_time"`
	HLSListSize  int        `json:"hls_list_size" toml:"hls_list_size"`
	LogLevel     string     `json:"log_level" toml:"log_level"`
	PreferHeight int        `json:"prefer_height" toml:"prefer_height"`
	LowLatency   bool       `json:"low_latency" toml:"low_latency"`
	// MaxStarts bounds concurrent ffmpeg launches only, not readiness waits.
	MaxStarts int `json:"max_starts" toml:"max_starts"`
}

// Packager configures the DASH execution engine. Engine, output container and
// codec targets are separate axes on purpose: a single use_ffmpeg flag cannot
// express "copy natively but transcode HEVC".
type Packager struct {
	// Engine is the default strategy: auto | native | ffmpeg.
	Engine           string `json:"engine" toml:"engine"`
	PlaylistSize     int    `json:"playlist_size" toml:"playlist_size"`
	LLHLS            bool   `json:"ll_hls" toml:"ll_hls"`
	PartTargetMS     int    `json:"part_target_ms" toml:"part_target_ms"`
	StartSegments    int    `json:"start_segments" toml:"start_segments"`
	PrefetchSegments int    `json:"prefetch_segments" toml:"prefetch_segments"`
	MaxSegmentBytes  int64  `json:"max_segment_bytes" toml:"max_segment_bytes"`
	// GraceSec keeps a segment readable after it leaves the playlist, so a
	// player holding the previous playlist does not get a hard 404.
	GraceSec int `json:"grace_sec" toml:"grace_sec"`
	// PrimaryTrackHoldSec bounds how far, in media time, audio may run ahead of
	// video. It is not an A/V sync knob: the tracks keep their own segment
	// boundaries. It stops audio from sliding its playlist window past what a
	// player stalled on video still needs.
	PrimaryTrackHoldSec int `json:"primary_track_hold_sec" toml:"primary_track_hold_sec"`
	// StallTimeoutSec fails a publication whose manifest keeps updating while
	// nothing reaches the playlist. -1 disables it.
	StallTimeoutSec int `json:"stall_timeout_sec" toml:"stall_timeout_sec"`
	// InflightBytes bounds the segment bytes held in memory across every channel
	// at once. This is what decides peak RSS: a 4K segment is tens of megabytes,
	// so limiting prefetch by segment count instead would make memory a function
	// of the source's bitrate.
	InflightBytes int64 `json:"inflight_bytes" toml:"inflight_bytes"`
}

const (
	EngineAuto   = "auto"
	EngineNative = "native"
	EngineFFmpeg = "ffmpeg"
)

func ValidEngine(s string) bool {
	switch s {
	case EngineAuto, EngineNative, EngineFFmpeg:
		return true
	}
	return false
}

func (f FFmpeg) Dependency() string {
	if f.Mode.IsDocker() {
		return "docker"
	}
	return f.Binary
}

type Logging struct {
	Level  string `json:"level" toml:"level"`
	Format string `json:"format" toml:"format"` // text | json
	Color  string `json:"color" toml:"color"`   // auto | always | never (text only)
}

func (c File) TokenTTL() time.Duration {
	h := c.Auth.TokenTTLHours
	if h <= 0 {
		h = 24
	}
	return time.Duration(h) * time.Hour
}

// EngineFor resolves the engine strategy for a channel, falling back to the
// global default.
func (c File) EngineFor(ch Channel) string {
	if ValidEngine(ch.Packager) {
		return ch.Packager
	}
	if ValidEngine(c.Packager.Engine) {
		return c.Packager.Engine
	}
	return EngineAuto
}

func (c File) IdleTimeout(ch Channel) time.Duration {
	sec := ch.IdleTimeoutSec
	if sec <= 0 {
		sec = 90
	}
	return time.Duration(sec) * time.Second
}

func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	var cfg File
	switch ext {
	case ".toml":
		if err := toml.Unmarshal(raw, &cfg); err != nil {
			return File{}, fmt.Errorf("parse toml: %w", err)
		}
	case ".json", ".jsonc":
		if err := json.Unmarshal(StripJSONC(raw), &cfg); err != nil {
			return File{}, fmt.Errorf("parse jsonc: %w", err)
		}
	default:
		return File{}, fmt.Errorf("unsupported config extension %q (use .toml .json .jsonc)", ext)
	}
	cfg.applyEnvOverrides()
	cfg.applyDefaults()
	cfg.resolveKeysPaths(path)
	if err := cfg.validate(); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func (c *File) applyEnvOverrides() {
	if v := os.Getenv("KILN_TOKEN_PRIVATE_KEY"); v != "" {
		c.Auth.TokenPrivateKey = v
	}
	if v := os.Getenv("KILN_TOKEN_PUBLIC_KEY"); v != "" {
		c.Auth.TokenPublicKey = v
	}
	if v := os.Getenv("KILN_TOKEN_PRIVATE_KEY_FILE"); v != "" {
		c.Auth.TokenPrivateKeyFile = v
	}
	if v := os.Getenv("KILN_TOKEN_PUBLIC_KEY_FILE"); v != "" {
		c.Auth.TokenPublicKeyFile = v
	}
	if v := os.Getenv("KILN_LISTEN"); v != "" {
		c.Server.Listen = v
	}
	if v := os.Getenv("KILN_PUBLIC_BASE_URL"); v != "" {
		c.Server.PublicBaseURL = v
	}
	if v := os.Getenv("KILN_DATA_DIR"); v != "" {
		c.Server.DataDir = v
	}
	if v := os.Getenv("KILN_LOG_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("KILN_LOG_FORMAT"); v != "" {
		c.Logging.Format = v
	}
	if v := os.Getenv("KILN_LOG_COLOR"); v != "" {
		c.Logging.Color = v
	}
	switch os.Getenv("KILN_PLAY_OPEN") {
	case "1", "true", "TRUE":
		c.Security.PlayRequireAuth = false
	case "0", "false", "FALSE":
		c.Security.PlayRequireAuth = true
	}
}

func (c *File) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = "0.0.0.0:8080"
	}
	if c.Server.DataDir == "" {
		c.Server.DataDir = "./data"
	}
	if c.Server.PublicBaseURL == "" {
		c.Server.PublicBaseURL = "http://127.0.0.1:8080"
	}
	c.Server.PublicBaseURL = strings.TrimRight(c.Server.PublicBaseURL, "/")
	if c.Server.ReadTimeout <= 0 {
		c.Server.ReadTimeout = 15
	}
	if c.Server.IdleTimeout <= 0 {
		c.Server.IdleTimeout = 120
	}
	if c.Auth.TokenTTLHours <= 0 {
		c.Auth.TokenTTLHours = 24
	}
	if c.Auth.LoginRatePerMin <= 0 {
		c.Auth.LoginRatePerMin = 20
	}
	if c.Auth.TokenIssuer == "" {
		c.Auth.TokenIssuer = "kiln"
	}
	if c.Auth.TokenAudience == "" {
		c.Auth.TokenAudience = "kiln"
	}
	if c.FFmpeg.Binary == "" {
		c.FFmpeg.Binary = "ffmpeg"
	}
	if c.FFmpeg.Mode == "" {
		c.FFmpeg.Mode = FFmpegModeNative
	}
	if c.FFmpeg.DockerImage == "" {
		c.FFmpeg.DockerImage = "kiln:local"
	}
	if c.FFmpeg.HLSTime <= 0 {
		c.FFmpeg.HLSTime = 2
	}
	if c.FFmpeg.HLSListSize <= 0 {
		c.FFmpeg.HLSListSize = 8
	}
	if c.FFmpeg.LogLevel == "" {
		c.FFmpeg.LogLevel = "error"
	}
	if !ValidEngine(c.Packager.Engine) {
		c.Packager.Engine = EngineAuto
	}
	if c.Packager.PlaylistSize <= 0 {
		c.Packager.PlaylistSize = 8
	}
	if c.Packager.PartTargetMS <= 0 {
		c.Packager.PartTargetMS = 500
	}
	if c.Packager.StartSegments <= 0 {
		c.Packager.StartSegments = 3
	}
	if c.Packager.PrefetchSegments <= 0 {
		c.Packager.PrefetchSegments = 3
	}
	if c.Packager.MaxSegmentBytes <= 0 {
		c.Packager.MaxSegmentBytes = 32 << 20
	}
	if c.Packager.GraceSec <= 0 {
		c.Packager.GraceSec = 30
	}
	if c.Packager.PrimaryTrackHoldSec <= 0 {
		c.Packager.PrimaryTrackHoldSec = 12
	}
	if c.Packager.StallTimeoutSec == 0 {
		c.Packager.StallTimeoutSec = 180
	}
	if c.Packager.InflightBytes <= 0 {
		c.Packager.InflightBytes = 96 << 20
	}
	c.Observe.Enabled = true
	if c.Observe.TraceSampleRatio <= 0 {
		c.Observe.TraceSampleRatio = 1
	}
	if c.Observe.ServiceName == "" {
		c.Observe.ServiceName = "kiln"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if c.Logging.Color == "" {
		c.Logging.Color = "auto"
	}
	if os.Getenv("KILN_PLAY_OPEN") == "" {
		c.Security.PlayRequireAuth = true
	}
	if c.Security.MaxPlaylistBytes <= 0 {
		c.Security.MaxPlaylistBytes = 8 << 20
	}
	if c.Security.MaxBodyBytes <= 0 {
		c.Security.MaxBodyBytes = 1 << 20
	}
	if c.Egress.Default == "" {
		c.Egress.Default = "direct"
	}
	if c.Egress.PlaylistPolicy == "" {
		c.Egress.PlaylistPolicy = "rewrite"
	}
	if c.Egress.DockerProxyHost == "" {
		c.Egress.DockerProxyHost = "host.docker.internal"
	}
	if c.EPG.Cache == nil {
		cache := true
		c.EPG.Cache = &cache
	}
	if c.EPG.CacheDir == "" {
		c.EPG.CacheDir = filepath.Join(c.Server.DataDir, "epg")
	}
	if c.EPG.RefreshIntervalMin <= 0 {
		c.EPG.RefreshIntervalMin = 360
	}
	if c.EPG.MaxSourceBytes <= 0 {
		c.EPG.MaxSourceBytes = 64 << 20
	}
	if c.EPG.DefaultTimezone == "" {
		c.EPG.DefaultTimezone = "Asia/Hong_Kong"
	}
	if c.EPG.ServeTimezone == "" {
		c.EPG.ServeTimezone = "keep"
	}
	for i := range c.EPG.Sources {
		if c.EPG.Sources[i].Proxy == "" {
			c.EPG.Sources[i].Proxy = "auto"
		}
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
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
	}
}

func (c *File) resolveKeysPaths(configPath string) {
	cfgDir := filepath.Dir(configPath)
	for i := range c.Channels {
		kf := c.Channels[i].KeysFile
		if kf == "" || filepath.IsAbs(kf) {
			continue
		}
		if _, err := os.Stat(kf); err == nil {
			continue
		}
		candidates := []string{
			filepath.Join(cfgDir, kf),
			filepath.Join(cfgDir, filepath.Base(kf)),
		}
		for _, alt := range candidates {
			if _, err := os.Stat(alt); err == nil {
				c.Channels[i].KeysFile = alt
				break
			}
		}
	}
}

func (c File) validate() error {
	if !c.FFmpeg.Mode.Valid() {
		return fmt.Errorf("ffmpeg.mode must be native or docker")
	}
	if len(c.Auth.Users) == 0 {
		return fmt.Errorf("auth.users must not be empty")
	}
	if c.Server.DataDir == "" &&
		c.Auth.TokenPrivateKey == "" &&
		c.Auth.TokenPrivateKeyFile == "" {
		return fmt.Errorf("auth requires token_private_key, token_private_key_file, or server.data_dir for auto-managed Ed25519 keys")
	}
	users := map[string]struct{}{}
	for _, u := range c.Auth.Users {
		if u.Username == "" || u.PasswordHash == "" {
			return fmt.Errorf("auth.users require username and password_hash")
		}
		if _, ok := users[u.Username]; ok {
			return fmt.Errorf("duplicate username %q", u.Username)
		}
		users[u.Username] = struct{}{}
		if u.Role == "" {
			return fmt.Errorf("user %q requires role", u.Username)
		}
	}
	up := map[string]Upstream{}
	for _, u := range c.Upstreams {
		if u.ID == "" || u.BaseURL == "" {
			return fmt.Errorf("upstream requires id and base_url")
		}
		if _, err := url.ParseRequestURI(u.BaseURL); err != nil {
			return fmt.Errorf("upstream %q base_url invalid: %w", u.ID, err)
		}
		if u.Proxy != "" {
			if _, err := url.ParseRequestURI(u.Proxy); err != nil {
				return fmt.Errorf("upstream %q proxy invalid: %w", u.ID, err)
			}
		}
		up[u.ID] = u
	}
	seen := map[string]struct{}{}
	for _, ch := range c.Channels {
		if ch.ID == "" {
			return fmt.Errorf("channel id is required")
		}
		if _, ok := seen[ch.ID]; ok {
			return fmt.Errorf("duplicate channel id %q", ch.ID)
		}
		seen[ch.ID] = struct{}{}
		if _, ok := up[ch.Upstream]; !ok {
			return fmt.Errorf("channel %q references unknown upstream %q", ch.ID, ch.Upstream)
		}
		switch ch.Ingress {
		case "hls", "dash":
		default:
			return fmt.Errorf("channel %q ingress must be hls or dash", ch.ID)
		}
		if ch.Path == "" {
			return fmt.Errorf("channel %q path is required", ch.ID)
		}
		if ch.Ingress == "dash" && !ch.Disabled && ch.KeysFile == "" && ch.Keys == "" {
			return fmt.Errorf("channel %q dash ingress requires keys or keys_file", ch.ID)
		}
		if ch.Keys != "" {
			if _, err := ParseKeys(ch.Keys); err != nil {
				return fmt.Errorf("channel %q keys: %w", ch.ID, err)
			}
		}
		if ch.Packager != "" && !ValidEngine(ch.Packager) {
			return fmt.Errorf("channel %q packager must be auto, native or ffmpeg", ch.ID)
		}
	}
	proxyIDs := map[string]struct{}{"direct": {}}
	for _, p := range c.Proxies {
		if p.ID == "" || p.URL == "" {
			return fmt.Errorf("proxies require id and url")
		}
		if _, err := url.ParseRequestURI(p.URL); err != nil {
			u, e2 := url.Parse(p.URL)
			if e2 != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("proxy %q url invalid: %v", p.ID, err)
			}
		}
		proxyIDs[p.ID] = struct{}{}
	}
	if _, ok := proxyIDs[c.Egress.Default]; !ok {
		return fmt.Errorf("egress.default unknown proxy %q", c.Egress.Default)
	}
	switch c.Egress.PlaylistPolicy {
	case "rewrite", "passthrough", "auto":
	default:
		return fmt.Errorf("egress.playlist_policy must be rewrite|passthrough|auto")
	}
	for _, rule := range c.Egress.Rules {
		if rule.Proxy == "" {
			return fmt.Errorf("egress rule requires proxy")
		}
		if _, ok := proxyIDs[rule.Proxy]; !ok {
			return fmt.Errorf("egress rule references unknown proxy %q", rule.Proxy)
		}
	}
	if c.EPG.ServeTimezone != "keep" {
		return fmt.Errorf("epg.serve_timezone must be keep")
	}
	if c.Packager.PartTargetMS < 100 || c.Packager.PartTargetMS > 5000 {
		return fmt.Errorf("packager.part_target_ms must be between 100 and 5000")
	}
	if c.Observe.TraceSampleRatio < 0 || c.Observe.TraceSampleRatio > 1 {
		return fmt.Errorf("observe.trace_sample_ratio must be between 0 and 1")
	}
	if c.Observe.OTLPEndpoint != "" {
		u, err := url.ParseRequestURI(c.Observe.OTLPEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("observe.otlp_endpoint invalid")
		}
	}
	if _, err := time.LoadLocation(c.EPG.DefaultTimezone); err != nil {
		return fmt.Errorf("epg.default_timezone invalid: %w", err)
	}
	epgIDs := map[string]struct{}{}
	for _, source := range c.EPG.Sources {
		if source.ID == "" {
			return fmt.Errorf("epg source requires id")
		}
		if _, exists := epgIDs[source.ID]; exists {
			return fmt.Errorf("duplicate epg source id %q", source.ID)
		}
		epgIDs[source.ID] = struct{}{}
		if source.URL != "" {
			u, err := url.ParseRequestURI(source.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("epg source %q url invalid", source.ID)
			}
		}
		if source.Timezone != "" {
			if _, err := time.LoadLocation(source.Timezone); err != nil {
				return fmt.Errorf("epg source %q timezone invalid: %w", source.ID, err)
			}
		}
		if source.Proxy != "auto" {
			if _, ok := proxyIDs[source.Proxy]; !ok {
				return fmt.Errorf("epg source %q references unknown proxy %q", source.ID, source.Proxy)
			}
		}
	}
	return nil
}

func (c File) UpstreamByID(id string) (Upstream, bool) {
	for _, u := range c.Upstreams {
		if u.ID == id {
			return u, true
		}
	}
	return Upstream{}, false
}

func (c File) ChannelByID(id string) (Channel, bool) {
	for _, ch := range c.Channels {
		if ch.ID == id {
			return ch, true
		}
	}
	return Channel{}, false
}

func (c File) ActiveChannels() []Channel {
	out := make([]Channel, 0, len(c.Channels))
	for _, ch := range c.Channels {
		if !ch.Disabled {
			out = append(out, ch)
		}
	}
	return out
}

func (c File) AllowedHostSet() map[string]struct{} {
	out := map[string]struct{}{}
	for _, h := range c.Security.AllowedHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			out[h] = struct{}{}
		}
	}
	for _, u := range c.Upstreams {
		pu, err := url.Parse(u.BaseURL)
		if err != nil {
			continue
		}
		host := strings.ToLower(pu.Hostname())
		if host != "" {
			out[host] = struct{}{}
		}
	}
	return out
}
