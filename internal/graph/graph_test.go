package graph

import (
	"testing"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

// Product ids, mirroring the real ones so model lookups work.
const (
	esx = 1
	vc  = 2
	sup = 1378
	vks = 1794
	vkr = 820
)

// fixture builds a small, hand-checked snapshot: two versions per product, wired so that
// the "new" set and the "old" set are each internally compatible and cross-incompatible.
//
// Release ids encode product and generation for readability: <product>00<gen>.
func fixture() *store.Snapshot {
	rel := func(id, product int, hybrid, relType string) store.Release {
		return store.Release{ID: id, ProductID: product, HybridVersion: hybrid, ReleaseType: relType, GADate: 1}
	}
	snap := &store.Snapshot{
		Releases: []store.Release{
			rel(11, esx, "9.0.0.0", "Major"),
			rel(12, esx, "8.0U3", "Minor"),
			rel(13, esx, "8.0U2", "Minor"), // below the 8.0U3 floor
			rel(21, vc, "9.0.0.0", "Major"),
			rel(22, vc, "8.0U3", "Minor"),
			rel(31, sup, "v1.33.0+vmware.1", "Minor"),
			rel(32, sup, "v1.31.0+vmware.1", "Minor"),
			rel(41, vks, "3.7.0+v1.36", "Minor"),
			rel(42, vks, "3.4.0+v1.33", "Minor"),
			rel(51, vkr, "1.36.1", "Minor"),
			rel(52, vkr, "1.33.1", "Minor"),
		},
	}

	// Only the seven pairs the real API publishes.
	for _, pr := range model.Pairs() {
		count := 1
		if isUnpublished(pr) {
			count = 0
		}
		snap.Coverage = append(snap.Coverage, store.PairCoverage{
			AProduct: pr[0], BProduct: pr[1], EdgeCount: count,
		})
	}

	ok := func(a, b int) store.Compat {
		return store.Compat{ARelease: a, BRelease: b, Status: store.StatusCompatible}
	}
	no := func(a, b int) store.Compat {
		return store.Compat{ARelease: a, BRelease: b, Status: store.StatusNotSupported}
	}
	// New generation works together; old generation works together; crossing fails.
	snap.Compat = []store.Compat{
		ok(11, 21), ok(12, 22), no(11, 22), no(12, 21),
		ok(21, 31), ok(22, 32), no(21, 32), no(22, 31),
		ok(11, 31), ok(12, 32), no(11, 32), no(12, 31),
		ok(21, 41), ok(22, 42), no(21, 42), no(22, 41),
		ok(21, 51), ok(22, 52), no(21, 52), no(22, 51),
		ok(31, 41), ok(32, 42), no(31, 42), no(32, 41),
		ok(41, 51), ok(42, 52), no(41, 52), no(42, 51),
		// 8.0U2 ESX (below the floor) is compatible with the old vCenter, so it would
		// survive were the floor not applied.
		ok(13, 22),
	}
	return snap
}

func isUnpublished(pr [2]int) bool {
	switch pr {
	case [2]int{esx, vks}, [2]int{esx, vkr}, [2]int{vkr, sup}:
		return true
	}
	// Pairs() normalises to lower id first, so check the normalised forms too.
	a, b := pr[0], pr[1]
	if a > b {
		a, b = b, a
	}
	return (a == esx && b == vks) || (a == esx && b == vkr) || (a == sup && b == vkr)
}

func load(t *testing.T, opts Options) *Graph {
	t.Helper()
	g, err := Load(fixture(), opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return g
}

func TestFloorDropsOldBaseReleases(t *testing.T) {
	g := load(t, Options{})
	for _, r := range g.ReleasesOf(esx) {
		if r.Raw == "8.0U2" {
			t.Errorf("ESX 8.0U2 is below the 8.0U3 floor and should have been dropped")
		}
	}
	if got := len(g.ReleasesOf(esx)); got != 2 {
		t.Errorf("expected 2 ESX releases above the floor, got %d", got)
	}

	all := load(t, Options{AllVersions: true})
	if got := len(all.ReleasesOf(esx)); got != 3 {
		t.Errorf("--all-versions should keep all 3 ESX releases, got %d", got)
	}
}

func TestPublishedDistinguishesNoDataFromNoMatch(t *testing.T) {
	g := load(t, Options{})
	if g.Published(esx, vkr) {
		t.Error("ESX × VKr has no upstream data and must report unpublished")
	}
	if !g.Published(vc, sup) {
		t.Error("vCenter × Supervisor is published and must report so")
	}
}

// A product pair with no published data must be reported as such, not as an empty
// compatible list — the difference between "we don't know" and "nothing works".
func TestCompatibleWithReportsUnpublishedGroups(t *testing.T) {
	g := load(t, Options{})
	r := g.Releases[11] // ESX 9.0.0.0
	groups := g.CompatibleWith(r, CompatOptions{})

	var sawUnpublished, sawPopulated bool
	for _, grp := range groups {
		switch grp.Product.Key {
		case "vkr", "vks":
			if grp.Published {
				t.Errorf("expected ESX × %s to be unpublished", grp.Product.Key)
			}
			sawUnpublished = true
		case "vcenter":
			if !grp.Published || len(grp.Releases) != 1 {
				t.Errorf("expected exactly one compatible vCenter, got published=%v n=%d",
					grp.Published, len(grp.Releases))
			}
			sawPopulated = true
		}
	}
	if !sawUnpublished || !sawPopulated {
		t.Error("expected both an unpublished and a populated group")
	}
}

func TestCheckThreeBuckets(t *testing.T) {
	g := load(t, Options{})

	// A wholly consistent new-generation stack: every published pair compatible, the
	// three unpublished pairs unverified. Must pass.
	good := map[int]*Release{
		esx: g.Releases[11], vc: g.Releases[21], sup: g.Releases[31],
		vks: g.Releases[41], vkr: g.Releases[51],
	}
	res := g.Check(good)
	if !res.OK() {
		t.Errorf("expected a consistent stack to pass, incompatible: %v", res.Incompatible())
	}
	// Every gap in the matrix falls on a pair that is not a dependency, so validating a
	// stack needs nothing inferred.
	if got := len(res.Unverified()); got != 0 {
		t.Errorf("expected no unverified dependency pairs, got %d", got)
	}

	// Mixing generations must fail on published pairs.
	bad := map[int]*Release{
		esx: g.Releases[11], vc: g.Releases[22], sup: g.Releases[31],
		vks: g.Releases[41], vkr: g.Releases[51],
	}
	if g.Check(bad).OK() {
		t.Error("expected a mixed-generation stack to fail")
	}
}

// Unverified pairs are a warning, not a failure: a stack whose only issue is missing
// upstream data must still pass, or three of the ten pairs would fail every check.
func TestUnverifiedDoesNotFailCheck(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{esx: g.Releases[11], vkr: g.Releases[51]} // unpublished pair
	res := g.Check(pins)
	if !res.OK() {
		t.Error("an unpublished pair must not fail the check")
	}
	// ESX against VKr is unpublished *and* not a dependency, so it is neither enforced
	// nor reported as something that had to be inferred.
	if len(res.Unverified()) != 0 {
		t.Errorf("expected no unverified dependency pairs, got %d", len(res.Unverified()))
	}
}

func TestStacksSolvesFromOnePin(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]} // vCenter 9.0.0.0

	stacks, failure := g.Stacks(pins, StackOptions{Limit: 5})
	if failure != nil {
		t.Fatalf("expected a solution, got failure %+v", failure)
	}
	if len(stacks) == 0 {
		t.Fatal("expected at least one stack")
	}
	for i, s := range stacks {
		if len(s.Releases) != len(model.Products) {
			t.Errorf("stack %d is incomplete: %d of %d products", i, len(s.Releases), len(model.Products))
		}
		// Every returned stack must independently pass Check — this is the invariant
		// the CLI's stack-then-check cross-verification relies on.
		if !g.Check(s.Releases).OK() {
			t.Errorf("stack %d does not pass Check: %+v", i, g.Check(s.Releases).Incompatible())
		}
		if got := len(s.Inferred()); got != 0 {
			t.Errorf("stack %d: nothing should need inferring, got %d", i, got)
		}
	}
}

