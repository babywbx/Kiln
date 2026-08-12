package epg_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

func newTestStore(t testing.TB) *epg.Store {
	t.Helper()
	store, err := epg.NewMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestDiskStoreKeepsIngestedGuideAcrossRestarts(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	store, err := epg.NewDiskStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`<tv><channel id="one"><display-name>One</display-name></channel>` +
		`<programme channel="one" start="20260713080000 +0800"><title>First</title></programme></tv>`)
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{"source": {Data: raw}}}, store)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(directory, "epg.db")); err != nil {
		t.Fatalf("EPG database was not created: %v", err)
	}
	reopened, err := epg.NewDiskStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{{ID: "source", Timezone: "Asia/Hong_Kong"}},
	}, &fakeSourceFetcher{errors: map[string]error{"source": errNotAvailable}}, reopened)

	payload, err := restarted.XML([]epg.ChannelRef{{ID: "kiln-one", EPGID: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "First") {
		t.Fatalf("reopened store lost the ingested guide:\n%s", payload)
	}
	statuses := restarted.Statuses()
	if len(statuses) != 1 || !statuses[0].Available || statuses[0].ProgrammeCount != 1 {
		t.Fatalf("statuses = %+v", statuses)
	}
}

func TestDiskStoreRestrictsFilePermissions(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes only carry the read-only bit")
	}
	directory := t.TempDir()
	store, err := epg.NewDiskStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	info, err := os.Stat(filepath.Join(directory, "epg.db"))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		t.Fatalf("EPG database permissions = %v, want no group or world access", permissions)
	}
}

func TestDiskStoreMigrationIsIdempotent(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	for range 3 {
		store, err := epg.NewDiskStore(directory)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
