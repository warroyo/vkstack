package graph

import (
	"sort"
	"testing"

	"github.com/warroyo/vkstack/internal/model"
)

// keysIn names the products a stack actually assigned, for readable failures.
func keysIn(s Stack) []string {
	var out []string
	for id := range s.Releases {
		p, _ := model.ByID(id)
		out = append(out, p.Key)
	}
	sort.Strings(out)
	return out
}

func has(s Stack, productID int) bool {
	_, ok := s.Releases[productID]
	return ok
}

// solveOne solves with the given pins and options, failing the test if nothing comes back.
func solveOne(t *testing.T, g *Graph, pins map[int]*Release, opts StackOptions) Stack {
	t.Helper()
	if opts.Limit == 0 {
		opts.Limit = 1
	}
	stacks, failure := g.Stacks(pins, opts)
	if len(stacks) == 0 {
		t.Fatalf("expected a stack for Include=%v, got failure %+v", opts.Include, failure)
	}
	return stacks[0]
}

// The four opt-in combinations are all real deployments, and each must produce exactly
// the optional products asked for — no more, no fewer.
func TestOptionalProductsAreIndependentlyOptIn(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]} // vCenter 9.0.0.0

	cases := []struct {
		name    string
		include []string
		wantNSX bool
		wantAvi bool
	}{
		{name: "neither", include: nil},
		{name: "nsx only", include: []string{"nsx"}, wantNSX: true},
		{name: "avi only", include: []string{"avi"}, wantAvi: true},
		{name: "both", include: []string{"nsx", "avi"}, wantNSX: true, wantAvi: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := solveOne(t, g, pins, StackOptions{Include: tc.include})

			if got := has(s, nsx); got != tc.wantNSX {
				t.Errorf("NSX present = %v, want %v (stack has %v)", got, tc.wantNSX, keysIn(s))
			}
			if got := has(s, avi); got != tc.wantAvi {
				t.Errorf("Avi present = %v, want %v (stack has %v)", got, tc.wantAvi, keysIn(s))
			}

			want := requiredCount()
			if tc.wantNSX {
				want++
			}
			if tc.wantAvi {
				want++
			}
			if len(s.Releases) != want {
				t.Errorf("stack has %d products (%v), want %d", len(s.Releases), keysIn(s), want)
			}
			if !g.Check(s.Releases).OK() {
				t.Errorf("stack does not pass Check: %+v", g.Check(s.Releases).Incompatible())
			}
		})
	}
}

// Avi on a distributed switch with no NSX anywhere is an ordinary deployment. An
// implementation that treats the NSX × Avi edge as a requirement rather than a
// constraint breaks exactly here.
func TestAviSolvesWithoutNSX(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]}

	s := solveOne(t, g, pins, StackOptions{Include: []string{"avi"}})

	if !has(s, avi) {
		t.Fatalf("expected an Avi release, stack has %v", keysIn(s))
	}
	if has(s, nsx) {
		t.Fatalf("Avi alone must not pull NSX in, stack has %v", keysIn(s))
	}
	// No verdict may mention NSX either: an absent product contributes no pair at all,
	// so nothing downstream can report a constraint that was never evaluated.
	for _, v := range s.Verdicts {
		if v.A.Key == "nsx" || v.B.Key == "nsx" {
			t.Errorf("Avi-only stack produced an NSX verdict: %s × %s", v.A.Key, v.B.Key)
		}
	}
}

// A pin is its own opt-in, and it opts in only the thing pinned.
func TestPinningAnOptionalProductDoesNotDragTheOtherIn(t *testing.T) {
	g := load(t, Options{})

	t.Run("nsx pin", func(t *testing.T) {
		s := solveOne(t, g, map[int]*Release{nsx: g.Releases[61]}, StackOptions{})
		if !has(s, nsx) {
			t.Errorf("pinned NSX is missing from the stack: %v", keysIn(s))
		}
		if has(s, avi) {
			t.Errorf("an NSX pin must not bring Avi in: %v", keysIn(s))
		}
	})

	t.Run("avi pin", func(t *testing.T) {
		s := solveOne(t, g, map[int]*Release{avi: g.Releases[71]}, StackOptions{})
		if !has(s, avi) {
			t.Errorf("pinned Avi is missing from the stack: %v", keysIn(s))
		}
		if has(s, nsx) {
			t.Errorf("an Avi pin must not bring NSX in: %v", keysIn(s))
		}
	})
}

