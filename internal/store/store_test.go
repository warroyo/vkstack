package store

import (
	"path/filepath"
	"testing"
)

// A refresh has to drop what upstream has withdrawn. Every write is an upsert, so
// without ClearMatrix a release that vanished upstream stayed in the cache, edges and
// all, and went on being offered as a real version.
func TestClearMatrixDropsWhatARefreshNoLongerWrites(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	write := func(clear bool, releases ...int) {
		t.Helper()
		w, err := db.BeginWrite()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}
		if clear {
			if err := w.ClearMatrix(); err != nil {
				t.Fatalf("ClearMatrix: %v", err)
			}
		}
		if err := w.PutProduct(Product{ID: 2, Name: "vCenter", ShortKey: "vcenter"}); err != nil {
			t.Fatalf("PutProduct: %v", err)
		}
		for _, id := range releases {
			if err := w.PutRelease(Release{ID: id, ProductID: 2, HybridVersion: "x"}); err != nil {
				t.Fatalf("PutRelease: %v", err)
			}
			if err := w.PutCompat(id, 900, StatusCompatible, ""); err != nil {
				t.Fatalf("PutCompat: %v", err)
			}
		}
		if err := w.PutPairCoverage(2, 1378, len(releases)); err != nil {
			t.Fatalf("PutPairCoverage: %v", err)
		}
		if err := w.Commit("test"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	write(false, 1, 2)
	write(true, 2)

	snap, err := db.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snap.Releases) != 1 || snap.Releases[0].ID != 2 {
		t.Errorf("releases after refresh = %+v, want only id 2", snap.Releases)
	}
	if len(snap.Compat) != 1 {
		t.Errorf("edges after refresh = %d, want 1; the withdrawn release kept its edge", len(snap.Compat))
	}
}
