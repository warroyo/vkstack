package graph

import (
	"fmt"
	"os"
	"testing"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/version"
)

// The invariant suite runs the solver against a real refreshed cache and checks the
// answers against the data they came from.
//
// It exists because the unit tests build small synthetic graphs, which prove the solver
// obeys the rules it is given but say nothing about how those rules behave on upstream's
// actual shape. Skipped unless VKSTACK_TEST_CACHE points at a populated cache, so
// `go test ./...` stays hermetic and offline.
//
// Note what is deliberately *not* asserted: that no stack contains a pair upstream marks
// NOT SUPPORTED. Pairs outside the constraint set are allowed to say no — that is the
// whole point of the constraint set being narrower than the published matrix, and
// upstream publishes plenty of such cells. See model.Constrains.
func realGraph(t *testing.T) *Graph { return realGraphOpts(t, Options{}) }

// realGraphOpts loads the same cache under a specific set of load-time filters.
func realGraphOpts(t *testing.T, opts Options) *Graph {
	t.Helper()
	path := os.Getenv("VKSTACK_TEST_CACHE")
	if path == "" {
		t.Skip("set VKSTACK_TEST_CACHE to a refreshed cache to run the solver invariants")
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("opening cache: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	snap, err := db.Load()
	if err != nil {
		t.Fatalf("loading cache: %v", err)
	}
	g, err := Load(snap, opts)
	if err != nil {
		t.Fatalf("loading graph: %v", err)
	}
	if len(g.Releases) == 0 {
		t.Fatalf("cache at %s has no releases — run `vkstack refresh`", path)
	}
	return g
}

// eachPin calls fn for every non-patch release of every product, pinned on its own.
//
// Solving from one pin per release is the query the tool exists to answer, so sweeping it
// is the closest thing to exercising real use.
func eachPin(t *testing.T, g *Graph, include []string, fn func(t *testing.T, p model.Product, r *Release, s Stack)) {
	t.Helper()
	optional := map[string]bool{"nsx": true, "avi": true}
	wanted := map[string]bool{}
	for _, k := range include {
		wanted[k] = true
	}

	solved := 0
	for _, p := range model.Products {
		if optional[p.Key] && !wanted[p.Key] {
			continue // cannot pin a product that is not in the solve
		}
		for _, r := range g.ReleasesOf(p.ID) {
			if r.IsPatch() {
				continue
			}
			stacks, _ := g.Stacks(map[int]*Release{p.ID: r}, StackOptions{Include: include})
			if len(stacks) == 0 {
				continue // no stack for this pin is a legitimate answer
			}
			solved++
			fn(t, p, r, stacks[0])
		}
	}
	if solved == 0 {
		t.Fatalf("no pin produced a stack for include=%v — the sweep proved nothing", include)
	}
	t.Logf("include=%v: checked %d solved stacks", include, solved)
}

// TestSolvedStacksSatisfyEveryConstrainingPair is the core invariant: whatever the solver
// hands back, every pair that is allowed to decide the stack must be a published yes.
//
// A failure here means the tool recommended a combination its own data contradicts, which
// is the one thing it must never do.
func TestSolvedStacksSatisfyEveryConstrainingPair(t *testing.T) {
	g := realGraph(t)
	for _, include := range [][]string{nil, {"nsx"}, {"avi"}, {"nsx", "avi"}} {
		eachPin(t, g, include, func(t *testing.T, p model.Product, r *Release, s Stack) {
			for _, v := range g.Check(s.Releases).Blocking() {
				t.Errorf("pin %s %s: stack contains %s %s × %s %s, which upstream does not publish as compatible (status %d)",
					p.Label, r.Raw, v.A.Label, v.ARelease.Raw, v.B.Label, v.BRelease.Raw, v.Status)
			}
		})
	}
}

// TestSolvedStacksKeepTheirPins guards the other half of the contract: the answer must
// contain what the caller asked for, not a nearby version the solver found easier.
func TestSolvedStacksKeepTheirPins(t *testing.T) {
	g := realGraph(t)
	eachPin(t, g, nil, func(t *testing.T, p model.Product, r *Release, s Stack) {
		got, ok := s.Releases[p.ID]
		switch {
		case !ok:
			t.Errorf("pin %s %s: solved stack has no %s at all", p.Label, r.Raw, p.Label)
		case got.ID != r.ID:
			t.Errorf("pin %s %s: solver returned %s instead", p.Label, r.Raw, got.Raw)
		case !s.Pinned[p.ID]:
			t.Errorf("pin %s %s: release is not marked pinned", p.Label, r.Raw)
		}
	})
}

// TestRequiredProductsAlwaysPresent checks that a stack is complete for the five products
// every deployment has. A stack missing one of them is a partial answer presented as a
// whole one.
func TestRequiredProductsAlwaysPresent(t *testing.T) {
	g := realGraph(t)
	eachPin(t, g, nil, func(t *testing.T, p model.Product, r *Release, s Stack) {
		for _, want := range model.Products {
			if want.Key == "nsx" || want.Key == "avi" {
				continue
			}
			if _, ok := s.Releases[want.ID]; !ok {
				t.Errorf("pin %s %s: stack is missing %s", p.Label, r.Raw, want.Label)
			}
		}
	})
}

// TestOptionalProductsStayIndependent checks that opting into one optional product never
// drags the other in. Asking for Avi and silently getting NSX would change what the user
// is being told to deploy.
func TestOptionalProductsStayIndependent(t *testing.T) {
	g := realGraph(t)
	nsx, _ := model.ByKey("nsx")
	avi, _ := model.ByKey("avi")

	for _, tc := range []struct {
		include []string
		absent  model.Product
	}{
		{include: []string{"avi"}, absent: nsx},
		{include: []string{"nsx"}, absent: avi},
		{include: nil, absent: nsx},
		{include: nil, absent: avi},
	} {
		eachPin(t, g, tc.include, func(t *testing.T, p model.Product, r *Release, s Stack) {
			if got, ok := s.Releases[tc.absent.ID]; ok {
				t.Errorf("include=%v, pin %s %s: stack contains %s %s, which was never asked for",
					tc.include, p.Label, r.Raw, tc.absent.Label, got.Raw)
			}
		})
	}
}

// TestPatchIsUpstreamsDefinition guards the alignment with the matrix's own isHidePatch
// toggle.
//
// Setting that flag upstream drops exactly the releases typed "Patch". Everything else —
// "Maint", "Maintenance", "Unknown", and releases the matrix returns with no type at all —
// survives it, and must survive here too. If upstream introduces a new type, or starts
// spelling an existing one differently, this fails rather than silently reclassifying
// releases the tool then hides or offers.
func TestPatchIsUpstreamsDefinition(t *testing.T) {
	g := realGraph(t)

	// Every type upstream is known to use. "Maint" and "Maintenance" both occur, and both
	// mean a release that is not a patch.
	known := map[string]bool{
		"Patch": true, "Maint": true, "Maintenance": true,
		"Major": true, "Minor": true, "Unknown": true, "": true,
	}
	seen := map[string]int{}
	for _, r := range g.Releases {
		seen[r.ReleaseType]++
		if !known[r.ReleaseType] {
			t.Errorf("release %s %s has unknown releaseType %q — decide whether upstream's isHidePatch hides it before shipping",
				r.ProductKey, r.Raw, r.ReleaseType)
		}
		if got, want := r.IsPatch(), r.ReleaseType == "Patch"; got != want {
			t.Errorf("release %s %s: IsPatch=%v for type %q", r.ProductKey, r.Raw, got, r.ReleaseType)
		}
	}
	if seen["Patch"] == 0 {
		t.Error("no Patch releases in the cache at all — the filter is untested against real data")
	}
	t.Logf("release types in cache: %v", seen)
}

// TestCompatibilityIsPerReleaseNotPerLine proves the tool reads compatibility at the
// individual release, which is the level upstream publishes it at.
//
// The risk this guards is a rollup: treating a version line as compatible because some
// member of it is. That would be inventing an answer. The test looks for real evidence
// that members of one line disagree about the same peer, and fails if the disagreement
// stops being visible — either because a rollup crept in, or because the releases that
// demonstrate it were dropped.
func TestCompatibilityIsPerReleaseNotPerLine(t *testing.T) {
	g := realGraph(t)

	// Group each product's releases by the line their version shares, then look for two
	// members that disagree about any single peer release.
	disagreements := 0
	for _, p := range model.Products {
		byLine := map[string][]*Release{}
		for _, r := range g.ReleasesOf(p.ID) {
			key := r.Version.Raw
			if len(r.Version.Key) > 0 && len(r.Version.Key[0]) >= 2 {
				key = fmt.Sprintf("%d.%d", r.Version.Key[0][0], r.Version.Key[0][1])
			}
			byLine[key] = append(byLine[key], r)
		}
		for line, members := range byLine {
			if len(members) < 2 {
				continue
			}
			for _, a := range members {
				for _, b := range members {
					if a.ID >= b.ID {
						continue
					}
					for _, e := range g.Compat[a.ID] {
						peer := g.Releases[e.Peer]
						if peer == nil {
							continue
						}
						other, ok := g.Status(b.ID, peer.ID)
						if !ok || Compatible(e.Status) == Compatible(other) {
							continue
						}
						disagreements++
						if disagreements == 1 {
							t.Logf("example: %s line %s — %s and %s disagree about %s %s (%d vs %d)",
								p.Label, line, a.Raw, b.Raw, peer.ProductKey, peer.Raw, e.Status, other)
						}
					}
				}
			}
		}
	}
	if disagreements == 0 {
		t.Error("no two releases of one version line disagree about any peer — either the data changed shape or compatibility is being rolled up to the line")
	}
	t.Logf("release pairs within a line that disagree about a peer: %d", disagreements)
}

// TestRecommendationPrefersTheNewestPlatform guards what "newest valid combination" means.
//
// The recommendation is the first complete assignment the search finds, so the order
// products are filled in silently decides which product's newness the whole stack
// inherits. With Supervisor filled first, pinning VKr 1.36 recommended Supervisor 1.33.9
// on the vsc0 train — whose only vCenter is 8.0U3k — and reported a vSphere 8 stack while
// a vSphere 9.1 one carrying the same VKr existed and went unmentioned.
//
// The check is deliberately independent of the search order: for each pin it finds the
// newest vCenter that can coexist with it by asking StackExists per vCenter release, then
// requires the recommendation to be on that vCenter. Reordering the solver cannot make
// this pass by construction.
func TestRecommendationPrefersTheNewestPlatform(t *testing.T) {
	g := realGraph(t)
	vc, _ := model.ByKey("vcenter")

	eachPin(t, g, nil, func(t *testing.T, p model.Product, r *Release, s Stack) {
		if p.ID == vc.ID {
			return // the caller fixed it; nothing to prefer
		}
		var best *Release
		for _, cand := range g.ReleasesOf(vc.ID) {
			trial := map[int]*Release{p.ID: r, vc.ID: cand}
			if !g.StackExists(trial, StackOptions{}) {
				continue
			}
			if best == nil || version.Compare(cand.Version, best.Version) > 0 {
				best = cand
			}
		}
		if best == nil {
			return // no vCenter works with this pin at all
		}
		got := s.Releases[vc.ID]
		if got == nil {
			t.Errorf("pin %s %s: recommended stack has no vCenter", p.Label, r.Raw)
			return
		}
		if version.Compare(got.Version, best.Version) < 0 {
			t.Errorf("pin %s %s: recommended vCenter %s, but %s also carries this pin and is newer",
				p.Label, r.Raw, got.Raw, best.Raw)
		}
	})
}

// TestConstraintSetMatchesTheDocumentedOne pins the constraint set itself.
//
// Everything above validates stacks against the rules; this validates the rules against
// what the docs promise. Widening or narrowing the set is a real decision — it changes
// which stacks the tool will recommend — so it should fail here and be reviewed rather
// than pass silently.
func TestConstraintSetMatchesTheDocumentedOne(t *testing.T) {
	unconstrained := map[string]bool{
		"vcenter×vks":    true,
		"vcenter×vkr":    true,
		"supervisor×vkr": true,
		"esx×nsx":        true,
		"esx×avi":        true,
	}

	for _, e := range model.Edges {
		from, _ := model.ByKey(e.From)
		to, _ := model.ByKey(e.To)
		name := e.From + "×" + e.To
		got := model.Constrains(from.ID, to.ID)
		if want := !unconstrained[name]; got != want {
			t.Errorf("%s: Constrains=%v, documented as %v — update docs/model.md and this list together",
				name, got, want)
		}
	}
	if len(model.Edges) != 15 {
		t.Errorf("edge count is %d, was 15 — a new relationship needs a constraint decision",
			len(model.Edges))
	}
}
