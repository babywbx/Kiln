package epg_test

import (
	"regexp"
	"slices"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

func TestPresetsContainVerifiedSourcesAndReturnIndependentCopies(t *testing.T) {
	t.Parallel()

	wantIDs := map[string]bool{
		"hk-1":     false,
		"tw-1":     false,
		"cn-1":     false,
		"global-1": false,
		"cn-2":     false,
		"cn-3":     false,
		"cn-4":     false,
	}

	got := epg.Presets()
	for _, source := range got {
		if _, ok := wantIDs[source.ID]; ok {
			wantIDs[source.ID] = true
		}
		if source.ID == "" || source.Name == "" || source.URL == "" || source.Timezone == "" {
			t.Fatalf("preset has incomplete metadata: %+v", source)
		}
	}
	for id, found := range wantIDs {
		if !found {
			t.Errorf("missing preset %q", id)
		}
	}

	got[0].Name = "mutated"
	if epg.Presets()[0].Name == "mutated" {
		t.Fatal("Presets returned shared mutable state")
	}
}

func TestPresetDescriptionsDoNotUseBroadcasterNamesAsExamples(t *testing.T) {
	t.Parallel()

	broadcasterName := regexp.MustCompile(`(?i)CCTV|TVBS?|BBC`)
	for _, source := range epg.Presets() {
		if broadcasterName.MatchString(source.Description) {
			t.Fatalf("preset %q description names a broadcaster: %q", source.ID, source.Description)
		}
	}
}

func TestConfigureSourcesMergesPresetsAndSortsCustomSources(t *testing.T) {
	t.Parallel()

	overrides := []epg.SourceOverride{
		{ID: "cn-2", Name: "备用 EPG", Enabled: true, Revision: 7, UpdatedAt: 123},
		{ID: "z-custom", URL: "https://z.example/epg.xml", Enabled: true},
		{ID: "a-custom", URL: "https://a.example/epg.xml", Timezone: "Asia/Taipei", Proxy: "lan-http"},
	}
	configured := epg.ConfigureSources(overrides)
	presets := epg.Presets()
	if len(configured) != len(presets)+2 {
		t.Fatalf("configured source count = %d, want %d", len(configured), len(presets)+2)
	}
	if got := configured[len(presets)].Source.ID; got != "a-custom" {
		t.Fatalf("first custom source = %q, want a-custom", got)
	}
	if got := configured[len(presets)+1].Source.ID; got != "z-custom" {
		t.Fatalf("second custom source = %q, want z-custom", got)
	}

	presetIDs := make([]string, 0, len(presets))
	configuredPresetIDs := make([]string, 0, len(presets))
	for index, preset := range presets {
		presetIDs = append(presetIDs, preset.ID)
		configuredPresetIDs = append(configuredPresetIDs, configured[index].Source.ID)
	}
	if !slices.Equal(configuredPresetIDs, presetIDs) {
		t.Fatalf("preset order = %v, want %v", configuredPresetIDs, presetIDs)
	}

	var backup epg.ConfiguredSource
	for _, source := range configured {
		if source.Source.ID == "cn-2" {
			backup = source
		}
		if source.Source.Proxy == "" {
			t.Errorf("source %q has empty proxy", source.Source.ID)
		}
	}
	if backup.Source.Name != "备用 EPG" || backup.Source.URL == "" || backup.Source.Proxy != "direct" {
		t.Fatalf("merged preset = %+v", backup)
	}
	if !backup.Enabled || backup.Revision != 7 || backup.UpdatedAt != 123 {
		t.Fatalf("merged metadata = %+v", backup)
	}
}

func TestConfigureSourcesStartsDisabledAndKeepsDeletedSourcesHidden(t *testing.T) {
	t.Parallel()

	configured := epg.ConfigureSources(nil)
	presets := epg.Presets()
	if len(configured) != len(presets) {
		t.Fatalf("new install sources = %d, want %d presets", len(configured), len(presets))
	}
	for index, source := range configured {
		if source.Source.ID != presets[index].ID {
			t.Fatalf("source %d = %q, want %q", index, source.Source.ID, presets[index].ID)
		}
		if source.Enabled || source.Revision != 0 {
			t.Fatalf("new install source = %+v, want disabled pristine preset", source)
		}
		if source.Source.Proxy != "direct" {
			t.Fatalf("new install source %q proxy = %q, want direct", source.Source.ID, source.Source.Proxy)
		}
	}

	configured = epg.ConfigureSources([]epg.SourceOverride{
		{ID: presets[0].ID, Deleted: true, Revision: 1},
		{ID: "removed-custom", URL: "https://example.test/removed.xml", Deleted: true, Revision: 1},
		{ID: "visible-custom", URL: "https://example.test/visible.xml"},
	})
	for _, source := range configured {
		if source.Source.ID == presets[0].ID || source.Source.ID == "removed-custom" {
			t.Fatalf("deleted source remained configured: %+v", source)
		}
	}
	if got := configured[len(configured)-1].Source.ID; got != "visible-custom" {
		t.Fatalf("last source = %q, want visible-custom", got)
	}
}
