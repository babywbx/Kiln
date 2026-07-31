package egress

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/babywbx/kiln/modules/mediaurl"
	"github.com/babywbx/kiln/modules/security"
)

type RewriteDecision func(absURL string) bool

type UpstreamSigner func(absURL string) string

func RewritePlaylist(
	playlist, playlistURL, proxyPrefix string,
	allowedPrivate map[string]struct{},
	shouldRewrite RewriteDecision,
	sign UpstreamSigner,
) (string, error) {
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
				rewritten, err := rewriteTagURI(trim, playlistURL, proxyPrefix, allowedPrivate, shouldRewrite, sign)
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
		out = append(out, mapURL(abs, proxyPrefix, allowedPrivate, shouldRewrite, sign))
	}
	return strings.Join(out, "\n"), nil
}

func rewriteTagURI(
	tag, playlistURL, proxyPrefix string,
	allowedPrivate map[string]struct{},
	shouldRewrite RewriteDecision,
	sign UpstreamSigner,
) (string, error) {
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
	return tag[:start] + mapURL(abs, proxyPrefix, allowedPrivate, shouldRewrite, sign) + tag[start+end:], nil
}

func mapURL(
	abs, proxyPrefix string,
	allowedPrivate map[string]struct{},
	shouldRewrite RewriteDecision,
	sign UpstreamSigner,
) string {
	if err := security.MediaHostOK(abs, allowedPrivate); err != nil {
		if err2 := security.HostAllowed(abs, allowedPrivate); err2 != nil {
			return abs
		}
	}
	if shouldRewrite != nil && !shouldRewrite(abs) {
		return abs
	}
	if sign == nil {
		return abs
	}
	signature := sign(abs)
	if signature == "" {
		return abs
	}
	return proxyPrefix + EncodeUpstream(abs) + "?sig=" + url.QueryEscape(signature)
}

func EncodeUpstream(absolute string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(absolute))
}

func DecodeUpstream(encoded string) (string, error) {
	absolute, err := base64.RawURLEncoding.DecodeString(encoded)
	return string(absolute), err
}
