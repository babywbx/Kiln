//go:build ignore

package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type box struct {
	typ        string
	start, end int64
}

func walk(data []byte) ([]box, error) {
	var boxes []box
	var off int64
	total := int64(len(data))
	for off+8 <= total {
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		hdr := int64(8)
		switch size {
		case 1:
			if off+16 > total {
				return nil, fmt.Errorf("truncated largesize box at %d", off)
			}
			size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		case 0:
			size = total - off
		}
		if size < hdr || off+size > total {
			return nil, fmt.Errorf("bad box %q size %d at %d", typ, size, off)
		}
		boxes = append(boxes, box{typ: typ, start: off, end: off + size})
		off += size
	}
	if off != total {
		return nil, fmt.Errorf("trailing %d bytes", total-off)
	}
	return boxes, nil
}

func split(path, outDir string, streamID int) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	boxes, err := walk(data)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}

	starts := []int{}
	for i, b := range boxes {
		if b.typ != "moof" {
			continue
		}
		s := i
		for s > 0 && (boxes[s-1].typ == "styp" || boxes[s-1].typ == "sidx") {
			s--
		}
		starts = append(starts, s)
	}
	if len(starts) == 0 {
		return 0, fmt.Errorf("%s: no moof found, not a fragmented mp4", path)
	}

	initEnd := boxes[starts[0]].start
	initPath := filepath.Join(outDir, fmt.Sprintf("init-stream%d.m4s", streamID))
	if err := os.WriteFile(initPath, data[:initEnd], 0o644); err != nil {
		return 0, err
	}

	for n, s := range starts {
		end := int64(len(data))
		if n+1 < len(starts) {
			end = boxes[starts[n+1]].start
		}
		p := filepath.Join(outDir, fmt.Sprintf("chunk-stream%d-%05d.m4s", streamID, n+1))
		if err := os.WriteFile(p, data[boxes[s].start:end], 0o644); err != nil {
			return 0, err
		}
	}
	return len(starts), nil
}

func segmentList(streamID, count int, total float64) string {
	var b strings.Builder
	per := int64(total * 1e6 / float64(count))
	fmt.Fprintf(&b, "\t\t\t\t<SegmentList timescale=\"1000000\" duration=\"%d\">\n", per)
	fmt.Fprintf(&b, "\t\t\t\t\t<Initialization sourceURL=\"init-stream%d.m4s\"/>\n", streamID)
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, "\t\t\t\t\t<SegmentURL media=\"chunk-stream%d-%05d.m4s\"/>\n", streamID, i)
	}
	fmt.Fprintf(&b, "\t\t\t\t</SegmentList>\n")
	return b.String()
}

func main() {
	var video, audio, outDir, vcodec, acodec string
	var width, height int
	var duration float64
	flag.StringVar(&video, "video", "", "encrypted fragmented mp4, video only")
	flag.StringVar(&audio, "audio", "", "encrypted fragmented mp4, audio only")
	flag.StringVar(&outDir, "out", "", "output directory")
	flag.StringVar(&vcodec, "vcodec", "avc1.42c00c", "MPD codecs attribute for video")
	flag.StringVar(&acodec, "acodec", "mp4a.40.2", "MPD codecs attribute for audio")
	flag.IntVar(&width, "width", 320, "video width")
	flag.IntVar(&height, "height", 180, "video height")
	flag.Float64Var(&duration, "duration", 4.0, "media presentation duration in seconds")
	flag.Parse()

	if video == "" || audio == "" || outDir == "" {
		fmt.Fprintln(os.Stderr, "usage: make-cenc-fixture -video v.mp4 -audio a.mp4 -out dir")
		os.Exit(2)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	vn, err := split(video, outDir, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	an, err := split(audio, outDir, 1)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"
	profiles="urn:mpeg:dash:profile:isoff-live:2011"
	type="static"
	mediaPresentationDuration="PT%.1fS"
	maxSegmentDuration="PT2.0S"
	minBufferTime="PT4.0S">
	<Period id="0" start="PT0.0S">
		<AdaptationSet id="0" contentType="video" segmentAlignment="true" startWithSAP="1">
			<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
			<Representation id="0" mimeType="video/mp4" codecs="%s" bandwidth="120000" width="%d" height="%d">
%s			</Representation>
		</AdaptationSet>
		<AdaptationSet id="1" contentType="audio" segmentAlignment="true" startWithSAP="1">
			<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
			<Representation id="1" mimeType="audio/mp4" codecs="%s" bandwidth="32000" audioSamplingRate="44100">
				<AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
%s			</Representation>
		</AdaptationSet>
	</Period>
</MPD>
`, duration, vcodec, width, height, segmentList(0, vn, duration), acodec, segmentList(1, an, duration))

	if err := os.WriteFile(filepath.Join(outDir, "stream.mpd"), []byte(mpd), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s: video=%d chunks audio=%d chunks\n", outDir, vn, an)
}
