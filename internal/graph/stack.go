package graph

import (
	"maps"
	"sort"

	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/version"
)

// Stack is a complete, valid assignment of one release per product.
type Stack struct {
	// Releases keyed by product id.
	Releases map[int]*Release
	// Pinned marks which products the caller fixed rather than the solver choosing.
	Pinned map[int]bool
	// Verdicts is the per-pair provenance, so callers can show which pairs are
	// verified and which had to be inferred.
	Verdicts []PairVerdict
}

// Inferred returns the pairs in this stack that upstream does not publish. The stack is
// still valid on every published pair; these are the ones taken on trust.
func (s Stack) Inferred() []PairVerdict {
	var out []PairVerdict
	for _, v := range s.Verdicts {
		if v.Unverified() {
			out = append(out, v)
		}
	}
	return out
}

// StackOptions control the solver.
type StackOptions struct {
	// Limit caps the number of stacks returned. Zero means one.
	Limit int
	// IncludePatches allows patch releases to be chosen. Off by default: a stack made
	// of the latest patch of each line is what people actually want to see.
	IncludePatches bool
}

// StackFailure explains why no valid stack exists for the given pins.
type StackFailure struct {
	// BlockedProduct is the product the solver could not fill, or — when PinConflict
	// is set — the second half of the conflicting pin pair.
	BlockedProduct model.Product
	// Against is the already-assigned product that ruled out every candidate.
	Against model.Product
	// PinConflict marks the case where two pinned versions are themselves
	// incompatible, as opposed to the search running out of candidates.
	PinConflict bool
	// Status is the offending compatibility status, set only for a pin conflict.
	Status int
}

// Stacks solves for complete valid stacks given zero or more pinned releases.
//
// This is the headline query: pin one version and get back the whole stack, rather than
// doing five pairwise lookups by hand.
//
// Products are filled in most-constrained-first order (the Kubernetes chain before the
// base layer), and a partial assignment is abandoned as soon as any *published* pair
// against an already-assigned product is not compatible. Unpublished pairs — three of
// the ten — cannot constrain anything, so they are skipped during the search and
// reported afterwards as inferred.
func (g *Graph) Stacks(pins map[int]*Release, opts StackOptions) ([]Stack, *StackFailure) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 1
	}

	// Validate the pins against each other first. The search only ever checks a newly
	// assigned release against what is already assigned, so without this an
	// incompatible pair of pins would sail through and produce a "valid" stack.
	if v, ok := g.firstBadPin(pins); !ok {
		return nil, &StackFailure{
			BlockedProduct: v.B, Against: v.A, PinConflict: true, Status: v.Status,
		}
	}

	order := solveOrder(pins)
	assigned := make(map[int]*Release, len(model.Products))
	maps.Copy(assigned, pins)

	var results []Stack
	var failure *StackFailure

	var recurse func(depth int) bool
	recurse = func(depth int) bool {
		if depth == len(order) {
			results = append(results, g.snapshotStack(assigned, pins))
			return len(results) >= limit
		}
		productID := order[depth]
		p, _ := model.ByID(productID)

		candidates := g.candidatesFor(productID, assigned, opts)
		if len(candidates) == 0 && failure == nil {
			failure = &StackFailure{BlockedProduct: p, Against: g.blockingProduct(productID, assigned)}
		}
		for _, c := range candidates {
			assigned[productID] = c
			if recurse(depth + 1) {
				delete(assigned, productID)
				return true
			}
			delete(assigned, productID)
		}
		return false
	}
	recurse(0)

	if len(results) > 0 {
		return results, nil
	}
	return nil, failure
}

// firstBadPin reports the first pinned pair that is published and not compatible.
// Unverified pairs pass: three of the ten have no upstream data by design.
func (g *Graph) firstBadPin(pins map[int]*Release) (PairVerdict, bool) {
	for _, v := range g.Check(pins).Pairs {
		if !v.Unverified() && !Compatible(v.Status) {
			return v, false
		}
	}
	return PairVerdict{}, true
}

