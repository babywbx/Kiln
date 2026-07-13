package packager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
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

	keySet := keys(t)
	if dir == "multikid" {
		keySet = multiKeys(t)
	}
	job, err := StartNative(context.Background(), Options{
		ManifestURL:   origin.URL + "/stream.mpd",
		Dir:           out,
		Keys:          keySet,
		Fetcher:       &httpFetcher{client: origin.Client(), hits: map[string]int{}},
		StartSegments: 1,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = job.Stop() })
	return job, out
}

func waitForStaticCompletion(t *testing.T, job *Native) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pl, ok := job.Publication().Playlist("video-main.m3u8")
		if ok && strings.Contains(string(pl), "#EXT-X-ENDLIST") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("static publication did not finish draining")
}

func TestNativeProducesPlayableHLS(t *testing.T) {
	job, out := runNative(t, "hevc")
	waitForStaticCompletion(t, job)

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

func TestCompletedStaticPublicationStaysAliveUntilStopped(t *testing.T) {
	job, _ := runNative(t, "hevc")
	waitForStaticCompletion(t, job)

	select {
	case <-job.Done():
		t.Fatal("completed static publication stopped serving without an explicit stop")
	default:
	}
}

func TestPublishedInitBytesAreReleasedFromTrackState(t *testing.T) {
	job, _ := runNative(t, "hevc")
	waitForStaticCompletion(t, job)
	for _, track := range job.tracks() {
		if track.init.Clear != nil {
			t.Fatalf("track %s retains %d published init bytes", track.name, len(track.init.Clear))
		}
	}
}

// The whole point of the native path: no MPEG-TS, no external process, and no
// stray copies of the media on disk.
func TestNativeWritesOnlyFinalAssets(t *testing.T) {
	job, out := runNative(t, "hevc")
	waitForStaticCompletion(t, job)
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

	waitForStaticCompletion(t, job)
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
	requireFFmpeg(t)
	type sourceTrack struct {
		parts []string
		key   string
	}
	sources := map[string]map[string]sourceTrack{
		"hevc": {
			"video-main.m3u8": {parts: []string{"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s", "chunk-stream0-00003.m4s"}, key: fixtureKey},
			"audio-main.m3u8": {parts: []string{"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"}, key: fixtureKey},
		},
		"hev1": {
			"video-main.m3u8": {parts: []string{"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s", "chunk-stream0-00003.m4s"}, key: fixtureKey},
			"audio-main.m3u8": {parts: []string{"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"}, key: fixtureKey},
		},
		"cbcs": {
			"video-main.m3u8": {parts: []string{"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s"}, key: fixtureKey},
			"audio-main.m3u8": {parts: []string{"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"}, key: fixtureKey},
		},
		"h264": {
			"video-main.m3u8": {parts: []string{"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s"}, key: fixtureKey},
			"audio-main.m3u8": {parts: []string{"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"}, key: fixtureKey},
		},
		"multikid": {
			"video-main.m3u8": {parts: []string{"init-stream0.m4s", "chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s"}, key: multiVideoKey},
			"audio-main.m3u8": {parts: []string{"init-stream1.m4s", "chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s"}, key: multiAudioKey},
		},
	}

	for dir, tracks := range sources {
		t.Run(dir, func(t *testing.T) {
			job, out := runNative(t, dir)
			waitForStaticCompletion(t, job)
			if err := job.Err(); err != nil {
				t.Fatalf("job error: %v", err)
			}
			writePlaylists(t, job, out)

			for playlist, source := range tracks {
				encrypted := filepath.Join(out, "source-"+playlist+".mp4")
				var raw bytes.Buffer
				for _, p := range source.parts {
					b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cenc", dir, p))
					if err != nil {
						t.Fatalf("read %s: %v", p, err)
					}
					raw.Write(b)
				}
				if err := os.WriteFile(encrypted, raw.Bytes(), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}

				want := frameCRCs(t, encrypted, source.key)
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

type controlledSegmentFetcher struct {
	data    []byte
	starts  chan string
	release map[string]chan struct{}
	errs    map[string]error
}

func (f *controlledSegmentFetcher) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	f.starts <- url
	if ready := f.release[url]; ready != nil {
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, url, ctx.Err()
		}
	}
	if err := f.errs[url]; err != nil {
		return nil, url, err
	}
	return bytes.Clone(f.data), url, nil
}

func pipelineNative(t *testing.T, prefetch int, fetcher Fetcher) (*Native, *trackState, []mpd.Segment, string) {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "cenc", "hevc")
	initRaw, err := os.ReadFile(filepath.Join(root, "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	init, err := cmaf.ParseInit(initRaw)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	pub, err := hls.New(hls.Config{Dir: out, Static: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := pub.AddTrack(hls.Track{Name: trackVideo, Kind: hls.KindVideo, Codec: init.Track.Codec}); err != nil {
		t.Fatal(err)
	}
	if err := pub.PublishInit(trackVideo, init.Clear); err != nil {
		t.Fatal(err)
	}
	rep := mpd.Representation{ID: "video", Addressing: mpd.Addressing{Timescale: 1}}
	n := &Native{opts: Options{Prefetch: prefetch, Fetcher: fetcher, Keys: keys(t), MaxSegmentBytes: defaultMaxSegmentBytes}, pub: pub, gate: newByteGate(segmentWorkingSet(defaultMaxSegmentBytes) * int64(prefetch)), now: time.Now, log: slog.Default()}
	ts := newTrackState(trackVideo, rep, init, time.Now())
	segs := make([]mpd.Segment, 6)
	for i := range segs {
		segs[i] = mpd.Segment{Number: uint64(i + 1), Time: uint64(i), Duration: 1, URL: fmt.Sprintf("segment-%d", i+1)}
	}
	return n, ts, segs, out
}

func fixtureSegment(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cenc", "hevc", "chunk-stream0-00001.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoStages(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary stages remain: %v", matches)
	}
}

type reservedTestFetcher struct {
	data          []byte
	calls         atomic.Int64
	reservedCalls atomic.Int64
	peaks         []int64
}

func (f *reservedTestFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	f.calls.Add(1)
	return bytes.Clone(f.data), "segment", nil
}

func (f *reservedTestFetcher) FetchReserved(_ context.Context, _ string, reserve func(int64) error) ([]byte, string, error) {
	f.calls.Add(1)
	f.reservedCalls.Add(1)
	if len(f.peaks) > 0 {
		for _, peak := range f.peaks {
			if err := reserve(peak); err != nil {
				return nil, "segment", err
			}
		}
	} else if err := reserve(int64(cap(f.data))); err != nil {
		return nil, "segment", err
	}
	return bytes.Clone(f.data), "segment", nil
}

func TestLoadInitUsesTheSharedReservationPath(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cenc", "hevc", "init-stream0.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	f := &reservedTestFetcher{data: data}
	opts := Options{
		Fetcher:         f,
		MaxSegmentBytes: defaultMaxSegmentBytes,
		Gate:            newByteGate(defaultInflightBytes),
		DownloadPool:    make(chan struct{}, 1),
	}
	_, reservation, err := loadInit(context.Background(), opts, mpd.Representation{
		ID: "video",
		Addressing: mpd.Addressing{
			InitURL: "https://origin.example.com/init.m4s",
		},
	}, defaultMaxInitBytes)
	if err != nil {
		t.Fatal(err)
	}
	reservation.release()
	if got := f.reservedCalls.Load(); got != 1 {
		t.Fatalf("reserved init fetches = %d, want 1", got)
	}
	if got := opts.Gate.usage(); got != 0 {
		t.Fatalf("gate usage after init parse = %d, want 0", got)
	}
}

func TestDownloadPoolAllowsTwoPeakGrowthsByDefault(t *testing.T) {
	n := &Native{opts: Options{MaxSegmentBytes: defaultMaxSegmentBytes}, gate: newByteGate(defaultInflightBytes)}
	if got := cap(n.downloadPool()); got != 2 {
		t.Fatalf("download slots = %d, want 2", got)
	}
}

func TestDownloadPoolAllowsOneOversizedSegment(t *testing.T) {
	n := &Native{opts: Options{MaxSegmentBytes: 64 << 20}, gate: newByteGate(8 << 20)}
	if got := cap(n.downloadPool()); got != 1 {
		t.Fatalf("download slots = %d, want 1", got)
	}
}

func TestDownloadPoolLeavesRoomForDecryptGrowth(t *testing.T) {
	if got := downloadSlots(1<<20, 32<<10); got != 31 {
		t.Fatalf("download slots = %d, want 31", got)
	}
}

func TestDefaultDecryptPoolSerializesWorkingSetGrowth(t *testing.T) {
	opts := Options{MaxSegmentBytes: defaultMaxSegmentBytes, Gate: newByteGate(defaultInflightBytes)}
	opts.applyDefaults()
	if got := cap(opts.DecryptPool); got != 1 {
		t.Fatalf("decrypt slots = %d, want 1", got)
	}
}

func TestMillisRejectsOverflow(t *testing.T) {
	if _, ok := millis(math.MaxUint64, 1); ok {
		t.Fatal("overflowing media time was accepted")
	}
	if got, ok := millis(math.MaxUint64-1, math.MaxUint64); !ok || got != 999 {
		t.Fatalf("large timescale conversion = %d, %v; want 999, true", got, ok)
	}
}

func TestStaticPublicationPrecomputesItsTargetDuration(t *testing.T) {
	rep := mpd.Representation{
		ID: "video",
		Addressing: mpd.Addressing{
			Mode:      mpd.AddressingTemplateTimeline,
			Timescale: 1,
			Timeline: []mpd.TimelineEntry{
				{Time: 0, Duration: 1},
				{Time: 1, Duration: 5},
			},
		},
	}
	pres := &mpd.Presentation{Periods: []mpd.Period{{Representations: []mpd.Representation{rep}}}}
	got, err := publicationMaxSegmentDuration(pres, Plan{Video: rep})
	if err != nil {
		t.Fatal(err)
	}
	if got != 5*time.Second {
		t.Fatalf("maximum segment duration = %s, want 5s", got)
	}
}

func TestFetchSegmentOverLimitUsesSingleRequest(t *testing.T) {
	f := &reservedTestFetcher{data: make([]byte, 1025)}
	n := &Native{opts: Options{Fetcher: f, MaxSegmentBytes: 1024}, gate: newByteGate(4096)}
	_, reservation, err := n.fetchSegment(context.Background(), "segment")
	if reservation != nil {
		reservation.release()
	}
	if err == nil {
		t.Fatal("oversized segment succeeded")
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestFetchSegmentAllowsOversizedExclusivePeak(t *testing.T) {
	f := &reservedTestFetcher{data: make([]byte, 64<<10), peaks: []int64{96 << 10, 64 << 10}}
	n := &Native{opts: Options{Fetcher: f, MaxSegmentBytes: 64 << 10}, gate: newByteGate(32 << 10)}
	_, reservation, err := n.fetchSegment(context.Background(), "segment")
	if err != nil {
		t.Fatal(err)
	}
	reservation.release()
	if got := n.gate.usage(); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

func TestFetchSegmentAccountsCapacity(t *testing.T) {
	data := make([]byte, 100, 200)
	f := &reservedTestFetcher{data: data}
	n := &Native{opts: Options{Fetcher: f, MaxSegmentBytes: 1024}, gate: newByteGate(4096)}
	raw, reservation, err := n.fetchSegment(context.Background(), "segment")
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if got, want := n.gate.usage(), int64(cap(raw)); got != want {
		t.Fatalf("usage = %d, want %d", got, want)
	}
}

func TestAcquireDecryptRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := &Native{decrypt: make(chan struct{}, 1)}
	if pool := n.acquireDecrypt(ctx); pool != nil {
		t.Fatal("cancelled context acquired decrypt token")
	}
	if got := len(n.decrypt); got != 0 {
		t.Fatalf("decrypt tokens = %d, want 0", got)
	}
}

func TestPrepareAccountsActualWorkingSetAndReleasesAfterStage(t *testing.T) {
	data := fixtureSegment(t)
	f := &controlledSegmentFetcher{data: data, starts: make(chan string, 1), release: map[string]chan struct{}{}, errs: map[string]error{}}
	n, ts, segs, _ := pipelineNative(t, 1, f)
	n.gate = newByteGate(int64(len(data)) * 4)
	n.opts.MaxSegmentBytes = int64(len(data))
	ts.segBytes.Store(1)
	observed := map[string]int64{}
	n.budgetObserved = func(phase string, usage int64) { observed[phase] = usage }
	result := n.prepare(context.Background(), ts, segs[0])
	if result.err != nil {
		t.Fatal(result.err)
	}
	if observed["estimate"] != 0 {
		t.Fatalf("estimated usage = %d, want 0", observed["estimate"])
	}
	if observed["ciphertext"] != int64(len(data)) {
		t.Fatalf("ciphertext usage = %d, want %d", observed["ciphertext"], len(data))
	}
	if observed["plaintext"] <= int64(len(data)) {
		t.Fatalf("plaintext usage = %d, want above %d", observed["plaintext"], len(data))
	}
	if observed["staged"] != 0 || n.gate.usage() != 0 {
		t.Fatalf("usage after Stage = %d, final = %d", observed["staged"], n.gate.usage())
	}
	n.pub.Discard(result.staged)
}

func TestPrepareReleasesBudgetOnAllExits(t *testing.T) {
	data := fixtureSegment(t)
	cases := []struct {
		name  string
		data  []byte
		err   error
		ctx   func() context.Context
		stage func() error
	}{
		{name: "success", data: data, ctx: context.Background},
		{name: "fetch", err: errors.New("fetch"), ctx: context.Background},
		{name: "decrypt", data: []byte("invalid"), ctx: context.Background},
		{name: "stage", data: data, ctx: context.Background, stage: func() error { return errors.New("stage") }},
		{name: "cancel", data: data, ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &controlledSegmentFetcher{data: tc.data, starts: make(chan string, 1), release: map[string]chan struct{}{}, errs: map[string]error{"segment-1": tc.err}}
			n, ts, segs, _ := pipelineNative(t, 1, f)
			n.stagePrepare = tc.stage
			result := n.prepare(tc.ctx(), ts, segs[0])
			if tc.name == "success" {
				if result.err != nil {
					t.Fatal(result.err)
				}
				n.pub.Discard(result.staged)
			} else if result.err == nil {
				t.Fatal("prepare succeeded")
			}
			if got := n.gate.usage(); got != 0 {
				t.Fatalf("usage = %d, want 0", got)
			}
		})
	}
}

func TestPrepareAllowsBoundedCiphertextConcurrency(t *testing.T) {
	data := fixtureSegment(t)
	block := make(chan struct{})
	f := &controlledSegmentFetcher{data: data, starts: make(chan string, 2), release: map[string]chan struct{}{}, errs: map[string]error{}}
	n, ts, segs, _ := pipelineNative(t, 2, f)
	n.gate = newByteGate(int64(len(data)) * 4)
	n.opts.MaxSegmentBytes = int64(len(data))
	var plaintext atomic.Int64
	n.budgetObserved = func(phase string, usage int64) {
		if phase == "plaintext" && plaintext.Add(1) == 1 {
			<-block
		}
		if usage > int64(len(data))*4 {
			t.Errorf("usage = %d exceeds limit", usage)
		}
	}
	done := make(chan prepared, 2)
	go func() { done <- n.prepare(context.Background(), ts, segs[0]) }()
	go func() { done <- n.prepare(context.Background(), ts, segs[1]) }()
	<-f.starts
	select {
	case <-f.starts:
	case <-time.After(time.Second):
		t.Fatal("second worker did not fetch concurrently")
	}
	close(block)
	for range 2 {
		result := <-done
		if result.err != nil {
			t.Fatal(result.err)
		}
		n.pub.Discard(result.staged)
	}
	if got := n.gate.usage(); got != 0 {
		t.Fatalf("usage = %d", got)
	}
}

func TestPublishSegmentsStartsAtMostPrefetchTasks(t *testing.T) {
	block := make(chan struct{})
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{}, errs: map[string]error{}}
	for i := 1; i <= 6; i++ {
		f.release[fmt.Sprintf("segment-%d", i)] = block
	}
	n, ts, segs, out := pipelineNative(t, 3, f)
	n.opts.MaxSegmentBytes = int64(len(f.data))
	n.gate = newByteGate(segmentWorkingSet(int64(len(f.data))) * 3)
	done := make(chan error, 1)
	go func() { done <- n.publishSegments(context.Background(), ts, segs) }()
	for range 3 {
		<-f.starts
	}
	select {
	case got := <-f.starts:
		t.Fatalf("started beyond window: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsCommitsHeadBeforeTailCompletes(t *testing.T) {
	tail := make(chan struct{})
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{"segment-2": tail}, errs: map[string]error{}}
	n, ts, segs, out := pipelineNative(t, 2, f)
	done := make(chan error, 1)
	go func() { done <- n.publishSegments(context.Background(), ts, segs[:2]) }()
	for range 2 {
		<-f.starts
	}
	deadline := time.After(time.Second)
	for n.pub.Frontier()[trackVideo] == 0 {
		select {
		case <-deadline:
			t.Fatal("head was not committed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(tail)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsRefillsOneSlotAfterEachCommit(t *testing.T) {
	blocks := map[string]chan struct{}{}
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: blocks, errs: map[string]error{}}
	for i := 1; i <= 4; i++ {
		blocks[fmt.Sprintf("segment-%d", i)] = make(chan struct{})
	}
	n, ts, segs, out := pipelineNative(t, 2, f)
	done := make(chan error, 1)
	go func() { done <- n.publishSegments(context.Background(), ts, segs[:4]) }()
	for range 2 {
		<-f.starts
	}
	close(blocks["segment-1"])
	if got := <-f.starts; got != "segment-3" {
		t.Fatalf("refill = %s", got)
	}
	select {
	case got := <-f.starts:
		t.Fatalf("extra refill: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(blocks["segment-2"])
	if got := <-f.starts; got != "segment-4" {
		t.Fatalf("refill = %s", got)
	}
	close(blocks["segment-3"])
	close(blocks["segment-4"])
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsPreservesMediaOrder(t *testing.T) {
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{}, errs: map[string]error{}}
	n, ts, segs, out := pipelineNative(t, 3, f)
	if err := n.publishSegments(context.Background(), ts, segs); err != nil {
		t.Fatal(err)
	}
	if ts.nextSeq != 7 || ts.lastTime != 5 {
		t.Fatalf("state = seq %d time %d", ts.nextSeq, ts.lastTime)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsCancelsWindowAfterHeadFailure(t *testing.T) {
	block := make(chan struct{})
	boom := errors.New("head")
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{"segment-2": block, "segment-3": block}, errs: map[string]error{"segment-1": boom}}
	n, ts, segs, out := pipelineNative(t, 3, f)
	if err := n.publishSegments(context.Background(), ts, segs); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if ts.nextSeq != 1 {
		t.Fatalf("next seq = %d", ts.nextSeq)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsDiscardsStagesAfterFailure(t *testing.T) {
	boom := errors.New("second")
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{}, errs: map[string]error{"segment-2": boom}}
	n, ts, segs, out := pipelineNative(t, 3, f)
	if err := n.publishSegments(context.Background(), ts, segs[:3]); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "video-main-000001.m4s")); err != nil {
		t.Fatalf("committed file removed: %v", err)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsDiscardsStagesAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	block := make(chan struct{})
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{"segment-2": block}, errs: map[string]error{}}
	n, ts, segs, out := pipelineNative(t, 2, f)
	done := make(chan error, 1)
	go func() { done <- n.publishSegments(ctx, ts, segs[:2]) }()
	for range 2 {
		<-f.starts
	}
	deadline := time.After(time.Second)
	for n.pub.Frontier()[trackVideo] == 0 {
		select {
		case <-deadline:
			t.Fatal("head was not committed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "video-main-000001.m4s")); err != nil {
		t.Fatalf("committed file removed: %v", err)
	}
	assertNoStages(t, out)
}

func TestPublishSegmentsReturnsFirstErrorInMediaOrder(t *testing.T) {
	first, later := errors.New("first"), errors.New("later")
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{}, errs: map[string]error{"segment-2": first, "segment-3": later}}
	n, ts, segs, out := pipelineNative(t, 3, f)
	if err := n.publishSegments(context.Background(), ts, segs[:3]); !errors.Is(err, first) {
		t.Fatalf("err = %v", err)
	}
	assertNoStages(t, out)
}

func TestLongStaticPublicationAdvancesBeforeTailIsPrepared(t *testing.T) {
	tail := make(chan struct{})
	f := &controlledSegmentFetcher{data: fixtureSegment(t), starts: make(chan string, 10), release: map[string]chan struct{}{"segment-3": tail}, errs: map[string]error{}}
	n, ts, segs, out := pipelineNative(t, 3, f)
	done := make(chan error, 1)
	go func() { done <- n.publishSegments(context.Background(), ts, segs) }()
	for range 3 {
		<-f.starts
	}
	deadline := time.After(time.Second)
	for n.pub.Frontier()[trackVideo] < 2 {
		select {
		case <-deadline:
			t.Fatal("publication did not advance")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(tail)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNoStages(t, out)
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

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return
	}
	if os.Getenv("KILN_REQUIRE_MEDIA_ORACLE") == "1" {
		t.Fatal("ffmpeg is required by KILN_REQUIRE_MEDIA_ORACLE=1")
	}
	t.Skip("ffmpeg not available")
}
