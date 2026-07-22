package channelconfig

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
)

type M3UOptions struct {
	PublicBase     string
	PlayPathPrefix string
	Token          string
	EPGURL         string
	FallbackLogo   func(config.Channel, string) string
}

func SourceURL(file config.File, channel config.Channel) (string, error) {
	if raw := strings.TrimSpace(channel.SourceURL); raw != "" {
		if err := config.ValidateSourceURL(raw); err != nil {
			return "", apperr.Wrap(apperr.CodeInternal, 500, "invalid source url", err)
		}
		return raw, nil
	}
	upstream, ok := file.UpstreamByID(channel.Upstream)
	if !ok {
		return "", apperr.New(apperr.CodeInternal, 500, "unknown upstream")
	}
	return JoinURL(upstream.BaseURL, channel.Path), nil
}

func Upstream(file config.File, channel config.Channel) (config.Upstream, error) {
	if strings.TrimSpace(channel.SourceURL) != "" {
		return config.Upstream{}, nil
	}
	upstream, ok := file.UpstreamByID(channel.Upstream)
	if !ok {
		return config.Upstream{}, apperr.New(apperr.CodeInternal, 500, "unknown upstream")
	}
	return upstream, nil
}

func FilterByIDs(channels []config.Channel, ids []string) []config.Channel {
	if len(ids) == 0 {
		return channels
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	filtered := make([]config.Channel, 0, len(ids))
	for _, channel := range channels {
		if _, ok := allowed[channel.ID]; ok {
			filtered = append(filtered, channel)
		}
	}
	return filtered
}

func M3U(channels []config.Channel, options M3UOptions) string {
	var body strings.Builder
	body.WriteString("#EXTM3U")
	if options.EPGURL != "" {
		fmt.Fprintf(&body, ` x-tvg-url="%s"`, escapeAttribute(options.EPGURL))
	}
	body.WriteByte('\n')
	for _, channel := range channels {
		if channel.Disabled {
			continue
		}
		title := channel.Title
		if title == "" {
			title = channel.ID
		}
		body.WriteString("#EXTINF:-1")
		if channel.Group != "" {
			fmt.Fprintf(&body, ` group-title="%s"`, escapeAttribute(channel.Group))
		}
		logoURL := channel.LogoURL
		if logoURL == "" && options.FallbackLogo != nil {
			logoURL = options.FallbackLogo(channel, title)
		}
		if logoURL != "" {
			fmt.Fprintf(&body, ` tvg-logo="%s"`, escapeAttribute(logoURL))
		}
		epgName := channel.EPGName
		if epgName == "" {
			epgName = title
		}
		fmt.Fprintf(&body, ` tvg-id="%s" tvg-name="%s",%s`,
			escapeAttribute(channel.ID), escapeAttribute(epgName), title)
		body.WriteByte('\n')
		body.WriteString(options.PublicBase + options.PlayPathPrefix + channel.ID + "/index.m3u8")
		if options.Token != "" {
			body.WriteString("?token=" + url.QueryEscape(options.Token))
		}
		body.WriteByte('\n')
	}
	return body.String()
}

func JoinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func escapeAttribute(value string) string {
	return strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ").Replace(value)
}