// candidatesFor returns the releases of a product that are still viable given what is
// already assigned, newest first so the solver reports the newest valid stack first.
func (g *Graph) candidatesFor(productID int, assigned map[int]*Release, opts StackOptions) []*Release {
	var out []*Release
	for _, r := range g.ReleasesOf(productID) {
		if r.IsPatch() && !opts.IncludePatches {
			continue
		}
		if g.consistent(r, assigned) {
			out = append(out, r)
		}
	}
	// ReleasesOf is ascending; reverse so newest is tried first.
	sort.Slice(out, func(i, j int) bool {
		return version.Compare(out[i].Version, out[j].Version) > 0
	})
	return out
}

// consistent reports whether adding r keeps every published pair compatible.
func (g *Graph) consistent(r *Release, assigned map[int]*Release) bool {
	for otherID, other := range assigned {
		if otherID == r.ProductID {
			continue
		}
		if !g.Published(r.ProductID, otherID) {
			continue // nothing to check against; reported later as inferred
		}
		status, ok := g.Status(r.ID, other.ID)
		if !ok || !Compatible(status) {
			return false
		}
	}
	return true
}

// blockingProduct names an assigned product that rules out every candidate, for the
// failure message. Best-effort: it reports the first assigned product that rejects
// everything.
func (g *Graph) blockingProduct(productID int, assigned map[int]*Release) model.Product {
	for otherID, other := range assigned {
		if !g.Published(productID, otherID) {
			continue
		}
		anyOK := false
		for _, r := range g.ReleasesOf(productID) {
			if status, ok := g.Status(r.ID, other.ID); ok && Compatible(status) {
				anyOK = true
				break
			}
		}
		if !anyOK {
			p, _ := model.ByID(otherID)
			return p
		}
	}
	return model.Product{}
}

func (g *Graph) snapshotStack(assigned, pins map[int]*Release) Stack {
	s := Stack{
		Releases: make(map[int]*Release, len(assigned)),
		Pinned:   make(map[int]bool, len(pins)),
	}
	maps.Copy(s.Releases, assigned)
	for id := range pins {
		s.Pinned[id] = true
	}
	s.Verdicts = g.Check(s.Releases).Pairs
	return s
}

// ViableOptions returns, for each product, every release that appears in at least one
// complete valid stack given the pins.
//
// This is what actually answers "what can I run?". Enumerating stacks instead tends to
// return near-duplicates that differ only in the last product the solver happened to
// assign, which looks like five answers but is really one.
//
// Each candidate is tested by asking for a single stack with that release added to the
// pins. That is one cheap solve per candidate — a few hundred in total.
func (g *Graph) ViableOptions(pins map[int]*Release, opts StackOptions) map[int][]*Release {
	out := make(map[int][]*Release, len(model.Products))
	probe := StackOptions{Limit: 1, IncludePatches: opts.IncludePatches}

	for _, p := range model.Products {
		if pinned, ok := pins[p.ID]; ok {
			out[p.ID] = []*Release{pinned}
			continue
		}
		trial := make(map[int]*Release, len(pins)+1)
		maps.Copy(trial, pins)

		var viable []*Release
		for _, r := range g.ReleasesOf(p.ID) {
			if r.IsPatch() && !opts.IncludePatches {
				continue
			}
			trial[p.ID] = r
			if stacks, _ := g.Stacks(trial, probe); len(stacks) > 0 {
				viable = append(viable, r)
			}
		}
		delete(trial, p.ID)

		// Newest first, matching how the rest of the output reads.
		sort.Slice(viable, func(i, j int) bool {
			return version.Compare(viable[i].Version, viable[j].Version) > 0
		})
		out[p.ID] = viable
	}
	return out
}

// solveOrder returns unpinned product ids most-constrained first.
//
// The Kubernetes chain is filled before the base layer: Supervisor, VKS and VKr have far
// fewer viable options per vCenter than the reverse, so choosing them first prunes the
// search hardest.
func solveOrder(pins map[int]*Release) []int {
	priority := map[string]int{
		"supervisor": 0,
		"vks":        1,
		"vkr":        2,
		"vcenter":    3,
		"esx":        4,
	}
	var out []int
	for _, p := range model.Products {
		if _, pinned := pins[p.ID]; !pinned {
			out = append(out, p.ID)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := model.ByID(out[i])
		b, _ := model.ByID(out[j])
		return priority[a.Key] < priority[b.Key]
	})
	return out
}
