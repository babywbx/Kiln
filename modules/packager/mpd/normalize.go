package mpd

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	cencScheme      = "urn:mpeg:dash:mp4protection:2011"
	trickModeScheme = "http://dashif.org/guidelines/trickmode"
)

func normalizePeriod(xp xmlPeriod, mpdBase *url.URL, tolerant bool) (Period, error) {
	period := Period{ID: xp.ID}
	var err error
	if period.Start, err = optDuration(xp.Start); err != nil {
		return Period{}, err
	}
	if period.Duration, err = optDuration(xp.Duration); err != nil {
		return Period{}, err
	}
	periodBase := resolveAll(mpdBase, xp.BaseURL)

	for i, as := range xp.AdaptationSets {
		asBase := resolveAll(periodBase, as.BaseURL)
		tmpl := mergeTemplate(xp.SegmentTemplate, as.SegmentTemplate)
		group := as.ID
		if group == "" {
			group = stableAdaptationGroup(as)
			if group == "" {
				group = strconv.Itoa(i)
			}
		}
		for _, xr := range as.Representations {
			rep, err := normalizeRepresentation(xr, as, tmpl, asBase, tolerant)
			if err != nil {
				return Period{}, err
			}
			rep.Group = group
			rep.PeriodID = xp.ID
			rep.AdaptationSetID = as.ID
			rep.TrackKey = stableTrackKey(rep)
			period.Representations = append(period.Representations, rep)
		}
	}
	return period, nil
}

func stableAdaptationGroup(as xmlAdaptationSet) string {
	roles := make([]string, 0, len(as.Roles))
	for _, role := range as.Roles {
		roles = append(roles, strings.TrimSpace(role.Value))
	}
	sort.Strings(roles)
	representations := make([]string, 0, len(as.Representations))
	for _, rep := range as.Representations {
		representations = append(representations, strings.Join([]string{
			rep.ID, rep.Codecs, strconv.Itoa(rep.Width), strconv.Itoa(rep.Height),
			rep.FrameRate, rep.AudioSamplingRate,
		}, ":"))
	}
	sort.Strings(representations)
	identity := strings.Join([]string{
		as.ContentType, as.MimeType, as.Codecs, as.Lang, strings.Join(roles, ","),
		strings.Join(representations, ","),
	}, "\x1f")
	if strings.Trim(identity, "\x1f,") == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	return "anon_" + base64.RawURLEncoding.EncodeToString(sum[:8])
}

func normalizeRepresentation(xr xmlRepresation, as xmlAdaptationSet, parentTmpl *xmlSegmentTmpl, asBase *url.URL, tolerant bool) (Representation, error) {
	base := resolveAll(asBase, xr.BaseURL)

	rep := Representation{
		ID:                xr.ID,
		MimeType:          firstNonEmpty(xr.MimeType, as.MimeType),
		Codecs:            firstNonEmpty(xr.Codecs, as.Codecs),
		Bandwidth:         xr.Bandwidth,
		Width:             firstNonZero(xr.Width, as.Width),
		Height:            firstNonZero(xr.Height, as.Height),
		FrameRate:         firstNonEmpty(xr.FrameRate, as.FrameRate),
		AudioSamplingRate: firstNonEmpty(xr.AudioSamplingRate, as.AudioSamplingRate),
		Lang:              as.Lang,
		Trick:             firstNonEmpty(xr.MaxPlayoutRate, as.MaxPlayoutRate) != "",
	}
	for _, ep := range append(append([]xmlDescriptor{}, as.Essential...), xr.Essential...) {
		scheme := strings.ToLower(strings.TrimSpace(ep.SchemeIDURI))
		if scheme == "" {
			continue
		}
		if scheme == trickModeScheme {
			rep.Trick = true
			continue
		}
		rep.Essential = append(rep.Essential, scheme)
	}
	for _, role := range append(append([]xmlDescriptor{}, as.Roles...), xr.Roles...) {
		if role.Value != "" {
			rep.Roles = append(rep.Roles, role.Value)
		}
	}
	for _, acc := range append(append([]xmlDescriptor{}, as.AudioChannelConf...), xr.AudioChannelConf...) {
		if n, err := strconv.Atoi(strings.TrimSpace(acc.Value)); err == nil && n > 0 {
			rep.AudioChannels = n
		}
	}
	rep.Type = deriveContentType(as.ContentType, rep.MimeType, rep.Codecs, rep.Height, rep.AudioSamplingRate)
	for _, cp := range append(append([]xmlProtection{}, as.ContentProtection...), xr.ContentProtection...) {
		if !strings.EqualFold(cp.SchemeIDURI, cencScheme) {
			continue
		}
		rep.Encrypted = true
		if cp.Value != "" {
			rep.Scheme = strings.ToLower(cp.Value)
		}
		if cp.DefaultKID != "" {
			rep.DefaultKID = normalizeKID(cp.DefaultKID)
		}
	}
	if len(as.ContentProtection) > 0 || len(xr.ContentProtection) > 0 {
		rep.Encrypted = true
	}

	addr, err := normalizeAddressing(xr, as, parentTmpl, base, rep.ID, rep.Bandwidth)
	if err != nil {
		if tolerant {
			rep.UnsupportedReason = err.Error()
			return rep, nil
		}
		return Representation{}, fmt.Errorf("representation %s: %w", rep.ID, err)
	}
	rep.Addressing = addr
	return rep, nil
}

