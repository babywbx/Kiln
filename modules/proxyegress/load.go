package proxyegress

import (
	"fmt"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func ConfigFromStore(db *store.DB, file config.File) (Config, error) {
	cfg := Config{
		Default:         file.Egress.Default,
		PlaylistPolicy:  PlaylistPolicy(file.Egress.PlaylistPolicy),
		DockerProxyHost: file.Egress.DockerProxyHost,
	}
	if cfg.Default == "" {
		cfg.Default = Direct
	}
	if cfg.PlaylistPolicy == "" {
		cfg.PlaylistPolicy = PolicyRewrite
	}
	if cfg.DockerProxyHost == "" {
		cfg.DockerProxyHost = "host.docker.internal"
	}
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
		profs, err := db.ListProxyProfiles()
		if err != nil {
			return Config{}, err
		}
		for _, p := range profs {
			cfg.Profiles = append(cfg.Profiles, Profile{
				ID: p.ID, Name: p.Name, URL: p.URL, Disabled: p.Disabled,
			})
		}
		rules, err := db.ListProxyRules()
		if err != nil {
			return Config{}, err
		}
		for _, r := range rules {
			cfg.Rules = append(cfg.Rules, Rule{
				ID: r.ID, Priority: r.Priority, Kind: RuleKind(r.Kind),
				Pattern: r.Pattern, ProxyID: r.ProxyID, Disabled: r.Disabled,
			})
		}
	}
	if db == nil {
		for _, p := range file.Proxies {
			cfg.Profiles = append(cfg.Profiles, Profile{
				ID: p.ID, Name: p.Name, URL: p.URL, Disabled: p.Disabled,
			})
		}
	}
	if db == nil {
		for _, r := range file.Egress.Rules {
			cfg.Rules = append(cfg.Rules, Rule{
				ID: r.ID, Priority: r.Priority, Kind: RuleKind(r.Kind),
				Pattern: r.Pattern, ProxyID: r.Proxy, Disabled: r.Disabled,
			})
		}
	}
	if cfg.Default != Direct {
		found := false
		for _, p := range cfg.Profiles {
			if p.ID == cfg.Default && !p.Disabled {
				found = true
				break
			}
		}
		if !found {
			return Config{}, fmt.Errorf("egress default %q not found among profiles", cfg.Default)
		}
	}
	return cfg, nil
}

func ConfigFromFile(file config.File) Config {
	cfg := Config{
		Default:         file.Egress.Default,
		PlaylistPolicy:  PlaylistPolicy(file.Egress.PlaylistPolicy),
		DockerProxyHost: file.Egress.DockerProxyHost,
	}
	if cfg.Default == "" {
		cfg.Default = Direct
	}
	if cfg.PlaylistPolicy == "" {
		cfg.PlaylistPolicy = PolicyRewrite
	}
	for _, p := range file.Proxies {
		cfg.Profiles = append(cfg.Profiles, Profile{
			ID: p.ID, Name: p.Name, URL: p.URL, Disabled: p.Disabled,
		})
	}
	for _, r := range file.Egress.Rules {
		cfg.Rules = append(cfg.Rules, Rule{
			ID: r.ID, Priority: r.Priority, Kind: RuleKind(r.Kind),
			Pattern: r.Pattern, ProxyID: r.Proxy, Disabled: r.Disabled,
		})
	}
	return cfg
}
