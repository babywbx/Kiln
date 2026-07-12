package packager

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
)

const (
	fixtureKey = "00112233445566778899aabbccddeeff"
	fixtureKID = "ffeeddccbbaa99887766554433221100"
)

type httpFetcher struct {
	client *http.Client

	mu   sync.Mutex
	hits map[string]int
}

func (f *httpFetcher) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	f.mu.Lock()
	f.hits[url]++
	f.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return b, resp.Request.URL.String(), nil
}

func startOrigin(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "cenc", dir)
	srv := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(srv.Close)
	return srv
}

func keys(t *testing.T) cmaf.KeySet {
	t.Helper()
	ks, err := cmaf.NewKeySet(map[string]string{fixtureKID: fixtureKey})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	return ks
}

func runNative(t *testing.T, dir string) (*Native, string) {
	t.Helper()
	origin := startOrigin(t, dir)
	out := t.TempDir()

	job, err := StartNative(context.Background(), Options{
		ManifestURL:   origin.URL + "/stream.mpd",
		Dir:           out,
		Keys:          keys(t),
		Fetcher:       &httpFetcher{client: origin.Client(), hits: map[string]int{}},
		StartSegments: 1,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = job.Stop() })
	return job, out
}

func TestNativeProducesPlayableHLS(t *testing.T) {
	job, out := runNative(t, "hevc")

	if job.Engine() != EngineNativeRewrite {
		t.Fatalf("engine = %s, want %s", job.Engine(), EngineNativeRewrite)
	}
	if !job.Publication().Playable() {
		t.Fatal("publication is not playable after StartNative returned")
	}

	master, ok := job.Publication().Playlist(hls.MasterName)
	if !ok {
		t.Fatal("no master playlist")
	}
	for _, want := range []string{
		"#EXT-X-VERSION:7",
		`CODECS="hvc1.1.6.L60.90,mp4a.40.2"`,
		"RESOLUTION=320x180",
		`AUDIO="audio"`,
		"video-main.m3u8",
		"audio-main.m3u8",
	} {
		if !strings.Contains(string(master), want) {
			t.Errorf("master playlist is missing %q:\n%s", want, master)
		}
	}

	video, ok := job.Publication().Playlist("video-main.m3u8")
	if !ok {
		t.Fatal("no video playlist")
	}
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:2",
		`#EXT-X-MAP:URI="video-main-init.mp4"`,
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(string(video), want) {
			t.Errorf("video playlist is missing %q:\n%s", want, video)
		}
	}

	// Every asset the playlist references must already be readable.
	for line := range strings.SplitSeq(string(video), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, ok := job.Publication().Asset(line); !ok {
			t.Errorf("playlist references %s but it is not a published asset", line)
		}
		if _, err := os.Stat(filepath.Join(out, line)); err != nil {
			t.Errorf("playlist references %s but it is not on disk: %v", line, err)
		}
	}
}

// The whole point of the native path: no MPEG-TS, no external process, and no
// stray copies of the media on disk.
func TestNativeWritesOnlyFinalAssets(t *testing.T) {
	_, out := runNative(t, "hevc")
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".m4s"), strings.HasSuffix(name, ".mp4"):
		default:
			t.Errorf("unexpected file %s in the work directory", name)
		}
		if strings.HasPrefix(name, ".tmp-") {
			t.Errorf("temporary asset %s was left behind", name)
		}
	}
}

