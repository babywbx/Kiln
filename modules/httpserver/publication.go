package httpserver

import (
	"net/url"
	"path"
	"strings"
)

// rewriteLocalPlaylist points every reference in a published playlist at this
// server. It must handle both bare URI lines and the URI="..." attribute used
// by EXT-X-MAP and EXT-X-MEDIA: a master playlist that only rewrote bare lines
// would leave the audio rendition and the init segment unauthenticated.
func rewriteLocalPlaylist(body []byte, prefix, token string) []byte {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		switch {
		case trim == "":
		case strings.HasPrefix(trim, "#"):
			lines[i] = rewriteTagURI(line, prefix, token)
		default:
			lines[i] = localURL(trim, prefix, token)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteTagURI(line, prefix, token string) string {
	const key = `URI="`
	idx := strings.Index(line, key)
	if idx < 0 {
		return line
	}
	start := idx + len(key)
	end := strings.IndexByte(line[start:], '"')
	if end < 0 {
		return line
	}
	ref := line[start : start+end]
	if ref == "" {
		return line
	}
	return line[:start] + localURL(ref, prefix, token) + line[start+end:]
}

// localURL maps a published asset name onto this server's play path. A
// publication is flat by design, so anything that is not already a plain file
// name is passed through untouched: silently turning "../../etc/passwd" or an
// absolute foreign URL into a local one only invents a link that cannot work.
func localURL(ref, prefix, token string) string {
	name := strings.TrimSpace(ref)
	if name != path.Base(name) || !safeFileName(name) {
		return ref
	}
	u := prefix + name
	// A path token already authenticates the URL; adding a query token would
	// only widen where the token shows up.
	if token != "" && !strings.HasPrefix(prefix, "/p/") {
		u += "?token=" + url.QueryEscape(token)
	}
	return u
}