func stableTrackKey(rep Representation) string {
	identity := strings.Join([]string{
		"v1", rep.PeriodID, rep.AdaptationSetID, rep.Group, rep.ID,
		string(rep.Type), rep.Lang, strings.Join(rep.Roles, ","), rep.Codecs,
		strconv.Itoa(rep.Width), strconv.Itoa(rep.Height), rep.FrameRate,
		strconv.Itoa(rep.AudioChannels), rep.AudioSamplingRate,
	}, "\x1f")
	sum := sha256.Sum256([]byte(identity))
	return "trk_" + base64.RawURLEncoding.EncodeToString(sum[:12])
}

func normalizeAddressing(xr xmlRepresation, as xmlAdaptationSet, parentTmpl *xmlSegmentTmpl, base *url.URL, repID string, bandwidth int) (Addressing, error) {
	tmpl := mergeTemplate(parentTmpl, xr.SegmentTemplate)
	if tmpl != nil {
		return templateAddressing(tmpl, base, repID, bandwidth)
	}
	list := xr.SegmentList
	if list == nil {
		list = as.SegmentList
	}
	if list != nil {
		return listAddressing(list, base)
	}
	if xr.SegmentBase != nil {
		return Addressing{}, fmt.Errorf("SegmentBase addressing is not supported")
	}
	return Addressing{}, fmt.Errorf("no segment addressing")
}

func templateAddressing(t *xmlSegmentTmpl, base *url.URL, repID string, bandwidth int) (Addressing, error) {
	if t.Media == "" {
		return Addressing{}, fmt.Errorf("SegmentTemplate without @media")
	}
	addr := Addressing{
		Timescale:              t.Timescale,
		Duration:               t.Duration,
		PresentationTimeOffset: t.PresentationTimeOffset,
		StartNumber:            1,
	}
	if t.StartNumber != nil {
		addr.StartNumber = *t.StartNumber
	}
	if addr.Timescale == 0 {
		addr.Timescale = 1
	}

	if t.Initialization != "" {
		initRef := expandIdentifiers(t.Initialization, repID, bandwidth, 0, 0)
		u, err := resolveRef(base, initRef)
		if err != nil {
			return Addressing{}, err
		}
		addr.InitURL = u
	}
	mediaRef := expandStatic(t.Media, repID, bandwidth)
	mediaURL, err := resolveRef(base, mediaRef)
	if err != nil {
		return Addressing{}, err
	}
	addr.Media = mediaURL

	switch {
	case t.SegmentTimeline != nil:
		addr.Mode = AddressingTemplateTimeline
		entries, err := normalizeTimeline(t.SegmentTimeline)
		if err != nil {
			return Addressing{}, err
		}
		addr.Timeline = entries
	case t.Duration > 0:
		addr.Mode = AddressingTemplateDuration
	default:
		return Addressing{}, fmt.Errorf("SegmentTemplate without SegmentTimeline or @duration")
	}
	return addr, nil
}

