package model

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEveryEdgeNamesRealProducts(t *testing.T) {
	for _, e := range Edges {
		if _, ok := ByKey(e.From); !ok {
			t.Errorf("edge %s->%s: unknown product %q", e.From, e.To, e.From)
		}
		if _, ok := ByKey(e.To); !ok {
			t.Errorf("edge %s->%s: unknown product %q", e.From, e.To, e.To)
		}
		if strings.TrimSpace(e.Prose) == "" {
			t.Errorf("edge %s->%s has no prose; the whole point is explaining why", e.From, e.To)
		}
		// The on-screen explainer shows only the summary, so an edge without one would
		// render as a bare arrow with no explanation at all.
		if strings.TrimSpace(e.Summary) == "" {
			t.Errorf("edge %s->%s has no summary for the on-screen explainer", e.From, e.To)
		}
		if len(e.Summary) > 90 {
			t.Errorf("edge %s->%s summary is %d chars; that view is a picture, not a doc",
				e.From, e.To, len(e.Summary))
		}
	}
}

func TestEveryProductAppearsInAnEdge(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Edges {
		seen[e.From], seen[e.To] = true, true
	}
	for _, p := range Products {
		if !seen[p.Key] {
			t.Errorf("product %q appears in no edge and would float unconnected in the diagram", p.Key)
		}
	}
}

func TestUpgradeOrderIsTotal(t *testing.T) {
	order := UpgradeOrder()
	if len(order) != len(Products) {
		t.Fatalf("upgrade order covers %d products, want %d", len(order), len(Products))
	}
	seen := map[string]bool{}
	for _, k := range order {
		if seen[k] {
			t.Errorf("product %q appears twice in the upgrade order", k)
		}
		seen[k] = true
	}
	positions := map[int]string{}
	for _, p := range Products {
		if prev, dup := positions[p.UpgradeOrder]; dup {
			t.Errorf("products %q and %q share upgrade position %d", prev, p.Key, p.UpgradeOrder)
		}
		positions[p.UpgradeOrder] = p.Key
	}
}

func TestPairsCoversEveryCombination(t *testing.T) {
	pairs := Pairs()
	want := len(Products) * (len(Products) - 1) / 2
	if len(pairs) != want {
		t.Errorf("got %d pairs, want %d", len(pairs), want)
	}
	for _, p := range pairs {
		if p[0] >= p[1] {
			t.Errorf("pair %v is not normalised to lower id first", p)
		}
	}
}

// Every node id an edge references must be declared, or mermaid renders a phantom box.
func TestMermaidDeclaresEveryReferencedNode(t *testing.T) {
	out := Mermaid(DefaultCoverage)

	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s{4}([A-Z]+)\[`).FindAllStringSubmatch(out, -1) {
		declared[m[1]] = true
	}
	edgeRe := regexp.MustCompile(`(?m)^\s{4}([A-Z]+)\s+(?:<-->|-->|-\.->)`)
	targetRe := regexp.MustCompile(`\|\s+([A-Z]+)\s*$`)
	for line := range strings.SplitSeq(out, "\n") {
		if m := edgeRe.FindStringSubmatch(line); m != nil && !declared[m[1]] {
			t.Errorf("edge source %q is never declared as a node", m[1])
		}
		if m := targetRe.FindStringSubmatch(line); m != nil && !declared[m[1]] {
			t.Errorf("edge target %q is never declared as a node", m[1])
		}
	}
	if len(declared) != len(Products) {
		t.Errorf("declared %d nodes, want %d", len(declared), len(Products))
	}
}

// Unpublished pairs must render as dashed and labelled, since that distinction is the
// main thing the diagram teaches beyond the box layout.
func TestMermaidMarksUnpublishedPairs(t *testing.T) {
	out := Mermaid(DefaultCoverage)
	if !strings.Contains(out, "no published data") {
		t.Error("expected unpublished pairs to be flagged in the diagram")
	}
	// With everything published, that flag must disappear.
	if strings.Contains(Mermaid(AllPublished), "no published data") {
		t.Error("with full coverage no edge should claim to be missing data")
	}
}

// The two Supervisor trains are the most misread fact in this model, so the diagram has
// to carry them rather than leaving them to prose.
func TestMermaidShowsSupervisorTrains(t *testing.T) {
	out := Mermaid(DefaultCoverage)
	for _, want := range []string{"two trains", "vsc9", "vsc0"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected the diagram to mention %q", want)
		}
	}
}

// Every edge must declare how it is known, or an asserted rule reads like a lookup.
func TestEveryEdgeDeclaresEvidence(t *testing.T) {
	for _, e := range Edges {
		if e.Evidence == "" {
			t.Errorf("edge %s->%s has no evidence set", e.From, e.To)
		}
		if e.Evidence.Describe() == string(e.Evidence) {
			t.Errorf("edge %s->%s has an unrecognised evidence %q", e.From, e.To, e.Evidence)
		}
	}
}

// The unknowns list is the honesty surface: it must name every unpublished pair and say
// what each one costs.
func TestUnknownsCoverEveryUnpublishedPair(t *testing.T) {
	unknowns := Unknowns(DefaultCoverage)
	if len(unknowns) < len(KnownUnpublishedPairs()) {
		t.Fatalf("got %d unknowns, expected at least one per unpublished pair (%d)",
			len(unknowns), len(KnownUnpublishedPairs()))
	}
	for _, pr := range KnownUnpublishedPairs() {
		a, _ := ByID(pr[0])
		b, _ := ByID(pr[1])
		found := false
		for _, u := range unknowns {
			if strings.Contains(u.Title, a.Label) && strings.Contains(u.Title, b.Label) {
				found = true
			}
		}
		if !found {
			t.Errorf("no unknown documents the %s × %s gap", a.Label, b.Label)
		}
	}
	for _, u := range unknowns {
		if u.Detail == "" || u.Consequence == "" {
			t.Errorf("unknown %q must say what it is and what it means", u.Title)
		}
	}

	// With full coverage the pair-specific entries drop out, but the standing limits
	// (upgrade safety, untested combinations, the version floor) must remain.
	if len(Unknowns(AllPublished)) == 0 {
		t.Error("expected standing limits even when every pair is published")
	}
}

func TestKnownUnpublishedPairsMatchesDefaultCoverage(t *testing.T) {
	pairs := KnownUnpublishedPairs()
	if len(pairs) != 7 {
		t.Fatalf("expected 7 known-unpublished pairs, got %d", len(pairs))
	}
	for _, p := range pairs {
		if DefaultCoverage(p[0], p[1]) {
			t.Errorf("pair %v is listed unpublished but DefaultCoverage says published", p)
		}
		// Coverage must be order-independent.
		if DefaultCoverage(p[1], p[0]) {
			t.Errorf("pair %v reversed disagrees with itself", p)
		}
	}
}

// docs/model.md is generated. This regenerates it and fails if the committed copy has
// drifted, which is what keeps the doc, the diagram and `vkstack explain` in step.
// Run `go test ./internal/model -update` to refresh it.
func TestDocMatchesCommittedFile(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "model.md")
	want := Doc(DefaultCoverage)

	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		t.Logf("updated %s", path)
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v (run `go test ./internal/model -update`)", path, err)
	}
	if string(got) != want {
		t.Errorf("%s is out of date — run `go test ./internal/model -update`", path)
	}
}