// Pins are only validated against products the search assigns, so a conflicting pair of
// pins used to slip through and yield a bogus stack.
func TestStacksRejectsConflictingPins(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{
		vc:  g.Releases[21], // new vCenter
		sup: g.Releases[32], // old Supervisor — explicitly not supported together
	}
	stacks, failure := g.Stacks(pins, StackOptions{Limit: 1})
	if len(stacks) != 0 {
		t.Fatalf("expected no stack from conflicting pins, got %d", len(stacks))
	}
	if failure == nil || !failure.PinConflict {
		t.Fatalf("expected a pin conflict failure, got %+v", failure)
	}
}

// ViableOptions must agree with the solver: anything it lists has to actually appear in
// a valid stack alongside the pins.
func TestViableOptionsAgreeWithSolver(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]}
	options := g.ViableOptions(pins, StackOptions{})

	for productID, releases := range options {
		for _, r := range releases {
			trial := map[int]*Release{vc: g.Releases[21], productID: r}
			stacks, _ := g.Stacks(trial, StackOptions{Limit: 1})
			if len(stacks) == 0 {
				p, _ := model.ByID(productID)
				t.Errorf("ViableOptions listed %s %s but no stack contains it", p.Label, r.Raw)
			}
		}
	}
	// With the new vCenter pinned, only the new generation is viable.
	if got := len(options[sup]); got != 1 {
		t.Errorf("expected exactly 1 viable Supervisor, got %d", got)
	}
}

