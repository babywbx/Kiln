package packager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/packager/hls"
)

func TestNativeServesACbcsSource(t *testing.T) {
	job, out := runNative(t, "cbcs")

	if job.Engine() != EngineNativeRewrite {
		t.Fatalf("engine = %s, want the native path to take a cbcs source", job.Engine())
	}
	if !job.Publication().Playable() {
		t.Fatal("publication is not playable")
	}

	waitForStaticCompletion(t, job)
	if err := job.Err(); err != nil {
		t.Fatalf("job error: %v", err)
	}

	for _, name := range []string{hls.MasterName, "video-main.m3u8", "audio-main.m3u8"} {
		pl, ok := job.Publication().Playlist(name)
		if !ok {
			t.Fatalf("no playlist %s", name)
		}
		if name == hls.MasterName {
			continue
		}
		if !strings.Contains(string(pl), "#EXT-X-ENDLIST") {
			t.Errorf("%s never ended:\n%s", name, pl)
		}
		for line := range strings.SplitSeq(string(pl), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if _, err := os.Stat(filepath.Join(out, line)); err != nil {
				t.Errorf("playlist references %s but it is not on disk: %v", line, err)
			}
		}
	}
}

func TestCbcsOutputCarriesNoProtection(t *testing.T) {
	_, out := runNative(t, "cbcs")

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	seen := 0
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, box := range []string{"senc", "saiz", "saio", "tenc", "encv", "enca", "sinf"} {
			if strings.Contains(string(raw), box) {
				t.Errorf("%s still carries a %s box", e.Name(), box)
			}
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no assets were published")
	}
}
