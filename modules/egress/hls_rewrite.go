package egress

import (
	"net/url"
	"strings"

	"github.com/babywbx/kiln/modules/mediaurl"
	"github.com/babywbx/kiln/modules/security"
)

type RewriteDecision func(absURL string) bool

func RewritePlaylist(playlist, playlistURL, proxyPrefix string, allowedPrivate map[string]struct{}, shouldRewrite RewriteDecision) (string, error) {
	if shouldRewrite == nil {
		shouldRewrite = func(string) bool { return true }
	}
	lines := strings.Split(playlist, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "#") {
			if strings.Contains(trim, `URI="`) {
				rewritten, err := rewriteTagURI(trim, playlistURL, proxyPrefix, allowedPrivate, shouldRewrite)
				if err != nil {
					return "", err
				}
				out = append(out, rewritten)
				continue
			}
			out = append(out, line)
			continue
		}
		abs, err := mediaurl.ResolveRef(playlistURL, trim)
		if err != nil {
			return "", err
		}
		out = append(out, mapURL(abs, proxyPrefix, allowedPrivate, shouldRewrite))
	}
	return strings.Join(out, "\n"), nil
}

func rewriteTagURI(tag, playlistURL, proxyPrefix string, allowedPrivate map[string]struct{}, shouldRewrite RewriteDecision) (string, error) {
	const key = `URI="`
	idx := strings.Index(tag, key)
	if idx < 0 {
		return tag, nil
	}
	start := idx + len(key)
	end := strings.Index(tag[start:], `"`)
	if end < 0 {
		return tag, nil
	}
	uri := tag[start : start+end]
	abs, err := mediaurl.ResolveRef(playlistURL, uri)
	if err != nil {
		return "", err
	}
	return tag[:start] + mapURL(abs, proxyPrefix, allowedPrivate, shouldRewrite) + tag[start+end:], nil
}

func mapURL(abs, proxyPrefix string, allowedPrivate map[string]struct{}, shouldRewrite RewriteDecision) string {
	if err := security.MediaHostOK(abs, allowedPrivate); err != nil {
		if err2 := security.HostAllowed(abs, allowedPrivate); err2 != nil {
			return abs
		}
	}
	if shouldRewrite != nil && !shouldRewrite(abs) {
		return abs
	}
	return proxyPrefix + url.PathEscape(abs)
}

func DecodeUpstream(escaped string) (string, error) {
	return url.PathUnescape(escaped)
}