// ESX × Avi is published and, in the fixture as upstream, says no to everything. It is
// not a dependency, so it must be reported and never enforced.
func TestESXAviIsInformationalNotBlocking(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]}

	s := solveOne(t, g, pins, StackOptions{Include: []string{"avi"}})

	if !g.Check(s.Releases).OK() {
		t.Fatalf("ESX × Avi must not invalidate a stack: %+v", g.Check(s.Releases).Incompatible())
	}

	found := false
	for _, v := range g.Check(s.Releases).Informational() {
		if (v.A.Key == "esx" && v.B.Key == "avi") || (v.A.Key == "avi" && v.B.Key == "esx") {
			found = true
			if v.Constrains {
				t.Error("ESX × Avi must not be a dependency")
			}
		}
	}
	if !found {
		t.Error("ESX × Avi should still be reported as informational")
	}
}

// NSX × Supervisor is a real dependency, so a mismatch there has to block.
func TestNSXSupervisorMismatchBlocks(t *testing.T) {
	g := load(t, Options{})

	// NSX 4.2.0.0 pairs with the old Supervisor; pinning it against the new one cannot
	// hold.
	pins := map[int]*Release{nsx: g.Releases[62], sup: g.Releases[31]}
	stacks, failure := g.Stacks(pins, StackOptions{Limit: 1})
	if len(stacks) > 0 {
		t.Fatalf("expected no stack, got %v", keysIn(stacks[0]))
	}
	if failure == nil || !failure.PinConflict {
		t.Fatalf("expected a pin conflict, got %+v", failure)
	}
}

// The NSX × Avi pair constrains only when both are chosen. Avi 31.2.1 fits the rest of
// the stack but no NSX, so it must solve alone and fail only once NSX joins.
func TestNSXAviPairOnlyAppliesWhenBothAreChosen(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{avi: g.Releases[73]} // Avi 31.2.1

	s := solveOne(t, g, pins, StackOptions{})
	if has(s, nsx) {
		t.Fatalf("expected an NSX-free stack, got %v", keysIn(s))
	}

	if stacks, failure := g.Stacks(pins, StackOptions{Limit: 1, Include: []string{"nsx"}}); len(stacks) > 0 {
		t.Errorf("Avi 31.2.1 fits no NSX release, so opting into NSX must fail: %v", keysIn(stacks[0]))
	} else if failure == nil {
		t.Error("expected a failure explaining why NSX could not be filled")
	}
}

// An optional product has options exactly when it is part of the solve. Reporting them
// for a product the same answer calls omitted invited the reader to pick from a list that
// was not on offer — and the list was quietly constrained by whichever *other* optional
// product happened to be included.
func TestViableOptionsFollowsInclude(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]}

	t.Run("absent when not asked for", func(t *testing.T) {
		viable := g.ViableOptions(pins, StackOptions{HidePatches: false})
		for _, id := range []int{nsx, avi} {
			p, _ := model.ByID(id)
			if got := len(viable[id]); got != 0 {
				t.Errorf("%s was not asked for but has %d options", p.Key, got)
			}
		}
	})

	// Each is independent: including one must not produce options for the other.
	for _, tc := range []struct{ include, want, notWant int }{
		{include: nsx, want: nsx, notWant: avi},
		{include: avi, want: avi, notWant: nsx},
	} {
		p, _ := model.ByID(tc.include)
		t.Run("included: "+p.Key, func(t *testing.T) {
			viable := g.ViableOptions(pins, StackOptions{HidePatches: false, Include: []string{p.Key}})
			if len(viable[tc.want]) == 0 {
				t.Errorf("%s was included but has no options", p.Key)
			}
			other, _ := model.ByID(tc.notWant)
			if got := len(viable[tc.notWant]); got != 0 {
				t.Errorf("%s was not included but has %d options", other.Key, got)
			}
		})
	}
}

