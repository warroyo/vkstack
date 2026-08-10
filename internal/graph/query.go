package graph

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/version"
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

// NotFoundError reports a version string that matched no release.
//
// Typed rather than formatted, because callers need to tell "you asked about something
// that does not exist" apart from every other failure — a person reads the message, a
// program reads the fields.
type NotFoundError struct {
	Product string
	Input   string
	// Newest is the newest release that does exist, as a starting point.
	Newest string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no %s release matches %q (newest available: %s)",
		e.Product, e.Input, e.Newest)
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
		return nil, fmt.Errorf("no %s releases in the cache — run `vkstack refresh`", p.Label)
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
		return nil, &NotFoundError{
			Product: p.Label, Input: input, Newest: releases[len(releases)-1].Raw,
		}
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
	// HiddenPatches counts compatible releases dropped by the patch filter. Without it,
	// a product whose only compatible releases are patches reads as "nothing
	// compatible", which is a different and false claim.
	HiddenPatches int
}

// CompatHit is one compatible peer release.
type CompatHit struct {
	Release   *Release
	Status    int
	Footnotes string
}

// CompatOptions filter a compatibility lookup.
type CompatOptions struct {
	// HidePatches drops Patch-type releases. Off by default; see StackOptions.HidePatches
	// for why this is the opposite of the interop site's own default.
	HidePatches bool
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
	hiddenPatches := map[int]int{}
	for _, e := range g.Compat[r.ID] {
		peer := g.Releases[e.Peer]
		if peer == nil || !opts.allows(e.Status) {
			continue
		}
		if peer.IsPatch() && opts.HidePatches {
			hiddenPatches[peer.ProductID]++
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
		group := CompatGroup{
			Product:       p,
			Published:     g.Published(r.ProductID, p.ID),
			Releases:      hits[p.ID],
			HiddenPatches: hiddenPatches[p.ID],
		}
		sort.Slice(group.Releases, func(i, j int) bool {
			return version.Compare(group.Releases[i].Release.Version, group.Releases[j].Release.Version) > 0
		})
		out = append(out, group)
	}
	return out
}

// PairVerdict is the compatibility outcome for one product pair in a stack.
type PairVerdict struct {
	// Constrains is false for pairs that are looked up but never allowed to decide a
	// stack — whether because they are not dependencies (vCenter × VKr) or because they
	// are dependencies whose published grid rules out nothing (ESX × NSX). See
	// model.Constrains.
	Constrains bool
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

// Unverified reports whether this pair could not be checked, as opposed to failing.
func (v PairVerdict) Unverified() bool { return !v.Published || !v.HasEdge }

// CheckResult is a whole-stack verdict.
type CheckResult struct {
	Pairs []PairVerdict
}

// Incompatible returns the constraining pairs that actively fail.
//
// Pairs outside the constraint set are excluded: vCenter against VKS is published but VKS
// does not run on vCenter, so a mismatch there says nothing about whether the stack works.
func (c CheckResult) Incompatible() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if p.Constrains && !p.Unverified() && !Compatible(p.Status) {
			out = append(out, p)
		}
	}
	return out
}

// Blocking returns the constraining pairs that cannot appear in one stack.
//
// This is stricter than Incompatible, and deliberately: inside a pair upstream publishes,
// a missing cell is the matrix declining to list the two releases together, not an open
// question. The solver has always read it that way when choosing releases — consistent()
// rejects a release whose cell against an assigned one is absent — so validating pinned
// releases any other way let a pin pair through that the solver would never have picked,
// and the map lit peers a release was never published against.
//
// Unverified pairs whose *product pair* upstream does not publish at all are a different
// thing and still pass: seven of the twenty-one have no data by design.
func (c CheckResult) Blocking() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if p.Constrains && p.Published && (!p.HasEdge || !Compatible(p.Status)) {
			out = append(out, p)
		}
	}
	return out
}

// Informational returns published pairs outside the constraint set, with their status.
// Worth showing when the caller named the combination, never worth failing on.
func (c CheckResult) Informational() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if !p.Constrains && !p.Unverified() {
			out = append(out, p)
		}
	}
	return out
}

// Unverified returns the pairs that could not be checked at all.
func (c CheckResult) Unverified() []PairVerdict {
	var out []PairVerdict
	for _, p := range c.Pairs {
		if p.Constrains && p.Unverified() {
			out = append(out, p)
		}
	}
	return out
}

// OK reports whether nothing actively fails. Unverified pairs are a warning, not a
// failure — seven of the twenty-one pairs have no upstream data by design.
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
			Constrains: model.Constrains(a, b),
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
