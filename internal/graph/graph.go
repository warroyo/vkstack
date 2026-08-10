// Package graph holds the whole cache in memory and answers every compatibility
// question from it. The dataset is small — a few hundred releases and ~70k edges — so
// loading it whole beats issuing SQL per query, and it keeps the version floor as a
// load-time concern rather than a schema concern.
package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/version"
)

// SupportPhase is where a release sits in its support lifecycle, as published by the
// interop matrix. The matrix carries this on every release as two flags; it is not a
// separate source and nothing here is inferred from dates.
type SupportPhase string

const (
	// PhaseGeneral: in General Support.
	PhaseGeneral SupportPhase = "general"
	// PhaseTechnicalGuidance: General Support has ended. Still listed, but only
	// Technical Guidance remains — no new fixes.
	PhaseTechnicalGuidance SupportPhase = "technical-guidance"
	// PhaseEndOfSupport: both General Support and Technical Guidance have ended.
	PhaseEndOfSupport SupportPhase = "end-of-support"
)

// Label renders a phase for display.
func (p SupportPhase) Label() string {
	switch p {
	case PhaseGeneral:
		return "General Support"
	case PhaseTechnicalGuidance:
		return "Technical Guidance"
	case PhaseEndOfSupport:
		return "End of Support"
	}
	return string(p)
}

// Supported reports whether a release is still in General Support.
func (p SupportPhase) Supported() bool { return p == PhaseGeneral }

// Release is a release with its parsed version.
type Release struct {
	ID          int
	ProductID   int
	ProductKey  string
	Raw         string // hybridVersion as published
	Version     version.Version
	ReleaseType string
	GADate      int64
	// TechGuided and GenGuided are the matrix's own support-phase flags.
	TechGuided bool
	GenGuided  bool
}

// Phase returns where this release sits in its support lifecycle.
func (r Release) Phase() SupportPhase {
	switch {
	case r.GenGuided:
		return PhaseGeneral
	case r.TechGuided:
		return PhaseTechnicalGuidance
	default:
		return PhaseEndOfSupport
	}
}

// IsPatch reports whether this is a patch release, which most views hide by default.
//
// This is upstream's own definition, not a heuristic over version strings. The matrix
// endpoint takes an isHidePatch flag, and setting it drops exactly the releases whose
// releaseType is "Patch" — nothing else. "Maint", "Maintenance", "Unknown" and untyped
// releases all survive it, so they are not patches here either, however patch-like their
// version numbers look. VKr 1.32.10 is a Maintenance release and is a first-class
// candidate; vCenter 8.0U3k is a Patch and is hidden until asked for.
//
// Matching upstream matters because compatibility is published per release, not per
// version line: VKr 1.32.7 and 1.32.10 sit in the same line and disagree about vCenter
// 8.0U3. Collapsing a line would invent an answer upstream never gave.
func (r Release) IsPatch() bool { return r.ReleaseType == "Patch" }

// Edge is one compatibility result from the perspective of a given release.
type Edge struct {
	Peer      int
	Status    int
	Footnotes string
}

// Compatible reports whether a status counts as a yes. Status 3 is "compatible, not
// tested" — still a yes. Status 4 ("not supported") is the dominant no in this dataset
// and must never be read as merely missing.
func Compatible(status int) bool {
	return status == store.StatusCompatible || status == store.StatusCompatibleNT
}

// Graph is the loaded, filtered dataset.
type Graph struct {
	Releases  map[int]*Release
	Compat    map[int][]Edge
	ByProduct map[int][]int // product id -> release ids, ascending by version
	Coverage  map[[2]int]bool
	FetchedAt int64

	// Excluded counts releases dropped by the version floor, for `cache info`.
	Excluded map[int]int
}

// Options control load-time filtering.
type Options struct {
	// AllVersions disables the supported-version floor entirely.
	AllVersions bool
	// MinVersions overrides a product's floor, keyed by product short key.
	MinVersions map[string]string
	// Generation restricts the answer to one vSphere platform generation, named by the
	// vCenter major version. Zero means every generation.
	//
	// Only vCenter is filtered by its own version; see model.Generations for why. Every
	// other product is kept or dropped by whether it can still reach a surviving vCenter.
	Generation int
}