// The alternatives listed for one optional product must not shift because the other was
// included. That coupling was invisible in the output: two runs of the same pin returned
// different NSX lists depending on an unrelated flag.
func TestViableOptionsForOneOptionalDoNotDependOnTheOther(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]}

	alone := g.ViableOptions(pins, StackOptions{HidePatches: false, Include: []string{"nsx"}})
	both := g.ViableOptions(pins, StackOptions{HidePatches: false, Include: []string{"nsx", "avi"}})

	// With both included the NSX list may legitimately narrow, because NSX × Avi is a
	// real dependency once Avi is in the stack. What must never happen is the reverse:
	// an NSX option that only exists when Avi is present.
	inAlone := map[int]bool{}
	for _, r := range alone[nsx] {
		inAlone[r.ID] = true
	}
	for _, r := range both[nsx] {
		if !inAlone[r.ID] {
			t.Errorf("NSX %s is viable only when Avi is included", r.Raw)
		}
	}
}

// Every product must be reachable by key from Include, and non-optional keys must have
// no effect — the solver already fills them.
func TestIncludeIsPerKeyAndIgnoresRequiredProducts(t *testing.T) {
	g := load(t, Options{})
	pins := map[int]*Release{vc: g.Releases[21]}

	s := solveOne(t, g, pins, StackOptions{Include: []string{"vcenter", "supervisor"}})
	if len(s.Releases) != requiredCount() {
		t.Errorf("naming required products changed the stack: %v", keysIn(s))
	}
}

// StackExists is an optimisation, so the only thing that matters about it is that it
// never disagrees with the search it replaces. It explores in a different order — most
// constrained first, rather than the fixed order that makes Stacks return the *newest*
// stack — and a different order must not mean a different answer.
func TestStackExistsAgreesWithStacks(t *testing.T) {
	g := load(t, Options{})

	products := []int{vc, esx, sup, vks, vkr, nsx, avi}
	includes := [][]string{nil, {"nsx"}, {"avi"}, {"nsx", "avi"}}

	checked, disagreed := 0, 0
	for _, inc := range includes {
		for _, hidePatches := range []bool{false, true} {
			opts := StackOptions{Limit: 1, HidePatches: hidePatches, Include: inc}

			// No pins at all, then every single-product pin the caller could send.
			pinSets := []map[int]*Release{{}}
			for _, pid := range products {
				for _, r := range g.ReleasesOf(pid) {
					pinSets = append(pinSets, map[int]*Release{pid: r})
				}
			}
			// And every pair of pins across two products, which is where an ordering
			// bug would actually show up.
			for _, a := range g.ReleasesOf(vc) {
				for _, b := range g.ReleasesOf(sup) {
					pinSets = append(pinSets, map[int]*Release{vc: a, sup: b})
				}
				for _, b := range g.ReleasesOf(avi) {
					pinSets = append(pinSets, map[int]*Release{vc: a, avi: b})
				}
			}

			for _, pins := range pinSets {
				stacks, _ := g.Stacks(pins, opts)
				want := len(stacks) > 0
				got := g.StackExists(pins, opts)
				checked++
				if got != want {
					disagreed++
					if disagreed <= 3 {
						var names []string
						for id, r := range pins {
							p, _ := model.ByID(id)
							names = append(names, p.Key+" "+r.Raw)
						}
						sort.Strings(names)
						t.Errorf("StackExists=%v but Stacks found %d, for include=%v hidePatches=%v pins=%v",
							got, len(stacks), inc, hidePatches, names)
					}
				}
			}
		}
	}
	t.Logf("checked %d pin/option combinations, %d disagreements", checked, disagreed)
}
