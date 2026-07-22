package proxyegress

import (
	"github.com/babywbx/kiln/modules/config"
)

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
