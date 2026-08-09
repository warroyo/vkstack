package graph

import (
	"os"
	"testing"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

// TestCoverageMatchesUpstream compares the pair coverage in a real cache against the
// set recorded in internal/model.
//
// It is the drift alarm: if upstream starts publishing Supervisor × VKr, or stops
// publishing a pair we rely on, this fails and model.knownUnpublished needs updating.
// Skipped unless VKSTACK_TEST_CACHE points at a populated cache, so `go test ./...`
// stays hermetic and offline.
func TestCoverageMatchesUpstream(t *testing.T) {
	path := os.Getenv("VKSTACK_TEST_CACHE")
	if path == "" {
		t.Skip("set VKSTACK_TEST_CACHE to a refreshed cache to check coverage against upstream")
	}

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("opening cache: %v", err)
	}
	defer db.Close()

	snap, err := db.Load()
	if err != nil {
		t.Fatalf("loading cache: %v", err)
	}
	if len(snap.Coverage) == 0 {
		t.Fatalf("cache at %s has no coverage rows — run `vkstack refresh`", path)
	}

	actual := map[[2]int]bool{}
	for _, pc := range snap.Coverage {
		actual[pair(pc.AProduct, pc.BProduct)] = pc.EdgeCount > 0
	}

	for _, pr := range model.Pairs() {
		got, ok := actual[pair(pr[0], pr[1])]
		if !ok {
			t.Errorf("pair %v was never probed by refresh", pr)
			continue
		}
		want := model.DefaultCoverage(pr[0], pr[1])
		if got != want {
			a, _ := model.ByID(pr[0])
			b, _ := model.ByID(pr[1])
			t.Errorf("%s × %s: upstream published=%v, model says published=%v — update model.knownUnpublished",
				a.Label, b.Label, got, want)
		}
	}
}
