package egress

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWatchPlaylistProgressAcceptsAdvancingPlaylist(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.m3u8")
	writeMediaPlaylist(t, index, 10, "seg_10.ts")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- watchPlaylistProgress(ctx, index, 10*time.Millisecond, 300*time.Millisecond)
	}()

	for sequence := 11; sequence <= 14; sequence++ {
		time.Sleep(100 * time.Millisecond)
		writeMediaPlaylist(t, index, sequence, "seg_"+strconv.Itoa(sequence)+".ts")
	}

	select {
	case err := <-done:
		t.Fatalf("watchdog stopped while playlist was advancing: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("canceled watchdog returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog leaked after cancellation")
	}
}

func TestWatchPlaylistProgressReportsStall(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.m3u8")
	writeMediaPlaylist(t, index, 42, "seg_42.ts")

	err := watchPlaylistProgress(context.Background(), index, 5*time.Millisecond, 35*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "playlist stalled") {
		t.Fatalf("stall error = %v", err)
	}
}

func TestStartPlaylistWatchdogCancelsStalledJob(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.m3u8")
	writeMediaPlaylist(t, index, 7, "seg_7.ts")

	ctx, cancel := context.WithCancel(context.Background())
	job := &DashJob{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	watchdogDone := startPlaylistWatchdog(ctx, job, index, 5*time.Millisecond, 35*time.Millisecond)

	select {
	case <-watchdogDone:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not terminate")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("job context error = %v, want canceled", ctx.Err())
	}
	if err := job.Err(); err == nil || !strings.Contains(err.Error(), "playlist stalled") {
		t.Fatalf("job error = %v", err)
	}
}

func TestStartPlaylistWatchdogStopsWithoutFailure(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.m3u8")
	writeMediaPlaylist(t, index, 7, "seg_7.ts")

	ctx, cancel := context.WithCancel(context.Background())
	job := &DashJob{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	watchdogDone := startPlaylistWatchdog(ctx, job, index, 5*time.Millisecond, time.Second)
	go func() {
		<-ctx.Done()
		close(job.done)
	}()
	if err := job.Stop(); err != nil {
		t.Fatalf("stop job: %v", err)
	}

	select {
	case <-watchdogDone:
	case <-time.After(time.Second):
		t.Fatal("watchdog leaked after cancellation")
	}
	if err := job.Err(); err != nil {
		t.Fatalf("intentional stop recorded watchdog error: %v", err)
	}
}

func TestPlaylistStallTimeoutUsesSegmentWindowAndBounds(t *testing.T) {
	tests := []struct {
		name     string
		hlsTime  int
		listSize int
		want     time.Duration
	}{
		{name: "default", hlsTime: 2, listSize: 6, want: 20 * time.Second},
		{name: "low latency", hlsTime: 2, listSize: 4, want: 16 * time.Second},
		{name: "minimum", hlsTime: 1, listSize: 1, want: 15 * time.Second},
		{name: "maximum", hlsTime: 30, listSize: 20, want: 2 * time.Minute},
		{name: "maximum integer", hlsTime: int(^uint(0) >> 1), listSize: int(^uint(0) >> 1), want: 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := playlistStallTimeout(tt.hlsTime, tt.listSize); got != tt.want {
				t.Fatalf("playlistStallTimeout(%d, %d) = %v, want %v", tt.hlsTime, tt.listSize, got, tt.want)
			}
		})
	}
}

func writeMediaPlaylist(t *testing.T, path string, sequence int, segment string) {
	t.Helper()
	body := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(sequence) + "\n#EXTINF:2.000,\n" + segment + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
}