// Load builds the in-memory graph from a cache snapshot, applying the version floor.
//
// Filtering happens here rather than at ingest so the cache stays a dumb mirror of
// upstream and moving the floor never requires a refetch.
func Load(snap *store.Snapshot, opts Options) (*Graph, error) {
	g := &Graph{
		Releases:  make(map[int]*Release, len(snap.Releases)),
		Compat:    make(map[int][]Edge),
		ByProduct: make(map[int][]int),
		Coverage:  make(map[[2]int]bool),
		Excluded:  make(map[int]int),
		FetchedAt: snap.FetchedAt,
	}

	for _, pc := range snap.Coverage {
		g.Coverage[pair(pc.AProduct, pc.BProduct)] = pc.EdgeCount > 0
	}

	floors, err := resolveFloors(opts)
	if err != nil {
		return nil, err
	}

	// Pass 1: parse every in-scope release and apply the explicit floors.
	for i := range snap.Releases {
		r := snap.Releases[i]
		p, ok := model.ByID(r.ProductID)
		if !ok {
			continue // a product outside our scope
		}
		v := version.Parse(r.HybridVersion, p.Scheme)
		if floor, has := floors[p.Key]; has && version.Compare(v, floor) < 0 {
			g.Excluded[r.ProductID]++
			continue
		}
		if opts.Generation > 0 && p.Key == "vcenter" && v.Major() != opts.Generation {
			g.Excluded[r.ProductID]++
			continue
		}
		g.Releases[r.ID] = &Release{
			ID:          r.ID,
			ProductID:   r.ProductID,
			ProductKey:  p.Key,
			Raw:         r.HybridVersion,
			Version:     v,
			ReleaseType: r.ReleaseType,
			GADate:      r.GADate,
			TechGuided:  r.TechGuided,
			GenGuided:   r.GenGuided,
		}
	}

	// Pass 2: keep edges where both endpoints survived, in both directions.
	for _, c := range snap.Compat {
		a, aOK := g.Releases[c.ARelease]
		b, bOK := g.Releases[c.BRelease]
		if !aOK || !bOK {
			continue
		}
		g.Compat[a.ID] = append(g.Compat[a.ID], Edge{Peer: b.ID, Status: c.Status, Footnotes: c.Footnotes})
		g.Compat[b.ID] = append(g.Compat[b.ID], Edge{Peer: a.ID, Status: c.Status, Footnotes: c.Footnotes})
	}

	// Pass 3: drop products without an explicit floor that cannot reach any surviving
	// ESX or vCenter release. Anything that only ever worked with vSphere 7 falls out
	// on its own, without inventing a cutoff we have no basis for.
	g.pruneUnreachable(floors)

	// Pass 4: narrow everything else to what the chosen generation can still reach.
	if opts.Generation > 0 {
		g.pruneToGeneration()
	}

	g.indexByProduct()
	return g, nil
}

// pruneUnreachable removes floor-less releases with no compatible edge to a base-layer
// (ESX or vCenter) release that survived the floor.
func (g *Graph) pruneUnreachable(floors map[string]version.Version) {
	if len(floors) == 0 {
		return // --all-versions: nothing to be reachable from
	}
	base := map[int]bool{}
	for _, p := range model.Products {
		if _, hasFloor := floors[p.Key]; hasFloor {
			base[p.ID] = true
		}
	}

	for id, r := range g.Releases {
		if base[r.ProductID] {
			continue
		}
		reachable := false
		for _, e := range g.Compat[id] {
			peer := g.Releases[e.Peer]
			if peer != nil && base[peer.ProductID] && Compatible(e.Status) {
				reachable = true
				break
			}
		}
		// VKS has no published ESX edges at all, so in practice its reachability comes
		// through vCenter. That is fine — vCenter is the hub.
		if !reachable {
			g.Excluded[r.ProductID]++
			delete(g.Releases, id)
		}
	}

	g.dropDanglingEdges()
}

