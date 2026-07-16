package store_test

import (
	"testing"

	"github.com/babywbx/kiln/modules/store"
)

func BenchmarkAccessTokenLookupAndTouch(b *testing.B) {
	db, err := store.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })

	const (
		tokenID   = "benchmark-token"
		tokenHash = "benchmark-token-hash"
	)
	if err := db.InsertAccessToken(store.AccessTokenRow{
		ID:        tokenID,
		Name:      "Benchmark",
		TokenHash: tokenHash,
		Prefix:    "kiln_bench",
		ScopeJSON: `{"all":true}`,
		Enabled:   true,
		CreatedAt: 1,
	}); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		row, found, err := db.GetAccessTokenByHash(tokenHash)
		if err != nil || !found {
			b.Fatalf("lookup: found=%v err=%v", found, err)
		}
		if err := db.TouchAccessToken(row.ID); err != nil {
			b.Fatal(err)
		}
	}
}