// A running publication never re-fetches or re-decrypts the same segment,
// however many players are watching.
func TestNativeFetchesEachSegmentOnce(t *testing.T) {
	origin := startOrigin(t, "hevc")
	fetcher := &httpFetcher{client: origin.Client(), hits: map[string]int{}}
	job, err := StartNative(context.Background(), Options{
		ManifestURL:   origin.URL + "/stream.mpd",
		Dir:           t.TempDir(),
		Keys:          keys(t),
		Fetcher:       fetcher,
		StartSegments: 1,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	defer func() { _ = job.Stop() }()

	select {
	case <-job.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("static publication did not finish draining")
	}
	if err := job.Err(); err != nil {
		t.Fatalf("job error: %v", err)
	}

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	for url, hits := range fetcher.hits {
		if strings.HasSuffix(url, ".m4s") && hits != 1 {
			t.Errorf("%s was fetched %d times, want 1", url, hits)
		}
	}
}

func TestNativeRejectsMissingKey(t *testing.T) {
	origin := startOrigin(t, "hevc")
	wrong, err := cmaf.NewKeySet(map[string]string{
		"0123456789abcdef0123456789abcdef": fixtureKey,
	})
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	_, err = StartNative(context.Background(), Options{
		ManifestURL: origin.URL + "/stream.mpd",
		Dir:         t.TempDir(),
		Keys:        wrong,
		Fetcher:     &httpFetcher{client: origin.Client(), hits: map[string]int{}},
	})
	if err == nil {
		t.Fatal("expected StartNative to fail without a matching key")
	}
	var fb *FallbackError
	if !asFallback(err, &fb) {
		t.Fatalf("err = %v, want a FallbackError", err)
	}
	if fb.Reason != ReasonMissingKey {
		t.Errorf("reason = %s, want %s", fb.Reason, ReasonMissingKey)
	}
}

// The end-to-end anchor: each published track must decode to exactly the frames
// ffmpeg gets by decrypting the original DASH segments itself. Timestamps and
// container metadata are allowed to differ; the decoded frames are not.
func TestNativeOutputMatchesFFmpegDecodeOfSource(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	sources := map[string]map[string][]string{
		"hevc": {
			"video-main.m3u8": {"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s", "chunk-stream0-00003.m4s"},
			"audio-main.m3u8": {"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"},
		},
		"h264": {
			"video-main.m3u8": {"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s"},
			"audio-main.m3u8": {"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"},
		},
	}

	for dir, tracks := range sources {
		t.Run(dir, func(t *testing.T) {
			job, out := runNative(t, dir)
			select {
			case <-job.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("publication did not finish draining")
			}
			if err := job.Err(); err != nil {
				t.Fatalf("job error: %v", err)
			}
			writePlaylists(t, job, out)

			for playlist, parts := range tracks {
				encrypted := filepath.Join(out, "source-"+playlist+".mp4")
				var raw bytes.Buffer
				for _, p := range parts {
					b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cenc", dir, p))
					if err != nil {
						t.Fatalf("read %s: %v", p, err)
					}
					raw.Write(b)
				}
				if err := os.WriteFile(encrypted, raw.Bytes(), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}

				want := frameCRCs(t, encrypted, fixtureKey)
				got := frameCRCs(t, filepath.Join(out, playlist), "")
				if len(want) == 0 {
					t.Fatalf("%s: ffmpeg produced no reference frames", playlist)
				}
				if len(got) != len(want) {
					t.Fatalf("%s: got %d frames, want %d", playlist, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("%s: frame %d crc = %s, want %s", playlist, i, got[i], want[i])
					}
				}
			}
		})
	}
}

func writePlaylists(t *testing.T, job *Native, dir string) {
	t.Helper()
	for _, name := range []string{hls.MasterName, "video-main.m3u8", "audio-main.m3u8"} {
		pl, ok := job.Publication().Playlist(name)
		if !ok {
			t.Fatalf("missing playlist %s", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), pl, 0o600); err != nil {
			t.Fatalf("write playlist: %v", err)
		}
	}
}

// frameCRCs returns the decoded-frame checksums only. Container timing headers
// legitimately differ between the DASH source and the HLS output; the frames
// must not.
func frameCRCs(t *testing.T, path, key string) []string {
	t.Helper()
	args := []string{"-v", "error", "-nostdin"}
	if key != "" {
		args = append(args, "-decryption_key", key)
	}
	args = append(args, "-i", path, "-f", "framecrc", "-")
	cmd := exec.Command("ffmpeg", args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, errBuf.String())
	}
	if errBuf.Len() > 0 {
		t.Fatalf("ffmpeg reported errors for %s:\n%s", path, errBuf.String())
	}
	var crcs []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		crcs = append(crcs, strings.TrimSpace(fields[len(fields)-1]))
	}
	return crcs
}

func asFallback(err error, target **FallbackError) bool {
	for err != nil {
		if fb, ok := err.(*FallbackError); ok {
			*target = fb
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