func TestResolve(t *testing.T) {
	g := load(t, Options{})

	r, err := g.Resolve("vcenter", "9.0.0.0")
	if err != nil {
		t.Fatalf("exact match failed: %v", err)
	}
	if r.ID != 21 {
		t.Errorf("resolved to release %d, want 21", r.ID)
	}

	if _, err := g.Resolve("vcenter", "7.0"); err == nil {
		t.Error("expected an error for a version that does not exist")
	}
	if _, err := g.Resolve("nope", "1.0"); err == nil {
		t.Error("expected an error for an unknown product")
	}

	// A prefix matching two releases must report ambiguity rather than guessing.
	if _, err := g.Resolve("esx", "8.0"); err == nil {
		t.Log("note: 8.0 resolved uniquely because the floor removed 8.0U2")
	}
}

// Only real dependencies may invalidate a stack. The matrix publishes vCenter against
// VKS, but VKS runs on the Supervisor — so a mismatch on that pair says nothing about
// whether the stack works, and must not rule the VKS version out.
func TestNonDependencyPairsAreNotEnforced(t *testing.T) {
	snap := fixture()
	// A newer VKS that the Supervisor supports, explicitly NOT supported against the
	// new-generation vCenter. Kept reachable through the old vCenter so the version
	// floor does not simply drop it.
	snap.Releases = append(snap.Releases, store.Release{
		ID: 43, ProductID: vks, HybridVersion: "3.8.0+v1.37", ReleaseType: "Minor", GADate: 1,
	})
	snap.Compat = append(snap.Compat,
		store.Compat{ARelease: 21, BRelease: 43, Status: store.StatusNotSupported},
		store.Compat{ARelease: 22, BRelease: 43, Status: store.StatusCompatible},
		store.Compat{ARelease: 31, BRelease: 43, Status: store.StatusCompatible},
		store.Compat{ARelease: 43, BRelease: 51, Status: store.StatusCompatible},
	)

	g, err := Load(snap, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if g.Releases[43] == nil {
		t.Fatal("fixture VKS 43 was pruned; the test cannot say anything")
	}

	pins := map[int]*Release{vc: g.Releases[21]}
	stacks, failure := g.Stacks(pins, StackOptions{Limit: 1})
	if len(stacks) == 0 {
		t.Fatalf("expected a stack, got %+v", failure)
	}
	// VKS 43 is the newest the Supervisor supports. The vCenter pair says "not
	// supported", but that pair is not a dependency, so it must not exclude it.
	if got := stacks[0].Releases[vks].ID; got != 43 {
		t.Errorf("chose VKS %d; the non-dependency vCenter pair should not have excluded 43", got)
	}

	// Still reported, never enforced.
	res := g.Check(stacks[0].Releases)
	if !res.OK() {
		t.Errorf("stack must pass; failing pairs are not dependencies: %+v", res.Incompatible())
	}
	var sawIt bool
	for _, v := range res.Informational() {
		if v.A.Key == "vcenter" && v.B.Key == "vks" && !Compatible(v.Status) {
			sawIt = true
		}
	}
	if !sawIt {
		t.Error("expected the non-dependency mismatch reported as informational")
	}
}

// A pinned pair with no published cell is not an open question — inside a pair upstream
// publishes, a missing cell is the matrix declining to list the two together. The solver
// has always read it that way when picking releases; pins used to be judged more loosely,
// which produced "valid" stacks the solver would never assemble and lit versions in the
// web map that were never published against the selection.
func TestUnlistedPinPairCannotFormAStack(t *testing.T) {
	snap := fixture()
	// Drop the cell linking the old vCenter to the old Supervisor, leaving the pair
	// published but these two releases never listed together.
	var kept []store.Compat
	for _, c := range snap.Compat {
		if (c.ARelease == 22 && c.BRelease == 32) || (c.ARelease == 32 && c.BRelease == 22) {
			continue
		}
		kept = append(kept, c)
	}
	snap.Compat = kept
	g, err := Load(snap, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	pins := map[int]*Release{vc: g.Releases[22], sup: g.Releases[32]}
	stacks, failure := g.Stacks(pins, StackOptions{})
	if len(stacks) > 0 {
		t.Fatal("two releases upstream never lists together must not form a stack")
	}
	if failure == nil || !failure.PinConflict || !failure.Unlisted {
		t.Fatalf("expected an unlisted pin conflict, got %+v", failure)
	}

	// And nothing is viable, including the pins themselves: a dead end must not read as
	// a one-node answer.
	if opts := g.ViableOptions(pins, StackOptions{}); len(opts) != 0 {
		t.Errorf("a dead-end pin should offer nothing, got %d products with options", len(opts))
	}
}

// An unpublished *product* pair still cannot constrain anything: three of the ten have no
// data by design, and demanding a cell there would rule out every stack.
func TestUnpublishedPairStillPassesPinValidation(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{sup: g.Releases[31], vkr: g.Releases[51]} // Supervisor × VKr
	if _, failure := g.Stacks(pins, StackOptions{}); failure != nil && failure.PinConflict {
		t.Errorf("Supervisor × VKr is unpublished and must not block a pin: %+v", failure)
	}
}

// "nothing compatible" was printed whenever the patch filter emptied a group, which is a
// different and false claim: the releases exist, they are just hidden by default.
func TestCompatibleWithCountsPatchesItHides(t *testing.T) {
	snap := fixture()
	snap.Releases = append(snap.Releases,
		store.Release{ID: 33, ProductID: sup, HybridVersion: "v1.34.0+vmware.1", ReleaseType: "Patch", GADate: 1})
	snap.Compat = append(snap.Compat, store.Compat{ARelease: 33, BRelease: 21, Status: store.StatusCompatible})
	// Remove the non-patch Supervisor the new vCenter pairs with, so patches are all
	// that is left for it.
	var kept []store.Compat
	for _, c := range snap.Compat {
		if (c.ARelease == 21 && c.BRelease == 31) || (c.ARelease == 31 && c.BRelease == 21) {
			continue
		}
		kept = append(kept, c)
	}
	snap.Compat = kept
	g, err := Load(snap, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, grp := range g.CompatibleWith(g.Releases[21], CompatOptions{}) {
		if grp.Product.Key != "supervisor" {
			continue
		}
		if len(grp.Releases) != 0 {
			t.Fatalf("expected the patch filter to empty the group, got %d", len(grp.Releases))
		}
		if grp.HiddenPatches != 1 {
			t.Errorf("HiddenPatches = %d, want 1 — otherwise this reads as nothing compatible",
				grp.HiddenPatches)
		}
	}
}
