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

// Upgrade hops are cleared per product, so rewriting one product's grid leaves another's
// alone, and a hop is stored in the direction it was given.
func TestUpgradesAreClearedPerProductAndKeepTheirDirection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	write := func(fn func(w *Writer)) {
		t.Helper()
		w, err := db.BeginWrite()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}
		fn(w)
		if err := w.Commit("test"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	put := func(w *Writer, u Upgrade) {
		t.Helper()
		if err := w.PutUpgrade(u); err != nil {
			t.Fatalf("PutUpgrade: %v", err)
		}
	}

	write(func(w *Writer) {
		put(w, Upgrade{ProductID: 1378, FromRelease: 20, ToRelease: 10, Status: UpgradeNotSupported, Footnotes: "back in time"})
		put(w, Upgrade{ProductID: 2, FromRelease: 1, ToRelease: 2, Status: UpgradeSupported})
	})
	write(func(w *Writer) {
		if err := w.ClearUpgrades(1378); err != nil {
			t.Fatalf("ClearUpgrades: %v", err)
		}
		put(w, Upgrade{ProductID: 1378, FromRelease: 20, ToRelease: 30, Status: UpgradeSupported})
	})

	snap, err := db.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := map[[2]int]Upgrade{}
	for _, u := range snap.Upgrades {
		got[[2]int{u.FromRelease, u.ToRelease}] = u
	}
	if len(got) != 2 {
		t.Fatalf("upgrades = %+v, want the vCenter hop and the new Supervisor hop", snap.Upgrades)
	}
	if _, ok := got[[2]int{20, 10}]; ok {
		t.Error("the cleared Supervisor hop 20→10 survived ClearUpgrades")
	}
	if u, ok := got[[2]int{1, 2}]; !ok || u.Status != UpgradeSupported {
		t.Errorf("the vCenter hop 1→2 = %+v, %v; clearing the Supervisor must not touch it", u, ok)
	}
	if _, ok := got[[2]int{30, 20}]; ok {
		t.Error("hop stored reversed; upgrades are directed")
	}
}

// A version 2 cache has no upgrades table. It upgrades in place, and reads as having no
// upgrade-path data until the next refresh.
func TestSchemaVersion2UpgradesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.SetMeta("schema_version", "2"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if _, err := db.sql.Exec(`DROP TABLE upgrades`); err != nil {
		t.Fatalf("dropping upgrades: %v", err)
	}
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a version 2 cache: %v", err)
	}
	defer db.Close()
	if v, _ := db.Meta("schema_version"); v != schemaVersion {
		t.Errorf("schema_version = %q, want %q", v, schemaVersion)
	}
	snap, err := db.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snap.Upgrades) != 0 {
		t.Errorf("upgrades = %d, want 0 before a refresh", len(snap.Upgrades))
	}
}
