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
		mapped, err := mapURL(abs, proxyPrefix, allowedPrivate, shouldRewrite, sign)
		if err != nil {
			return "", err
		}
		out = append(out, mapped)
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
	mapped, err := mapURL(abs, proxyPrefix, allowedPrivate, shouldRewrite, sign)
	if err != nil {
		return "", err
	}
	return tag[:start] + mapped + tag[start+end:], nil
}

func mapURL(
	abs, proxyPrefix string,
	allowedPrivate map[string]struct{},
	shouldRewrite RewriteDecision,
	sign UpstreamSigner,
) (string, error) {
	if shouldRewrite != nil && !shouldRewrite(abs) {
		return abs, nil
	}
	if err := security.MediaHostOK(abs, allowedPrivate); err != nil {
		if err2 := security.HostAllowed(abs, allowedPrivate); err2 != nil {
			return "", err
		}
	}
	if sign == nil {
		return abs, nil
	}
	signature := sign(abs)
	if signature == "" {
		return abs, nil
	}
	return proxyPrefix + EncodeUpstream(abs) + "?sig=" + url.QueryEscape(signature), nil
}

func EncodeUpstream(absolute string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(absolute)) + mediaExtension(absolute)
}

func DecodeUpstream(encoded string) (string, error) {
	if dot := strings.LastIndexByte(encoded, '.'); dot >= 0 {
		encoded = encoded[:dot]
	}
	absolute, err := base64.RawURLEncoding.DecodeString(encoded)
	return string(absolute), err
}

func mediaExtension(absolute string) string {
	path := absolute
	if cut := strings.IndexAny(path, "?#"); cut >= 0 {
		path = path[:cut]
	}
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 || strings.IndexByte(path[dot:], '/') >= 0 {
		return ""
	}
	switch ext := strings.ToLower(path[dot:]); ext {
	case ".m3u8", ".ts", ".m4s", ".mp4", ".m4a", ".m4v", ".aac", ".vtt", ".webvtt", ".key", ".cmfv", ".cmfa":
		return ext
	default:
		return ""
	}
}
