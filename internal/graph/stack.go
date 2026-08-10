package graph

import (
	"maps"
	"slices"
	"sort"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/version"
)

// Stack is a complete, valid assignment of one release per product in the solve.
//
// Optional products (NSX, Avi) are absent unless the caller pinned them or asked for
// them by key. A stack without an NSX release is a complete answer about the five
// components every deployment has — not a partial one, and not a claim that the
// deployment has no NSX.
type Stack struct {
	// Releases keyed by product id.
	Releases map[int]*Release
	// Pinned marks which products the caller fixed rather than the solver choosing.
	Pinned map[int]bool
	// Verdicts is the per-pair provenance, so callers can show which pairs are
	// verified and which had to be inferred.
	Verdicts []PairVerdict
}

// Inferred returns dependency pairs in this stack that upstream does not publish — the
// ones taken on trust.
//
// With the constraint set limited to real dependencies this is normally empty: all seven
// gaps in the matrix fall on pairs that are not dependencies. Three are the long-standing
// ones (ESX against VKS and VKr, Supervisor against VKr); the other four are NSX and Avi
// against VKS and VKr, and neither optional component is a dependency of the guest
// cluster layer. So nothing has to be inferred to validate a stack.
func (s Stack) Inferred() []PairVerdict {
	var out []PairVerdict
	for _, v := range s.Verdicts {
		if v.Dependency && v.Unverified() {
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
	// Include opts optional products into the solve, by product key.
	//
	// Optional products (NSX, Avi) are left out unless they are pinned or named here.
	// Membership is per key and nothing is implied from one key to another: Include
	// ["avi"] solves a stack with Avi and no NSX, which is an ordinary deployment.
	Include []string
}

// wants reports whether an optional product should take part in the solve.
func (o StackOptions) wants(key string) bool { return slices.Contains(o.Include, key) }

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
	// Unlisted marks a pin conflict where upstream publishes the pair but never listed
	// these two releases together, as opposed to listing them as incompatible.
	Unlisted bool
	// Status is the offending compatibility status, set only for a pin conflict.
	Status int
}

// Stacks solves for complete valid stacks given zero or more pinned releases.
//
// This is the headline query: pin one version and get back the whole stack, rather than
// doing a fistful of pairwise lookups by hand.
//
// Products are filled in most-constrained-first order (the Kubernetes chain before the
// base layer), and a partial assignment is abandoned as soon as any *published* pair
// against an already-assigned product is not compatible. Unpublished pairs — seven of
// the twenty-one — cannot constrain anything, so they are skipped during the search and
// reported afterwards as inferred.
//
// Optional products take part only when pinned or named in opts.Include, and each is
// decided separately: opting into Avi does not bring NSX along, and an Avi-only stack
// never consults the NSX × Avi pair.
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
			BlockedProduct: v.B, Against: v.A, PinConflict: true,
			Unlisted: !v.HasEdge, Status: v.Status,
		}
	}

	order := solveOrder(pins, opts)
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

// firstBadPin reports the first pinned dependency pair that cannot hold.
//
// Pins are held to the same standard as releases the solver picks itself: inside a
// published pair, both an incompatible result and a missing one are a no. Anything looser
// let two pinned releases the matrix never lists together produce a "valid" stack — a
// combination the solver would refuse to assemble on its own. Pairs upstream publishes no
// data for at all still pass; nothing can be enforced there.
func (g *Graph) firstBadPin(pins map[int]*Release) (PairVerdict, bool) {
	if bad := g.Check(pins).Blocking(); len(bad) > 0 {
		return bad[0], false
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
		// Only real dependencies constrain a stack. vCenter against VKS is published
		// but is not a dependency, and enforcing it rules out combinations that work.
		if !model.IsDependency(r.ProductID, otherID) || !g.Published(r.ProductID, otherID) {
			continue
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
		if !model.IsDependency(productID, otherID) || !g.Published(productID, otherID) {
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
	// Include has to survive into the probe: a caller solving with --with nsx is asking
	// what else can join *that* stack, not the one without NSX.
	probe := StackOptions{Limit: 1, IncludePatches: opts.IncludePatches, Include: opts.Include}

	// A pin that no complete stack can contain has no viable options anywhere, including
	// itself. Returning the pin regardless made a dead end look like a one-node answer:
	// the map lit the selection, and callers could not tell it apart from a real result.
	if len(pins) > 0 {
		if stacks, _ := g.Stacks(pins, probe); len(stacks) == 0 {
			return out
		}
	}

	for _, p := range model.Products {
		if pinned, ok := pins[p.ID]; ok {
			out[p.ID] = []*Release{pinned}
			continue
		}
		// An optional product the caller did not ask for has no options, because it has
		// no place in this stack. Listing them anyway reported alternatives for a product
		// the same answer declared omitted — and, worse, the list was silently filtered
		// through the *other* optional product's constraints, so `--with avi` and
		// `--with nsx` returned different NSX alternatives for a stack containing neither.
		if p.Optional && !opts.wants(p.Key) {
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
// search hardest. NSX and Avi sit between the two: more constrained than vCenter, less
// than the Supervisor.
//
// Optional products only appear here when the caller pinned them or asked for them by
// key. Each is decided on its own — no optional product implies another — so opting into
// Avi alone produces an order with Avi in it and no NSX.
func solveOrder(pins map[int]*Release, opts StackOptions) []int {
	priority := map[string]int{
		"supervisor": 0,
		"vks":        1,
		"vkr":        2,
		"nsx":        3,
		"avi":        4,
		"vcenter":    5,
		"esx":        6,
	}
	var out []int
	for _, p := range model.Products {
		if _, pinned := pins[p.ID]; pinned {
			continue
		}
		if p.Optional && !opts.wants(p.Key) {
			continue
		}
		out = append(out, p.ID)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := model.ByID(out[i])
		b, _ := model.ByID(out[j])
		return priority[a.Key] < priority[b.Key]
	})
	return out
}
