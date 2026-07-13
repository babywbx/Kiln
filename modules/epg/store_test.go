package epg_test

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/epg"
)

func TestDiskStorePersistsAnAtomicCacheEntry(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	store, err := epg.NewDiskStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	want := epg.CacheEntry{
		SourceID:  "../custom/source",
		Data:      []byte(`<tv><channel id="one"></channel></tv>`),
		Metadata:  epg.CacheMetadata{ETag: `"version"`},
		UpdatedAt: time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC),
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	want.Data = []byte(`<tv><channel id="new"></channel></tv>`)
	want.Metadata.ETag = `"new-version"`
	want.UpdatedAt = want.UpdatedAt.Add(time.Hour)
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].IsDir() {
		t.Fatalf("cache directory = %+v, want one regular file", files)
	}

	reopened, err := epg.NewDiskStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.Load(want.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("saved cache entry was not found")
	}
	if !bytes.Equal(got.Data, want.Data) || got.Metadata.ETag != want.Metadata.ETag || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("entry = %+v, want %+v", got, want)
	}
}

func TestMemoryStoreDoesNotExposeMutableCacheBytes(t *testing.T) {
	t.Parallel()

	store := epg.NewMemoryStore()
	entry := epg.CacheEntry{SourceID: "one", Data: []byte("original")}
	if err := store.Save(entry); err != nil {
		t.Fatal(err)
	}
	entry.Data[0] = 'X'
	loaded, found, err := store.Load("one")
	if err != nil || !found {
		t.Fatalf("Load() = %+v, %v, %v", loaded, found, err)
	}
	loaded.Data[0] = 'Y'
	again, _, _ := store.Load("one")
	if string(again.Data) != "original" {
		t.Fatalf("cached data was mutated: %q", again.Data)
	}
}
