//go:build !lite

package proxyegress

import (
	"fmt"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func ConfigFromStore(db *store.DB, file config.File) (Config, error) {
	cfg := ConfigFromFile(file)
	if db != nil {
		if v, ok, err := db.GetSetting("egress_default"); err == nil && ok && v != "" {
			cfg.Default = v
		}
		if v, ok, err := db.GetSetting("playlist_policy"); err == nil && ok && v != "" {
			cfg.PlaylistPolicy = PlaylistPolicy(v)
		}
		if v, ok, err := db.GetSetting("docker_proxy_host"); err == nil && ok && v != "" {
			cfg.DockerProxyHost = v
		}
		profiles, err := db.ListProxyProfiles()
		if err != nil {
			return Config{}, err
		}
		cfg.Profiles = cfg.Profiles[:0]
		for _, profile := range profiles {
			cfg.Profiles = append(cfg.Profiles, Profile{
				ID: profile.ID, Name: profile.Name, URL: profile.URL, Disabled: profile.Disabled,
			})
		}
		rules, err := db.ListProxyRules()
		if err != nil {
			return Config{}, err
		}
		cfg.Rules = cfg.Rules[:0]
		for _, rule := range rules {
			cfg.Rules = append(cfg.Rules, Rule{
				ID: rule.ID, Priority: rule.Priority, Kind: RuleKind(rule.Kind),
				Pattern: rule.Pattern, ProxyID: rule.ProxyID, Disabled: rule.Disabled,
			})
		}
	}
	if cfg.Default != Direct {
		for _, profile := range cfg.Profiles {
			if profile.ID == cfg.Default && !profile.Disabled {
				return cfg, nil
			}
		}
		return Config{}, fmt.Errorf("egress default %q not found among profiles", cfg.Default)
	}
	return cfg, nil
}
