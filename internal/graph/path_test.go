package graph

import (
	"testing"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

// pathFixture is a small vSphere 8 → 9 world shaped like the real one:
//
//   - vCenter 8.0U3 takes Supervisor 1.28 only; 8.0U3g takes 1.28–1.30; 9.1.0.0 takes
//     vsc0 1.30 and vsc9 1.30–1.31; 9.1.1.0 takes vsc9 1.31 only.
//   - ESX 8 pairs only with vsc0 Supervisors, and ESX 9 only with vsc9.
//   - Avi "old" pairs with vCenter 8 and vsc0; "bridge" pairs with 8.0U3g and 9.1.0.0 and
//     with vsc0 1.30 and vsc9 1.30; "new" pairs with 9.1.x and vsc9 only.
//
// Release ids: 1xx vCenter, 2xx ESX, 3xx Supervisor, 7xx Avi, 5xx VKr.
func pathFixture() *store.Snapshot {
	rel := func(id, product int, hybrid string) store.Release {
		return store.Release{ID: id, ProductID: product, HybridVersion: hybrid, ReleaseType: "Minor", GADate: 1}
	}
	const (
		vc803, vc803g, vc910, vc911 = 101, 102, 103, 104
		esx8, esx9                  = 201, 202
		s128, s129, s130, s130n9    = 301, 302, 303, 304
		s131n9                      = 305
		aviOld, aviBridge, aviNew   = 701, 702, 703
		k128, k129, k130, k131      = 501, 502, 503, 504
	)
	snap := &store.Snapshot{Releases: []store.Release{
		rel(vc803, vc, "8.0U3"), rel(vc803g, vc, "8.0U3g"), rel(vc910, vc, "9.1.0.0"), rel(vc911, vc, "9.1.1.0"),
		rel(esx8, esx, "8.0U3"), rel(esx9, esx, "9.1.1.0"),
		rel(s128, sup, "v1.28.3+vmware.2-fips.1-vsc0.1.9"),
		rel(s129, sup, "v1.29.7+vmware.1-fips-vsc0.1.10"),
		rel(s130, sup, "v1.30.10+vmware.1-fips-vsc0.1.12"),
		rel(s130n9, sup, "v1.30.14+vmware.8-fips-vsc9.1.0.0"),
		rel(s131n9, sup, "v1.31.11+vmware.8-fips-vsc9.1.0.0"),
		rel(aviOld, avi, "22.1.7"), rel(aviBridge, avi, "31.2.2"), rel(aviNew, avi, "32.1.3"),
		rel(k128, vkr, "1.28.8"), rel(k129, vkr, "1.29.15"), rel(k130, vkr, "1.30.14"), rel(k131, vkr, "1.31.14"),
	}}
	for _, pr := range model.Pairs() {
		count := 1
		if isUnpublished(pr) {
			count = 0
		}
		snap.Coverage = append(snap.Coverage, store.PairCoverage{AProduct: pr[0], BProduct: pr[1], EdgeCount: count})
	}
	yes := map[[2]int]bool{}
	allow := func(a int, bs ...int) {
		for _, b := range bs {
			yes[[2]int{a, b}] = true
		}
	}
	allow(vc803, s128)
	allow(vc803g, s128, s129, s130)
	allow(vc910, s130, s130n9, s131n9)
	allow(vc911, s131n9)
	allow(esx8, s128, s129, s130)
	allow(esx9, s130n9, s131n9)
	allow(esx8, vc803, vc803g, vc910, vc911)
	allow(esx9, vc910, vc911)
	allow(aviOld, vc803, vc803g, s128, s129, s130)
	allow(aviBridge, vc803g, vc910, s130, s130n9)
	allow(aviNew, vc910, vc911, s130n9, s131n9)

	all := []int{}
	for _, r := range snap.Releases {
		all = append(all, r.ID)
	}
	byID := map[int]int{}
	for _, r := range snap.Releases {
		byID[r.ID] = r.ProductID
	}
	// Every cross-product pair the fixture cares about gets an explicit answer, so a
	// missing cell never stands in for a "no" by accident.
	for i, a := range all {
		for _, b := range all[i+1:] {
			if byID[a] == byID[b] {
				continue
			}
			st := store.StatusNotSupported
			if yes[[2]int{a, b}] || yes[[2]int{b, a}] {
				st = store.StatusCompatible
			}
			snap.Compat = append(snap.Compat, store.Compat{ARelease: a, BRelease: b, Status: st})
		}
	}
	return snap
}

func loadPath(t *testing.T) *Graph {
	t.Helper()
	g, err := Load(pathFixture(), Options{AllVersions: true})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return g
}

func pins(g *Graph, ids ...int) map[int]*Release {
	out := map[int]*Release{}
	for _, id := range ids {
		r := g.Releases[id]
		out[r.ProductID] = r
	}
	return out
}

func stepsString(res *PathResult) []string {
	var out []string
	for _, s := range res.Steps {
		out = append(out, s.Product.Key+" "+s.To.Raw)
	}
	return out
}

func TestPathClimbsSupervisorOneMinorAtATimeAndInterleavesVCenter(t *testing.T) {
	g := loadPath(t)
	res, fail := g.Path(pins(g, 101, 301), pins(g, 104, 305), PathOptions{})
	if fail != nil {
		t.Fatalf("no path: %v", fail)
	}
	want := []string{
		"vcenter 8.0U3g",
		"supervisor v1.29.7+vmware.1-fips-vsc0.1.10",
		"supervisor v1.30.10+vmware.1-fips-vsc0.1.12",
		"vcenter 9.1.0.0",
		// The train switch can ride along with the next minor: one Supervisor upgrade.
		"supervisor v1.31.11+vmware.8-fips-vsc9.1.0.0",
		"vcenter 9.1.1.0",
	}
	got := stepsString(res)
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d = %q, want %q (all: %v)", i+1, got[i], want[i], got)
		}
	}
}