func normalizeTimeline(t *xmlTimeline) ([]TimelineEntry, error) {
	if len(t.S) == 0 {
		return nil, fmt.Errorf("empty SegmentTimeline")
	}
	out := make([]TimelineEntry, 0, len(t.S))
	var next uint64
	for i, s := range t.S {
		if s.D == 0 {
			return nil, fmt.Errorf("SegmentTimeline S[%d] without @d", i)
		}
		e := TimelineEntry{Duration: s.D}
		if s.T != nil {
			e.Time = *s.T
		} else if i == 0 {
			e.Time = 0
		} else {
			e.Time = next
		}
		if s.R != nil {
			if *s.R < -1 {
				return nil, fmt.Errorf("SegmentTimeline S[%d] has @r=%d", i, *s.R)
			}
			e.Repeat = *s.R
		}
		if e.Repeat == -1 && i != len(t.S)-1 {
			return nil, fmt.Errorf("SegmentTimeline S[%d] has @r=-1 but is not last", i)
		}
		if e.Repeat >= 0 {
			next = e.Time + e.Duration*uint64(e.Repeat+1)
		}
		out = append(out, e)
	}
	return out, nil
}

func listAddressing(l *xmlSegmentList, base *url.URL) (Addressing, error) {
	addr := Addressing{
		Mode:                   AddressingList,
		Timescale:              l.Timescale,
		Duration:               l.Duration,
		PresentationTimeOffset: l.PresentationTimeOffset,
		StartNumber:            1,
	}
	if l.StartNumber != nil {
		addr.StartNumber = *l.StartNumber
	}
	if addr.Timescale == 0 {
		addr.Timescale = 1
	}
	if l.Initialization != nil && l.Initialization.SourceURL != "" {
		u, err := resolveRef(base, l.Initialization.SourceURL)
		if err != nil {
			return Addressing{}, err
		}
		addr.InitURL = u
	}
	for _, s := range l.SegmentURLs {
		if s.MediaRange != "" {
			return Addressing{}, fmt.Errorf("byte-range SegmentURL is not supported")
		}
		u, err := resolveRef(base, s.Media)
		if err != nil {
			return Addressing{}, err
		}
		addr.List = append(addr.List, u)
	}
	if len(addr.List) == 0 {
		return Addressing{}, fmt.Errorf("SegmentList without SegmentURL")
	}
	if addr.Duration == 0 {
		return Addressing{}, fmt.Errorf("SegmentList without @duration")
	}
	return addr, nil
}

func mergeTemplate(parent, child *xmlSegmentTmpl) *xmlSegmentTmpl {
	if parent == nil {
		return child
	}
	if child == nil {
		return parent
	}
	out := *parent
	if child.Initialization != "" {
		out.Initialization = child.Initialization
	}
	if child.Media != "" {
		out.Media = child.Media
	}
	if child.Timescale != 0 {
		out.Timescale = child.Timescale
	}
	if child.Duration != 0 {
		out.Duration = child.Duration
	}
	if child.StartNumber != nil {
		out.StartNumber = child.StartNumber
	}
	if child.PresentationTimeOffset != 0 {
		out.PresentationTimeOffset = child.PresentationTimeOffset
	}
	if child.SegmentTimeline != nil {
		out.SegmentTimeline = child.SegmentTimeline
	}
	return &out
}

func deriveContentType(declared, mimeType, codecs string, height int, audioSamplingRate string) ContentType {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case "video":
		return TypeVideo
	case "audio":
		return TypeAudio
	case "text", "subtitle", "subtitles":
		return TypeText
	}
	mt := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(mt, "video/"):
		return TypeVideo
	case strings.HasPrefix(mt, "audio/"):
		return TypeAudio
	case strings.HasPrefix(mt, "text/") || strings.HasPrefix(mt, "application/ttml"):
		return TypeText
	}
	switch codecFamily(codecs) {
	case "avc1", "avc3", "hev1", "hvc1", "vp09", "av01", "dvh1", "dvhe":
		return TypeVideo
	case "mp4a", "ac-3", "ec-3", "opus", "flac":
		return TypeAudio
	case "stpp", "wvtt", "ttml":
		return TypeText
	}
	if height > 0 {
		return TypeVideo
	}
	if audioSamplingRate != "" {
		return TypeAudio
	}
	return ""
}

func normalizeKID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, "-", "")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