// pruneToGeneration removes releases that cannot reach a surviving vCenter.
//
// This has to be its own pass rather than a wider base set for pruneUnreachable, because
// that one skips every floored product as a base it never questions. ESX carries a floor,
// so under a generation filter an ESX release that only ever paired with vCenter 8 would
// survive a pass that only asks about floor-less products. Here vCenter is the sole base
// and everything else, ESX included, has to earn its place.
//
// One hop is enough: vCenter is the hub, and every other product publishes vCenter pairs.
// A product that publishes none is left whole — an absent pair is not evidence of
// incompatibility, and the stack already reports answers resting on one as inferred.
func (g *Graph) pruneToGeneration() {
	vcenter, ok := model.ByKey("vcenter")
	if !ok {
		return
	}

	// Only judge a product upstream actually publishes against vCenter.
	judged := map[int]bool{}
	for _, p := range model.Products {
		if p.ID != vcenter.ID && g.Coverage[pair(p.ID, vcenter.ID)] {
			judged[p.ID] = true
		}
	}

	for id, r := range g.Releases {
		if !judged[r.ProductID] {
			continue
		}
		reachable := false
		for _, e := range g.Compat[id] {
			peer := g.Releases[e.Peer]
			if peer != nil && peer.ProductID == vcenter.ID && Compatible(e.Status) {
				reachable = true
				break
			}
		}
		if !reachable {
			g.Excluded[r.ProductID]++
			delete(g.Releases, id)
		}
	}

	g.dropDanglingEdges()
}

// dropDanglingEdges removes compatibility entries whose endpoints a prune has deleted.
func (g *Graph) dropDanglingEdges() {
	for id, edges := range g.Compat {
		if _, ok := g.Releases[id]; !ok {
			delete(g.Compat, id)
			continue
		}
		kept := edges[:0]
		for _, e := range edges {
			if _, ok := g.Releases[e.Peer]; ok {
				kept = append(kept, e)
			}
		}
		g.Compat[id] = kept
	}
}

func (g *Graph) indexByProduct() {
	for id, r := range g.Releases {
		g.ByProduct[r.ProductID] = append(g.ByProduct[r.ProductID], id)
	}
	for pid := range g.ByProduct {
		ids := g.ByProduct[pid]
		sort.Slice(ids, func(i, j int) bool {
			a, b := g.Releases[ids[i]], g.Releases[ids[j]]
			if c := version.Compare(a.Version, b.Version); c != 0 {
				return c < 0
			}
			return a.ID < b.ID
		})
	}
}

func resolveFloors(opts Options) (map[string]version.Version, error) {
	if opts.AllVersions {
		return nil, nil
	}
	floors := map[string]version.Version{}
	for _, p := range model.Products {
		if p.MinVersion != "" {
			floors[p.Key] = version.Parse(p.MinVersion, p.Scheme)
		}
	}
	for key, raw := range opts.MinVersions {
		p, ok := model.ByKey(key)
		if !ok {
			return nil, fmt.Errorf("unknown product %q in --min-version (want one of %s)", key, productKeyList())
		}
		floors[key] = version.Parse(raw, p.Scheme)
	}
	return floors, nil
}

func productKeyList() string {
	keys := make([]string, 0, len(model.Products))
	for _, p := range model.Products {
		keys = append(keys, p.Key)
	}
	return strings.Join(keys, ", ")
}

// Published reports whether upstream publishes compatibility data for a product pair.
// Distinct from "nothing is compatible": seven of the twenty-one pairs in scope have no
// data.
func (g *Graph) Published(aProductID, bProductID int) bool {
	return g.Coverage[pair(aProductID, bProductID)]
}

// ReleasesOf returns a product's releases, ascending by version.
func (g *Graph) ReleasesOf(productID int) []*Release {
	ids := g.ByProduct[productID]
	out := make([]*Release, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.Releases[id])
	}
	return out
}

// Status returns the compatibility status between two releases, and whether an edge
// exists at all.
func (g *Graph) Status(aRelease, bRelease int) (int, bool) {
	for _, e := range g.Compat[aRelease] {
		if e.Peer == bRelease {
			return e.Status, true
		}
	}
	return 0, false
}

func pair(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}