func TestPathRoutesAviThroughTheBridgeRelease(t *testing.T) {
	g := loadPath(t)
	res, fail := g.Path(pins(g, 101, 301, 701), pins(g, 104, 305, 703), PathOptions{})
	if fail != nil {
		t.Fatalf("no path: %v", fail)
	}
	seenBridge := false
	for _, s := range res.Steps {
		if s.Product.Key == "avi" && s.To.Raw == "31.2.2" {
			seenBridge = true
		}
	}
	if !seenBridge {
		t.Fatalf("Avi must pass through the bridge release 31.2.2; got %v", stepsString(res))
	}
}

func TestPathNamesTheOptionalProductThatBlocksIt(t *testing.T) {
	g := loadPath(t)
	// Without the bridge release there is no way across.
	_, fail := g.Path(pins(g, 101, 301, 701), pins(g, 104, 305, 703),
		PathOptions{Exclude: map[int]bool{702: true}})
	if fail == nil || fail.Reason != "no_route" {
		t.Fatalf("want no_route, got %+v", fail)
	}
	if len(fail.BlockedBy) != 1 || fail.BlockedBy[0].Key != "avi" {
		t.Fatalf("BlockedBy = %v, want avi", fail.BlockedBy)
	}
}

func TestPathAllowsTheTransitionalESXState(t *testing.T) {
	g := loadPath(t)
	res, fail := g.Path(pins(g, 103, 201, 303), pins(g, 104, 202, 305), PathOptions{})
	if fail != nil {
		t.Fatalf("no path: %v", fail)
	}
	transitional := 0
	for i, s := range res.Steps {
		if s.Transitional {
			transitional++
			if s.Product.Key != "supervisor" {
				t.Fatalf("step %d is transitional but moves %s; the Supervisor goes first", i+1, s.Product.Key)
			}
		}
	}
	if transitional != 1 {
		t.Fatalf("want exactly one transitional state, got %d in %v", transitional, stepsString(res))
	}
}

func TestPathMovesGuestClustersOneMinorAtATime(t *testing.T) {
	g := loadPath(t)
	res, fail := g.Path(pins(g, 102, 303, 501), pins(g, 102, 303, 504), PathOptions{})
	if fail != nil {
		t.Fatalf("no path: %v", fail)
	}
	// 1.28 → 1.29 → 1.30 → 1.31, and nothing ties VKr to the Supervisor's own minor:
	// the Supervisor stays on 1.30 while the clusters go to 1.31.
	want := []string{"vkr 1.29.15", "vkr 1.30.14", "vkr 1.31.14"}
	got := stepsString(res)
	if len(got) != len(want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step %d = %q, want %q", i+1, got[i], want[i])
		}
	}
}

func TestPathRefusesToMoveBackwards(t *testing.T) {
	g := loadPath(t)
	_, fail := g.Path(pins(g, 104), pins(g, 101), PathOptions{})
	if fail == nil || fail.Reason != "backwards" {
		t.Fatalf("want backwards, got %+v", fail)
	}
}
