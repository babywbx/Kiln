//go:build ignore

// make-cbcs-fixture turns clear fragmented MP4s into a cbcs-encrypted DASH
// fixture. ffmpeg only writes cenc-aes-ctr, so cbcs coverage has to be produced
// here. The encryption is done by the library's own encryptor, which keeps the
// fixture independent of the decryption path under test.
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
)

// encrypt writes one track's init and media segments, encrypted in place.
func encrypt(path, outDir string, streamID int, key, kid []byte, clearDir string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	f, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if f.Init == nil {
		return 0, fmt.Errorf("%s: not a fragmented mp4", path)
	}

	var frags []*mp4.Fragment
	for _, seg := range f.Segments {
		frags = append(frags, seg.Fragments...)
	}
	if len(frags) == 0 {
		return 0, fmt.Errorf("%s: no fragments", path)
	}

	// The plaintext goes next to the fixture: it is what the decrypted output is
	// held against, and without it a passing test would only prove that the two
	// halves of one library agree with each other.
	if clearDir != "" {
		if err := write(filepath.Join(clearDir, fmt.Sprintf("init-stream%d.m4s", streamID)), f.Init); err != nil {
			return 0, err
		}
		for i, frag := range frags {
			name := fmt.Sprintf("chunk-stream%d-%05d.m4s", streamID, i+1)
			if err := write(filepath.Join(clearDir, name), frag); err != nil {
				return 0, err
			}
		}
	}

	// cbcs uses one constant IV for every sample, carried in the tenc box.
	iv, err := hex.DecodeString("11223344556677889900aabbccddeeff")
	if err != nil {
		return 0, err
	}
	ipd, err := mp4.InitProtect(f.Init, key, iv, "cbcs", mp4.UUID(kid), nil)
	if err != nil {
		return 0, fmt.Errorf("%s: init protect: %w", path, err)
	}
	if err := write(filepath.Join(outDir, fmt.Sprintf("init-stream%d.m4s", streamID)), f.Init); err != nil {
		return 0, err
	}
	for i, frag := range frags {
		if _, err := mp4.EncryptFragment(frag, key, iv, ipd); err != nil {
			return 0, fmt.Errorf("%s: encrypt fragment %d: %w", path, i+1, err)
		}
		name := fmt.Sprintf("chunk-stream%d-%05d.m4s", streamID, i+1)
		if err := write(filepath.Join(outDir, name), frag); err != nil {
			return 0, err
		}
	}
	return len(frags), nil
}

type encoder interface{ Encode(w io.Writer) error }

func write(path string, e encoder) error {
	var buf bytes.Buffer
	if err := e.Encode(&buf); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
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
	var video, audio, outDir, clearDir, vcodec, acodec, keyHex, kidHex string
	var width, height int
	var duration float64
	flag.StringVar(&video, "video", "", "clear fragmented mp4, video only")
	flag.StringVar(&audio, "audio", "", "clear fragmented mp4, audio only")
	flag.StringVar(&outDir, "out", "", "output directory")
	flag.StringVar(&clearDir, "clear-out", "", "where to also write the plaintext segments")
	flag.StringVar(&vcodec, "vcodec", "avc1.42c00c", "MPD codecs attribute for video")
	flag.StringVar(&acodec, "acodec", "mp4a.40.2", "MPD codecs attribute for audio")
	flag.StringVar(&keyHex, "key", "", "16-byte content key, hex")
	flag.StringVar(&kidHex, "kid", "", "16-byte key id, hex")
	flag.IntVar(&width, "width", 320, "video width")
	flag.IntVar(&height, "height", 180, "video height")
	flag.Float64Var(&duration, "duration", 4.0, "media presentation duration in seconds")
	flag.Parse()

	if video == "" || audio == "" || outDir == "" || keyHex == "" || kidHex == "" {
		fmt.Fprintln(os.Stderr, "usage: make-cbcs-fixture -video v.mp4 -audio a.mp4 -out dir -key hex -kid hex")
		os.Exit(2)
	}
	key, err := hex.DecodeString(keyHex)
	check(err)
	kid, err := hex.DecodeString(kidHex)
	check(err)
	if len(key) != 16 || len(kid) != 16 {
		fmt.Fprintln(os.Stderr, "key and kid must both be 16 bytes")
		os.Exit(2)
	}
	check(os.MkdirAll(outDir, 0o755))
	if clearDir != "" {
		check(os.MkdirAll(clearDir, 0o755))
	}

	vn, err := encrypt(video, outDir, 0, key, kid, clearDir)
	check(err)
	an, err := encrypt(audio, outDir, 1, key, kid, clearDir)
	check(err)

	mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011"
	profiles="urn:mpeg:dash:profile:isoff-live:2011"
	type="static"
	mediaPresentationDuration="PT%.1fS"
	maxSegmentDuration="PT2.0S"
	minBufferTime="PT4.0S">
	<Period id="0" start="PT0.0S">
		<AdaptationSet id="0" contentType="video" segmentAlignment="true" startWithSAP="1">
			<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cbcs"/>
			<Representation id="0" mimeType="video/mp4" codecs="%s" bandwidth="120000" width="%d" height="%d">
%s			</Representation>
		</AdaptationSet>
		<AdaptationSet id="1" contentType="audio" segmentAlignment="true" startWithSAP="1">
			<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cbcs"/>
			<Representation id="1" mimeType="audio/mp4" codecs="%s" bandwidth="32000" audioSamplingRate="44100">
				<AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
%s			</Representation>
		</AdaptationSet>
	</Period>
</MPD>
`, duration, vcodec, width, height, segmentList(0, vn, duration), acodec, segmentList(1, an, duration))

	check(os.WriteFile(filepath.Join(outDir, "stream.mpd"), []byte(mpd), 0o644))
	fmt.Printf("%s: video=%d chunks audio=%d chunks (cbcs)\n", outDir, vn, an)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
