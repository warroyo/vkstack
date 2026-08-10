package graph

import (
	"testing"

	"github.com/warroyo/vkstack/internal/model"
)

// The generation filter's one claim is that it constrains vCenter and nothing else.
//
// That claim is only interesting against real data, because what makes it necessary is a
// fact about upstream's matrix: components cross the generation line constantly. NSX 4.x
// has more compatible vCenter 9 pairs than NSX 9.x does, and ESX 8.x pairs with vCenter 9
// in the hundreds. A synthetic graph would prove the code does what it says while saying
// nothing about whether what it says is the right rule, so these run on the cache.

// TestGenerationFiltersOnlyVCenter checks that each generation keeps exactly its own
// vCenter releases, and that the products which genuinely span both survive either way.
func TestGenerationFiltersOnlyVCenter(t *testing.T) {
	for _, gen := range model.Generations {
		t.Run(versionLabel(gen), func(t *testing.T) {
			g := realGraphOpts(t, Options{Generation: gen})

			vcenter, _ := model.ByKey("vcenter")
			rels := g.ReleasesOf(vcenter.ID)
			if len(rels) == 0 {
				t.Fatalf("generation %d left no vCenter releases at all", gen)
			}
			for _, r := range rels {
				if got := r.Version.Major(); got != gen {
					t.Errorf("vCenter %s survived generation %d with major %d", r.Raw, gen, got)
				}
			}

			// Every other product must still have something. A generation that empties a
			// layer is a filter that has over-reached past vCenter.
			for _, p := range model.Products {
				if p.ID == vcenter.ID {
					continue
				}
				if len(g.ReleasesOf(p.ID)) == 0 {
					t.Errorf("generation %d left no %s releases", gen, p.Label)
				}
			}
		})
	}
}

// TestGenerationKeepsCrossLineComponents is the specific regression this filter exists to
// avoid: a reader who picks vSphere 9 must still be offered the older components that
// upstream publishes as compatible with it.
func TestGenerationKeepsCrossLineComponents(t *testing.T) {
	g := realGraphOpts(t, Options{Generation: 9})

	// ESX 8.x with vCenter 9.x is 154 published pairs in the cache this was written
	// against — by far the most common way to run a vCenter 9 estate today.
	esx, _ := model.ByKey("esx")
	if !hasMajor(g, esx.ID, 8) {
		t.Error("generation 9 dropped every ESX 8.x release; ESX 8 pairs with vCenter 9")
	}

	// NSX 4.x with vCenter 9.x is 185 pairs, more than NSX 9.x manages.
	nsx, _ := model.ByKey("nsx")
	if !hasMajor(g, nsx.ID, 4) {
		t.Error("generation 9 dropped every NSX 4.x release; NSX 4 pairs with vCenter 9")
	}
}

// TestGenerationDropsWhatCannotReachIt is the other half: under vSphere 9, a component
// whose only published vCenter pairs are 8.x has to go, or the map offers a path that
// cannot be built.
func TestGenerationDropsWhatCannotReachIt(t *testing.T) {
	all := realGraphOpts(t, Options{})
	nine := realGraphOpts(t, Options{Generation: 9})

	if len(nine.Releases) >= len(all.Releases) {
		t.Fatalf("generation 9 kept %d of %d releases; expected a strict narrowing",
			len(nine.Releases), len(all.Releases))
	}

	vcenter, _ := model.ByKey("vcenter")
	for id, r := range nine.Releases {
		if r.ProductID == vcenter.ID {
			continue
		}
		// Anything that survived and is judged against vCenter must have a live pair.
		if !nine.Coverage[pair(r.ProductID, vcenter.ID)] {
			continue
		}
		reachable := false
		for _, e := range nine.Compat[id] {
			peer := nine.Releases[e.Peer]
			if peer != nil && peer.ProductID == vcenter.ID && Compatible(e.Status) {
				reachable = true
				break
			}
		}
		if !reachable {
			t.Errorf("%s %s survived generation 9 with no compatible vCenter 9 release",
				r.ProductKey, r.Raw)
		}
	}
}

// TestGenerationsCoverCache fails when the cache holds a vCenter major the UI offers no
// tab for. A new platform generation should surface as a red build, not as releases that
// quietly cannot be reached from any filter.
func TestGenerationsCoverCache(t *testing.T) {
	g := realGraph(t)
	vcenter, _ := model.ByKey("vcenter")
	for _, r := range g.ReleasesOf(vcenter.ID) {
		if !model.KnownGeneration(r.Version.Major()) {
			t.Errorf("vCenter %s is major %d, which model.Generations does not list — add it",
				r.Raw, r.Version.Major())
		}
	}
}

func hasMajor(g *Graph, productID, major int) bool {
	for _, r := range g.ReleasesOf(productID) {
		if r.Version.Major() == major {
			return true
		}
	}
	return false
}

func versionLabel(gen int) string {
	switch gen {
	case 8:
		return "vSphere8"
	case 9:
		return "vSphere9"
	}
	return "generation"
}
