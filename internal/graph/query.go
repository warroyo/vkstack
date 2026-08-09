package graph

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/version"
)

// AmbiguousError reports a version string that matched more than one release.
type AmbiguousError struct {
	Product    string
	Input      string
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches %d %s releases: %s — be more specific",
		e.Input, len(e.Candidates), e.Product, strings.Join(e.Candidates, ", "))
}

// Resolve finds the release of a product matching a user-supplied version string.
// Tries exact match first, then unique prefix. Ambiguity is an error rather than a guess.
func (g *Graph) Resolve(productKey, input string) (*Release, error) {
	p, ok := model.ByKey(productKey)
	if !ok {
		return nil, fmt.Errorf("unknown product %q (want one of %s)", productKey, productKeyList())
	}
	releases := g.ReleasesOf(p.ID)
	if len(releases) == 0 {
		return nil, fmt.Errorf("no %s releases in the cache — run `interop refresh`", p.Label)
	}

	input = strings.TrimSpace(input)
	for _, r := range releases {
		if strings.EqualFold(r.Raw, input) {
			return r, nil
		}
	}

	var matches []*Release
	for _, r := range releases {
		if strings.HasPrefix(strings.ToLower(r.Raw), strings.ToLower(input)) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no %s release matches %q (newest available: %s)",
			p.Label, input, releases[len(releases)-1].Raw)
	default:
		// Prefer the newest when every match shares a version and differs only by id,
		// otherwise make the caller disambiguate.
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, m.Raw)
		}
		return nil, &AmbiguousError{Product: p.Label, Input: input, Candidates: names}
	}
}

// CompatGroup is everything one product offers against a given release.
type CompatGroup struct {
	Product model.Product
	// Published is false when upstream has no data for this product pair at all.
	// Rendered as "no data", never as an empty compatible list.
	Published bool
	Releases  []CompatHit
}

// CompatHit is one compatible peer release.
type CompatHit struct {
	Release   *Release
	Status    int
	Footnotes string
}

// CompatOptions filter a compatibility lookup.
type CompatOptions struct {
	// IncludePatches keeps Patch-type releases, which are hidden by default.
	IncludePatches bool
	// Statuses to include; defaults to compatible and compatible-not-tested.
	Statuses []int
}

func (o CompatOptions) allows(status int) bool {
	if len(o.Statuses) == 0 {
		return Compatible(status)
	}
	return slices.Contains(o.Statuses, status)
}

// CompatibleWith returns everything compatible with a release, grouped by product and
// sorted newest-first. Products with no published pair are still returned, marked
// unpublished, so the gap is visible rather than looking like "nothing works".
func (g *Graph) CompatibleWith(r *Release, opts CompatOptions) []CompatGroup {
	hits := map[int][]CompatHit{}
	for _, e := range g.Compat[r.ID] {
		peer := g.Releases[e.Peer]
		if peer == nil || !opts.allows(e.Status) {
			continue
		}
		if peer.IsPatch() && !opts.IncludePatches {
			continue
		}
		hits[peer.ProductID] = append(hits[peer.ProductID], CompatHit{
			Release: peer, Status: e.Status, Footnotes: e.Footnotes,
		})
	}

	var out []CompatGroup
	for _, p := range model.Products {
		if p.ID == r.ProductID {
			continue
		}
		group := CompatGroup{Product: p, Published: g.Published(r.ProductID, p.ID), Releases: hits[p.ID]}
		sort.Slice(group.Releases, func(i, j int) bool {
			return version.Compare(group.Releases[i].Release.Version, group.Releases[j].Release.Version) > 0
		})
		out = append(out, group)
	}
	return out
}

// PairVerdict is the compatibility outcome for one product pair in a stack.
type PairVerdict struct {
	// Dependency is false for pairs the matrix publishes that are not real
	// dependencies. Those are reported for reference and never enforced.
	Dependency bool
	A, B       model.Product
	ARelease   *Release
	BRelease   *Release
	Status     int
	// Published is false when upstream has no data for this product pair.
	Published bool
	// HasEdge is false when the pair is published but these two specific releases were
	// never evaluated against each other.
	HasEdge   bool
	Footnotes string
}

// OK reports whether this pair is a verified yes.
func (v PairVerdict) OK() bool { return v.Published && v.HasEdge && Compatible(v.Status) }

// Enforced reports whether this pair can invalidate a stack.
func (v PairVerdict) Enforced() bool { return v.Dependency }

// Unverified reports whether this pair could not be checked, as opposed to failing.
func (v PairVerdict) Unverified() bool { return !v.Published || !v.HasEdge }

// CheckResult is a whole-stack verdict.
type CheckResult struct {
	Pairs []PairVerdict
}

// Incompatible returns the dependency pairs that actively fail.
//
// Non-dependency pairs are excluded: vCenter against VKS is published but VKS does not
// run on vCenter, so a mismatch there says nothing about whether the stack works.
func (c CheckResult) Incompatible() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if p.Dependency && !p.Unverified() && !Compatible(p.Status) {
			out = append(out, p)
		}
	}
	return out
}

// Informational returns published pairs that are not dependencies, with their status.
// Worth showing, never worth failing on.
func (c CheckResult) Informational() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if !p.Dependency && !p.Unverified() {
			out = append(out, p)
		}
	}
	return out
}

// Unverified returns the pairs that could not be checked at all.
func (c CheckResult) Unverified() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if p.Dependency && p.Unverified() {
			out = append(out, p)
		}
	}
	return out
}

// OK reports whether nothing actively fails. Unverified pairs are a warning, not a
// failure — three of the ten pairs have no upstream data by design.
func (c CheckResult) OK() bool { return len(c.Incompatible()) == 0 }

// Check validates every pair of a pinned stack, keyed by product id.
func (g *Graph) Check(pins map[int]*Release) CheckResult {
	var res CheckResult
	for _, pr := range model.Pairs() {
		a, b := pr[0], pr[1]
		ra, okA := pins[a]
		rb, okB := pins[b]
		if !okA || !okB {
			continue
		}
		pa, _ := model.ByID(a)
		pb, _ := model.ByID(b)
		v := PairVerdict{
			A: pa, B: pb, ARelease: ra, BRelease: rb,
			Published:  g.Published(a, b),
			Dependency: model.IsDependency(a, b),
		}
		if v.Published {
			for _, e := range g.Compat[ra.ID] {
				if e.Peer == rb.ID {
					v.Status, v.Footnotes, v.HasEdge = e.Status, e.Footnotes, true
					break
				}
			}
		}
		res.Pairs = append(res.Pairs, v)
	}
	return res
}
