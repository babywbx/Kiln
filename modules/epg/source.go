package epg

import "sort"

// IDKind describes the stable identifier format used by a source.
type IDKind string

const (
	IDKindNumeric IDKind = "numeric"
	IDKindName    IDKind = "name"
	IDKindMixed   IDKind = "mixed"
)

// Source is an XMLTV endpoint. Proxy is an opaque routing hint interpreted by
// the caller when it constructs the HTTP client used by Fetcher.
type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Region      string `json:"region,omitempty"`
	IDKind      IDKind `json:"id_kind"`
	Timezone    string `json:"timezone"`
	ApproxBytes int64  `json:"approx_bytes,omitempty"`
	Description string `json:"description"`
	Proxy       string `json:"proxy,omitempty"`
}

type SourceOverride struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Proxy     string `json:"proxy,omitempty"`
	Enabled   bool   `json:"enabled"`
	Revision  int64  `json:"revision,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type ConfiguredSource struct {
	Source    Source `json:"source"`
	Enabled   bool   `json:"enabled"`
	Revision  int64  `json:"revision,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

var presetSources = []Source{
	{
		ID: "hk-1", Name: "香港源 1", URL: "https://epg.pw/xmltv/epg_HK.xml.gz",
		Region: "HK", IDKind: IDKindNumeric, Timezone: "Asia/Hong_Kong", ApproxBytes: 1_700_000,
		Description: "香港地区 XMLTV 节目单",
	},
	{
		ID: "tw-1", Name: "台湾源 1", URL: "https://epg.pw/xmltv/epg_TW.xml.gz",
		Region: "TW", IDKind: IDKindNumeric, Timezone: "Asia/Taipei", ApproxBytes: 2_100_000,
		Description: "台湾地区 XMLTV 节目单",
	},
	{
		ID: "cn-1", Name: "大陆源 1", URL: "https://epg.pw/xmltv/epg_CN.xml.gz",
		Region: "CN", IDKind: IDKindNumeric, Timezone: "Asia/Shanghai", ApproxBytes: 524_000,
		Description: "中国大陆地区 XMLTV 节目单",
	},
	{
		ID: "global-1", Name: "全球源 1", URL: "https://epg.pw/xmltv/epg.xml.gz",
		Region: "global", IDKind: IDKindNumeric, Timezone: "Asia/Hong_Kong", ApproxBytes: 45_600_000,
		Description: "全球 XMLTV 节目单，文件较大",
	},
	{
		ID: "cn-2", Name: "大陆源 2", URL: "http://epg.51zmt.top:8000/e2.xml.gz",
		Region: "CN/HK/TW", IDKind: IDKindNumeric, Timezone: "Asia/Shanghai", ApproxBytes: 704_000,
		Description: "使用简体中文频道名称的 XMLTV 备选源",
	},
	{
		ID: "cn-3", Name: "cn-3", URL: "https://epg.112114.xyz/pp.xml.gz",
		Region: "CN", IDKind: IDKindName, Timezone: "Asia/Shanghai", ApproxBytes: 201_000,
		Description: "使用频道名称作为 ID 的轻量 XMLTV 备选源",
	},
	{
		ID: "cn-4", Name: "cn-4", URL: "https://live.fanmingming.com/e.xml",
		Region: "CN", IDKind: IDKindName, Timezone: "Asia/Shanghai", ApproxBytes: 7_600_000,
		Description: "未压缩的 XMLTV 备选源",
	},
}

// Presets returns a copy of the built-in, verified source catalog.
func Presets() []Source {
	return append([]Source(nil), presetSources...)
}

// Preset returns a built-in source by ID.
func Preset(id string) (Source, bool) {
	for _, source := range presetSources {
		if source.ID == id {
			return source, true
		}
	}
	return Source{}, false
}

// ConfigureSources merges persisted overrides with all built-in presets.
// Presets keep catalog order; custom sources follow in stable ID order.
func ConfigureSources(overrides []SourceOverride) []ConfiguredSource {
	byID := make(map[string]SourceOverride, len(overrides))
	for _, override := range overrides {
		if override.ID != "" {
			byID[override.ID] = override
		}
	}

	configured := make([]ConfiguredSource, 0, len(presetSources)+len(byID))
	for _, preset := range presetSources {
		preset.Proxy = "auto"
		override, exists := byID[preset.ID]
		if exists {
			preset = mergeSourceOverride(preset, override)
			delete(byID, preset.ID)
		}
		configured = append(configured, ConfiguredSource{
			Source: preset, Enabled: override.Enabled,
			Revision: override.Revision, UpdatedAt: override.UpdatedAt,
		})
	}

	customIDs := make([]string, 0, len(byID))
	for id := range byID {
		customIDs = append(customIDs, id)
	}
	sort.Strings(customIDs)
	for _, id := range customIDs {
		override := byID[id]
		source := Source{
			ID: id, Name: id, Region: "custom", IDKind: IDKindMixed,
			Timezone: DefaultTimezone, Description: "自定义节目单源", Proxy: "auto",
		}
		source = mergeSourceOverride(source, override)
		configured = append(configured, ConfiguredSource{
			Source: source, Enabled: override.Enabled,
			Revision: override.Revision, UpdatedAt: override.UpdatedAt,
		})
	}
	return configured
}

func mergeSourceOverride(source Source, override SourceOverride) Source {
	if override.Name != "" {
		source.Name = override.Name
	}
	if override.URL != "" {
		source.URL = override.URL
	}
	if override.Timezone != "" {
		source.Timezone = override.Timezone
	}
	if override.Proxy != "" {
		source.Proxy = override.Proxy
	}
	return source
}
