// Package graph holds the whole cache in memory and answers every compatibility
// question from it. The dataset is small — a few hundred releases and ~70k edges — so
// loading it whole beats issuing SQL per query, and it keeps the version floor as a
// load-time concern rather than a schema concern.
package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/version"
)

// SupportPhase is where a release sits in its support lifecycle.
//
// Two independent sources answer this. The interop matrix carries two flags on every
// release, and the Broadcom Product Lifecycle portal publishes dates. Where both speak,
// the dates win: the flags lag badly, still calling VKr 1.32 generally supported months
// after its published end of general support, and every vCenter 7.0U3 patch likewise.
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

// PhaseSource names which of the two sources decided a release's phase.
type PhaseSource string

const (
	// SourceMatrix: the interop matrix flags, because the portal publishes nothing for
	// this release.
	SourceMatrix PhaseSource = "matrix"
	// SourceLifecycle: a published date that has passed.
	SourceLifecycle PhaseSource = "lifecycle"
)

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

	// EOGSDate is the published end of general support, and EOTGDate the published end
	// of technical guidance, both epoch ms and both zero when the portal says nothing.
	// EOTGDate is rarely published: Broadcom lists it for a couple of hundred releases
	// across the whole portal and for none of the products here.
	EOGSDate int64
	EOTGDate int64
	// LifecycleKey is the portal release string these dates were read from, kept so a
	// coarse join ("1.32" answering for 1.32.10) can be shown rather than implied.
	LifecycleKey string
	// noTechnicalGuidance mirrors the product's own flag, so PhaseAt can decide what an
	// absent end-of-technical-guidance date means for this release.
	noTechnicalGuidance bool

	// phase and phaseSource are PhaseAt and PhaseSourceAt evaluated once at load, so
	// every view shares one answer and one clock. A long-running serve re-reads the
	// cache on refresh, which re-evaluates them.
	phase       SupportPhase
	phaseSource PhaseSource
}

// Phase returns where this release sits according to the interop matrix alone.
//
// Kept flag-only and date-free on purpose: it is the raw upstream answer, and several
// callers want exactly that. Anything user-facing should ask PhaseAt.
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

// PhaseAt returns the support phase as of now, preferring published dates over the
// matrix flags wherever the two disagree.
//
// A release never moves back up: a matrix flag saying End of Support is believed even
// when no date has passed yet, because the flags do eventually retire a release and
// nothing is gained by arguing with them in that direction.
func (r Release) PhaseAt(now time.Time) SupportPhase {
	flags := r.Phase()
	ms := now.UnixMilli()

	switch {
	case r.EOTGDate > 0 && ms >= r.EOTGDate:
		return PhaseEndOfSupport
	case r.EOGSDate > 0 && ms >= r.EOGSDate:
		// With no published end of technical guidance, what passing general support
		// means depends on whether the product has a technical guidance period at all.
		// VKr and VKS do not, so they are done; vSphere's runs years past this date.
		if r.noTechnicalGuidance {
			return PhaseEndOfSupport
		}
		if flags == PhaseEndOfSupport {
			return PhaseEndOfSupport
		}
		return PhaseTechnicalGuidance
	}
	return flags
}

// PhaseSourceAt reports which source decided PhaseAt's answer.
func (r Release) PhaseSourceAt(now time.Time) PhaseSource {
	if r.PhaseAt(now) != r.Phase() {
		return SourceLifecycle
	}
	return SourceMatrix
}

// EffectivePhase is the phase every view should show: the published dates where they
// exist, the matrix flags otherwise. Evaluated once when the graph was loaded.
//
// A Release assembled outside Load has no stamped phase, so it falls back to the flags
// rather than reporting the zero value as a phase in its own right.
func (r Release) EffectivePhase() SupportPhase {
	if r.phase == "" {
		return r.Phase()
	}
	return r.phase
}

// EffectivePhaseSource names which source EffectivePhase came from.
func (r Release) EffectivePhaseSource() PhaseSource {
	if r.phaseSource == "" {
		return SourceMatrix
	}
	return r.phaseSource
}

// MarshalJSON emits the resolved phase alongside the raw fields, so a JSON or MCP
// consumer is never left to re-derive it from the flags — which is the very thing that
// gives the wrong answer for a release whose published date has passed.
func (r Release) MarshalJSON() ([]byte, error) {
	type release Release // shed the method, keep the fields
	return json.Marshal(struct {
		release
		Phase       SupportPhase `json:"phase"`
		PhaseLabel  string       `json:"phaseLabel"`
		PhaseSource PhaseSource  `json:"phaseSource"`
	}{
		release:     release(r),
		Phase:       r.EffectivePhase(),
		PhaseLabel:  r.EffectivePhase().Label(),
		PhaseSource: r.EffectivePhaseSource(),
	})
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
	// Now is the clock the support phase is evaluated against. Zero means time.Now().
	// Tests set it so a published date passing does not change their answers.
	Now time.Time
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

	lifecycle := lifecycleIndexes(snap.Lifecycle)
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
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
		rel := &Release{
			ID:                  r.ID,
			ProductID:           r.ProductID,
			ProductKey:          p.Key,
			Raw:                 r.HybridVersion,
			Version:             v,
			ReleaseType:         r.ReleaseType,
			GADate:              r.GADate,
			TechGuided:          r.TechGuided,
			GenGuided:           r.GenGuided,
			noTechnicalGuidance: p.Lifecycle.NoTechnicalGuidance,
		}
		if l, ok := lifecycle[p.Key].lookup(r.HybridVersion, v, p.Lifecycle.Match); ok {
			rel.EOGSDate, rel.EOTGDate, rel.LifecycleKey = l.EOGSDate, l.EOTGDate, l.ReleaseKey
		}
		rel.phase = rel.PhaseAt(now)
		rel.phaseSource = rel.PhaseSourceAt(now)
		g.Releases[r.ID] = rel
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
// Distinct from "nothing is compatible": eleven of the twenty-eight pairs in scope have no
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
