package catalog

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

type ImportAction string

const (
	ImportCreate ImportAction = "create"
	ImportUpdate ImportAction = "update"
	ImportSkip   ImportAction = "skip"
)

type ParsedM3UEntry struct {
	Title            string       `json:"title"`
	Group            string       `json:"group,omitempty"`
	LogoURL          string       `json:"logo_url,omitempty"`
	TvgID            string       `json:"tvg_id,omitempty"`
	TvgName          string       `json:"tvg_name,omitempty"`
	URL              string       `json:"url"`
	SuggestedID      string       `json:"suggested_id,omitempty"`
	SuggestedIngress string       `json:"suggested_ingress,omitempty"`
	Action           ImportAction `json:"action"`
	Skip             bool         `json:"skip,omitempty"`
	Note             string       `json:"note,omitempty"`
}

type ImportResult struct {
	Preview bool             `json:"preview,omitempty"`
	Applied bool             `json:"applied,omitempty"`
	Count   int              `json:"count"`
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Skipped int              `json:"skipped"`
	Entries []ParsedM3UEntry `json:"entries,omitempty"`
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
					key := strings.ToLower(am[1])
					value := am[2]
					switch key {
					case "group-title":
						e.Group = value
					case "tvg-logo":
						e.LogoURL = value
					case "tvg-id":
						e.TvgID = value
					case "tvg-name":
						e.TvgName = value
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

func (s *Service) PreviewM3U(raw string) (ImportResult, error) {
	result, _, err := s.planM3U(raw)
	if err != nil {
		return ImportResult{}, err
	}
	result.Preview = true
	return result, nil
}

func (s *Service) ApplyM3U(raw string, revisions map[string]int64) (ImportResult, error) {
	result, pending, err := s.planM3U(raw)
	if err != nil {
		return ImportResult{}, err
	}
	if len(pending) > 0 {
		if err := s.UpsertBatchIfRevisions(pending, revisions); err != nil {
			return ImportResult{}, err
		}
	}
	result.Applied = true
	return result, nil
}

func (s *Service) planM3U(raw string) (ImportResult, []config.Channel, error) {
	if strings.TrimSpace(raw) == "" {
		return ImportResult{}, nil, fmt.Errorf("m3u content required")
	}
	existingChannels, err := s.List(true)
	if err != nil {
		return ImportResult{}, nil, err
	}
	existing := make(map[string]config.Channel, len(existingChannels))
	for _, channel := range existingChannels {
		existing[channel.ID] = channel
	}

	parsed := ParseM3U(raw)
	result := ImportResult{Count: len(parsed), Entries: make([]ParsedM3UEntry, 0, len(parsed))}
	pending := make([]config.Channel, 0, len(parsed))
	used := make(map[string]int, len(parsed))
	for _, entry := range parsed {
		entry.URL = strings.TrimSpace(entry.URL)
		parsedURL, _ := url.Parse(entry.URL)
		pathHint := ""
		if parsedURL != nil {
			pathHint = parsedURL.Path
		}
		entry.SuggestedID = uniqueImportID(slugID(entry.Title, entry.TvgID, pathHint), used)
		entry.SuggestedIngress = inferImportIngress(parsedURL)

		if err := config.ValidateSourceURL(entry.URL); err != nil {
			entry.Skip = true
			entry.Action = ImportSkip
			entry.Note = joinNote(entry.Note, "invalid source URL: "+err.Error())
			result.Skipped++
			result.Entries = append(result.Entries, entry)
			continue
		}

		channel, exists := existing[entry.SuggestedID]
		if !exists {
			channel = config.Channel{ID: entry.SuggestedID, OnDemand: true, IdleTimeoutSec: 90}
			entry.Action = ImportCreate
		} else {
			entry.Action = ImportUpdate
		}
		if entry.Title != "" {
			channel.Title = entry.Title
		}
		if entry.Group != "" {
			channel.Group = entry.Group
		}
		if entry.LogoURL != "" {
			channel.LogoURL = entry.LogoURL
		}
		if entry.TvgID != "" {
			channel.EPGID = entry.TvgID
		}
		if entry.TvgName != "" {
			channel.EPGName = entry.TvgName
		}
		channel.SourceURL = entry.URL
		channel.Upstream = ""
		channel.Path = ""
		channel.Ingress = entry.SuggestedIngress
		channel = normalizeChannel(channel)
		if err := store.ValidateChannel(channel, s.cfg.Upstreams); err != nil {
			entry.Skip = true
			entry.Action = ImportSkip
			entry.Note = joinNote(entry.Note, err.Error())
			result.Skipped++
			result.Entries = append(result.Entries, entry)
			continue
		}

		if entry.Action == ImportCreate {
			result.Created++
		} else {
			result.Updated++
		}
		pending = append(pending, channel)
		result.Entries = append(result.Entries, entry)
	}
	return result, pending, nil
}

func inferImportIngress(parsedURL *url.URL) string {
	if parsedURL != nil && strings.EqualFold(path.Ext(parsedURL.Path), ".mpd") {
		return "dash"
	}
	return "hls"
}

func uniqueImportID(base string, used map[string]int) string {
	count := used[base] + 1
	used[base] = count
	if count == 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, count)
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

func slugID(title, tvgID, pathHint string) string {
	base := tvgID
	if base == "" {
		base = title
	}
	if base == "" {
		base = pathHint
	}
	base = strings.ToLower(strings.TrimSpace(base))
	var builder strings.Builder
	lastDash := false
	for _, char := range base {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		slug = "ch"
	}
	runes := []rune(slug)
	if len(runes) > 48 {
		slug = string(runes[:48])
	}
	return slug
}
