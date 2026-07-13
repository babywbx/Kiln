package catalog

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

type ParsedM3UEntry struct {
	Title             string `json:"title"`
	Group             string `json:"group,omitempty"`
	LogoURL           string `json:"logo_url,omitempty"`
	TvgID             string `json:"tvg_id,omitempty"`
	TvgName           string `json:"tvg_name,omitempty"`
	URL               string `json:"url"`
	SuggestedID       string `json:"suggested_id,omitempty"`
	SuggestedUpstream string `json:"suggested_upstream,omitempty"`
	SuggestedPath     string `json:"suggested_path,omitempty"`
	SuggestedIngress  string `json:"suggested_ingress,omitempty"`
	Skip              bool   `json:"skip,omitempty"`
	Note              string `json:"note,omitempty"`
}

var (
	extinfRe = regexp.MustCompile(`(?i)^#EXTINF:(-?\d+)\s*(.*),(.*)$`)
	attrRe   = regexp.MustCompile(`([\w-]+)="([^"]*)"`)
)

func ParseM3U(raw string) []ParsedM3UEntry {
	lines := strings.Split(raw, "\n")
	var out []ParsedM3UEntry
	var pending *ParsedM3UEntry
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			e := ParsedM3UEntry{}
			if m := extinfRe.FindStringSubmatch(line); len(m) == 4 {
				attrs := m[2]
				e.Title = strings.TrimSpace(m[3])
				for _, am := range attrRe.FindAllStringSubmatch(attrs, -1) {
					k := strings.ToLower(am[1])
					v := am[2]
					switch k {
					case "group-title":
						e.Group = v
					case "tvg-logo":
						e.LogoURL = v
					case "tvg-id":
						e.TvgID = v
					case "tvg-name":
						e.TvgName = v
					}
				}
			} else if i := strings.LastIndex(line, ","); i >= 0 {
				e.Title = strings.TrimSpace(line[i+1:])
			}
			pending = &e
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if pending == nil {
			pending = &ParsedM3UEntry{Title: "channel"}
		}
		pending.URL = line
		out = append(out, *pending)
		pending = nil
	}
	return out
}

type ImportOptions struct {
	DefaultUpstream string
	DefaultIngress  string
	DefaultKeysFile string
	PreferHeight    int
}

func SuggestImport(entries []ParsedM3UEntry, opt ImportOptions) []ParsedM3UEntry {
	if opt.DefaultUpstream == "" {
		opt.DefaultUpstream = "origin"
	}
	if opt.DefaultIngress == "" {
		opt.DefaultIngress = "hls"
	}
	out := make([]ParsedM3UEntry, 0, len(entries))
	used := map[string]int{}
	for _, e := range entries {
		e.SuggestedUpstream = opt.DefaultUpstream
		e.SuggestedIngress = opt.DefaultIngress
		path, ingress, note := mapStreamURL(e.URL)
		if path != "" {
			e.SuggestedPath = path
		}
		if ingress != "" {
			e.SuggestedIngress = ingress
		}
		if note != "" {
			e.Note = note
		}
		if e.SuggestedPath == "" {
			if u, err := url.Parse(e.URL); err == nil && u.Path != "" && u.Path != "/" {
				e.SuggestedPath = u.Path
				e.Note = joinNote(e.Note, "used URL path; verify upstream")
			} else {
				e.Skip = true
				e.Note = joinNote(e.Note, "could not map path")
			}
		}
		if e.SuggestedIngress == "dash" && opt.DefaultKeysFile != "" {
			e.Note = joinNote(e.Note, "dash needs keys_file")
		}
		id := slugID(e.Title, e.TvgID, e.SuggestedPath)
		if n, ok := used[id]; ok {
			used[id] = n + 1
			id = fmt.Sprintf("%s-%d", id, n+1)
		} else {
			used[id] = 1
		}
		e.SuggestedID = id
		out = append(out, e)
	}
	return out
}

func joinNote(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func mapStreamURL(raw string) (path, ingress, note string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", ""
	}
	p := u.Path
	p = strings.TrimSuffix(p, "/master.m3u8")
	p = strings.TrimSuffix(p, "/index.m3u8")
	p = strings.TrimSuffix(p, ".m3u8")

	if strings.HasPrefix(p, "/stream/") {
		rest := strings.TrimPrefix(p, "/stream/")
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 {
			provider := parts[0]
			name := parts[1]
			path = "/" + provider + "/" + name
			switch strings.ToLower(provider) {
			case "live", "tv":
				ingress = "dash"
				note = "mapped /stream path"
			case "vod", "vd":
				ingress = "hls"
				note = "mapped /stream path"
			default:
				ingress = "hls"
				note = "mapped /stream path"
			}
			return path, ingress, note
		}
	}
	if strings.HasPrefix(p, "/live/") || strings.HasPrefix(p, "/tv/") {
		return p, "dash", "dash path"
	}
	if strings.HasPrefix(p, "/vod/") {
		return p, "hls", "hls path"
	}
	if p != "" && p != "/" {
		return p, "", ""
	}
	return "", "", ""
}

func slugID(title, tvgID, pathHint string) string {
	base := tvgID
	if base == "" {
		base = title
	}
	if base == "" {
		base = pathHint
	}
	base = strings.ToLower(strings.TrimSpace(base))
	var b strings.Builder
	lastDash := false
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "ch"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}
