package observe

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func (s *Service) WritePrometheus(w io.Writer) error {
	snapshot := s.Snapshot()
	if _, err := fmt.Fprintf(w, "# TYPE kiln_uptime_seconds gauge\nkiln_uptime_seconds %d\n", snapshot.UptimeSec); err != nil {
		return err
	}
	metrics := []struct {
		name  string
		kind  string
		value uint64
	}{
		{"kiln_bytes_in_total", "counter", snapshot.BytesIn},
		{"kiln_bytes_out_total", "counter", snapshot.BytesOut},
		{"kiln_http_requests_total", "counter", snapshot.Requests},
		{"kiln_errors_total", "counter", snapshot.Errors},
		{"kiln_goroutines", "gauge", uint64(snapshot.Goroutines)},
		{"kiln_sessions", "gauge", uint64(snapshot.SessionCount)},
	}
	for _, metric := range metrics {
		if _, err := fmt.Fprintf(w, "# TYPE %s %s\n%s %d\n", metric.name, metric.kind, metric.name, metric.value); err != nil {
			return err
		}
	}
	sort.Slice(snapshot.Sessions, func(i, j int) bool { return snapshot.Sessions[i].ChannelID < snapshot.Sessions[j].ChannelID })
	for _, session := range snapshot.Sessions {
		if _, err := fmt.Fprintf(w, "kiln_session_info{channel=%s,engine=%s,state=%s} 1\n",
			promLabel(session.ChannelID), promLabel(session.Engine), promLabel(session.State)); err != nil {
			return err
		}
		if session.Packager == nil {
			continue
		}
		if err := writePackagerMetrics(w, session.ChannelID, *session.Packager); err != nil {
			return err
		}
	}
	return nil
}

func writePackagerMetrics(w io.Writer, channel string, stat PackagerStat) error {
	label := "{channel=" + promLabel(channel) + "}"
	counters := []struct {
		name  string
		value uint64
	}{
		{"segments_published", stat.SegmentsPublished},
		{"parts_published", stat.PartsPublished},
		{"segments_fetched", stat.SegmentsFetched},
		{"segment_fetch_errors", stat.SegmentFetchErrs},
		{"manifest_refreshes", stat.ManifestRefreshes},
		{"manifest_errors", stat.ManifestErrs},
		{"discontinuities", stat.Discontinuities},
		{"reanchors", stat.Reanchors},
		{"reresolves", stat.Reresolves},
		{"track_holds", stat.TrackHolds},
		{"key_mismatches", stat.KeyMismatches},
	}
	for _, metric := range counters {
		if _, err := fmt.Fprintf(w, "kiln_packager_%s_total%s %d\n", metric.name, label, metric.value); err != nil {
			return err
		}
	}
	gauges := []struct {
		name  string
		value string
	}{
		{"decrypt_seconds", strconv.FormatFloat(stat.DecryptSeconds, 'f', -1, 64)},
		{"cache_bytes", strconv.FormatInt(stat.CacheBytes, 10)},
		{"cache_items", strconv.Itoa(stat.CacheItems)},
		{"video_frontier", strconv.FormatUint(stat.VideoFrontier, 10)},
		{"audio_frontier", strconv.FormatUint(stat.AudioFrontier, 10)},
		{"video_tracks", strconv.Itoa(stat.VideoTracks)},
		{"audio_tracks", strconv.Itoa(stat.AudioTracks)},
		{"text_tracks", strconv.Itoa(stat.TextTracks)},
		{"clock_offset_seconds", strconv.FormatFloat(stat.ClockOffsetSeconds, 'f', -1, 64)},
	}
	for _, metric := range gauges {
		if _, err := fmt.Fprintf(w, "kiln_packager_%s%s %s\n", metric.name, label, metric.value); err != nil {
			return err
		}
	}
	return nil
}

func promLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
