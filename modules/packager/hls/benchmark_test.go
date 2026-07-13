package hls

import (
	"fmt"
	"testing"
	"time"
)

func benchmarkPublisher(b *testing.B, static bool, tracks, segments int) {
	p, err := New(Config{Dir: b.TempDir(), Static: static, PlaylistSize: segments + 1, Grace: time.Second})
	if err != nil {
		b.Fatal(err)
	}
	for track := range tracks {
		name := fmt.Sprintf("track-%d", track)
		kind := KindAudio
		codec := "mp4a.40.2"
		if track == 0 {
			kind = KindVideo
			codec = "avc1.64001f"
		}
		if err := p.AddTrack(Track{Name: name, Kind: kind, Codec: codec}); err != nil {
			b.Fatal(err)
		}
		if err := p.PublishInit(name, []byte("init")); err != nil {
			b.Fatal(err)
		}
	}
	media := make([]byte, 4096)
	var sequence uint64
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for range segments {
			for track := range tracks {
				if err := p.PublishSegment(Publication{Track: fmt.Sprintf("track-%d", track), Seq: sequence, Duration: 2}, media); err != nil {
					b.Fatal(err)
				}
			}
			sequence++
		}
	}
	b.StopTimer()
	for track, frontier := range p.Frontier() {
		if frontier+1 != sequence {
			b.Fatalf("%s frontier = %d, want %d", track, frontier, sequence-1)
		}
	}
}

func BenchmarkDynamicMultiTrack(b *testing.B) { benchmarkPublisher(b, false, 8, 10) }
func BenchmarkLongStatic(b *testing.B)        { benchmarkPublisher(b, true, 2, 1000) }
