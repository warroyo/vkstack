package web

import (
	"strings"
	"testing"

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/version"
)

// supRelease builds a Supervisor release in General Support. The support flags must be
// set explicitly: a zero-value Release reads as End of Support, since the matrix encodes
// the phase as two positive flags.
func supRelease(raw string) *graph.Release {
	p, _ := model.ByKey("supervisor")
	return &graph.Release{
		Raw: raw, ProductKey: "supervisor", Version: version.Parse(raw, p.Scheme),
		TechGuided: true, GenGuided: true,
	}
}

// The matrix carries the support phase as two flags. Pin the mapping, including the
// zero value, so nothing silently reclassifies releases.
func TestSupportPhaseMapping(t *testing.T) {
	for _, tc := range []struct {
		tech, gen bool
		want      graph.SupportPhase
	}{
		{true, true, graph.PhaseGeneral},
		{true, false, graph.PhaseTechnicalGuidance},
		{false, false, graph.PhaseEndOfSupport},
		// genGuided alone still means General Support: it is the stronger flag.
		{false, true, graph.PhaseGeneral},
	} {
		r := graph.Release{TechGuided: tc.tech, GenGuided: tc.gen}
		if got := r.Phase(); got != tc.want {
			t.Errorf("tech=%v gen=%v -> %q, want %q", tc.tech, tc.gen, got, tc.want)
		}
	}
	if graph.PhaseGeneral.Supported() != true || graph.PhaseTechnicalGuidance.Supported() {
		t.Error("only General Support counts as supported")
	}
}

// A node takes the best phase among its releases: a line stays usable while any release
// in it is still generally supported.
func TestNodePhaseTakesTheBestOfItsReleases(t *testing.T) {
	general := supRelease("v1.30.14+vmware.8-fips-vsc9.1.0.0")
	legacy := supRelease("v1.30.5+vmware.4-fips-vsc9.0.0.0")
	legacy.GenGuided, legacy.TechGuided = false, true

	var node mapNode
	setPhase(&node, []*graph.Release{legacy, general})
	if node.Phase != string(graph.PhaseGeneral) || node.Legacy {
		t.Errorf("mixed line should stay generally supported, got %q legacy=%v", node.Phase, node.Legacy)
	}

	setPhase(&node, []*graph.Release{legacy})
	if node.Phase != string(graph.PhaseTechnicalGuidance) || !node.Legacy {
		t.Errorf("all-legacy line should be flagged, got %q legacy=%v", node.Phase, node.Legacy)
	}
}

// Supervisor ships the same Kubernetes version on two trains, and they are not
// interchangeable: a vCenter 8 deployment takes vsc0 only. Grouping by Kubernetes minor
// alone merged them into one node that looked compatible with everything.
func TestSupervisorTrainsAreSeparateNodes(t *testing.T) {
	p, _ := model.ByKey("supervisor")

	vsc9 := supRelease("v1.31.11+vmware.8-fips-vsc9.1.0.0")
	vsc0 := supRelease("v1.31.11+vmware.1-fips-vsc0.1.15")

	if got := supervisorTrain(vsc9); got != "vsc9" {
		t.Errorf("train of %q = %q, want vsc9", vsc9.Raw, got)
	}
	if got := supervisorTrain(vsc0); got != "vsc0" {
		t.Errorf("train of %q = %q, want vsc0", vsc0.Raw, got)
	}

	a, b := lineKey(p, vsc9), lineKey(p, vsc0)
	if a == b {
		t.Fatalf("both trains collapsed to one node key %q — the distinction is lost", a)
	}
	if a != "1.31 vsc9" || b != "1.31 vsc0" {
		t.Errorf("line keys = %q and %q, want \"1.31 vsc9\" and \"1.31 vsc0\"", a, b)
	}
}

// VKr carries a Kubernetes minor too but has only one train, so it must not sprout a
// train suffix.
func TestNonSupervisorProductsHaveNoTrain(t *testing.T) {
	vkr, _ := model.ByKey("vkr")
	r := &graph.Release{Raw: "1.36.1", Version: version.Parse("1.36.1", vkr.Scheme)}
	if got := lineKey(vkr, r); got != "1.36" {
		t.Errorf("VKr line key = %q, want \"1.36\"", got)
	}

	vc, _ := model.ByKey("vcenter")
	v := &graph.Release{Raw: "8.0U3k", Version: version.Parse("8.0U3k", vc.Scheme)}
	if got := lineKey(vc, v); got != "8.0U3k" {
		t.Errorf("vCenter must not be collapsed, got %q", got)
	}
}

// Several entries under one Supervisor minor are the same minor delivered by different
// vSphere releases, not competing patches. Verified against the live matrix: for a given
// vCenter and a given delivery there is exactly one Kubernetes patch per minor. The node
// text has to say that, because reading the list as a choice between patches is the
// easiest mistake to make here.
func TestSupervisorDetailNamesDeliveriesNotPatches(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.30.14+vmware.8-fips-vsc9.1.0.0"),
		supRelease("v1.30.10+vmware.1-fips-vsc9.0.1.0"),
		supRelease("v1.30.5+vmware.4-fips-vsc9.0.0.0"),
	}

	detail, labels := describeSupervisor(rels, "9.1.0.0")
	if detail != "1.30.14 · ships with this vCenter, 2 more supported" {
		t.Errorf("detail = %q", detail)
	}
	if len(labels) != 3 || labels[0] != "v1.30.14+vmware.8-fips-vsc9.1.0.0 — ships with vCenter 9.1.0.0" {
		t.Errorf("expected the shipped build first and labelled, got %v", labels)
	}

	// The build that ships with the pin leads even when it is not first in the input.
	detail, labels = describeSupervisor(rels, "9.0.0.0")
	if detail != "1.30.5 · ships with this vCenter, 2 more supported" {
		t.Errorf("detail with an older pin = %q", detail)
	}
	if labels[0] != "v1.30.5+vmware.4-fips-vsc9.0.0.0 — ships with vCenter 9.0.0.0" {
		t.Errorf("expected the matching delivery promoted, got %q", labels[0])
	}
}

// Two async deliveries of the identical Kubernetes patch must not read as two patches.
func TestSupervisorDetailCollapsesOneK8sPatch(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.15"),
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.14"),
	}
	detail, _ := describeSupervisor(rels, "8.0U3k")
	if detail != "1.32.9 · in 2 releases" {
		t.Errorf("detail = %q, want the single patch named once", detail)
	}
}

// Supervisor ships out of band from vCenter, so a supported delivery can be *newer* than
// the selected vCenter. Nothing may describe the other entries as older.
func TestSupervisorDeliveriesMayBeNewerThanTheVCenter(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.32.9+vmware.2-fips-vsc9.0.2.0100"), // delivered by a later vCenter patch
		supRelease("v1.32.9+vmware.2-fips-vsc9.0.2.0"),
	}
	detail, labels := describeSupervisor(rels, "9.0.2.0")
	if strings.Contains(detail, "older") {
		t.Errorf("detail %q calls a newer delivery older", detail)
	}
	if detail != "1.32.9 · ships with this vCenter, 1 more supported" {
		t.Errorf("detail = %q", detail)
	}
	if labels[0] != "v1.32.9+vmware.2-fips-vsc9.0.2.0 — ships with vCenter 9.0.2.0" {
		t.Errorf("expected the matching delivery first, got %q", labels[0])
	}
}

// Several genuinely different Kubernetes patches under one minor is normal on 9.x, and
// must not be described as a single version.
func TestSupervisorMultipleK8sPatchesUnderOneMinor(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.30.14+vmware.8-fips-vsc9.1.0.0"),
		supRelease("v1.30.10+vmware.1-fips-vsc9.0.1.0"),
		supRelease("v1.30.5+vmware.4-fips-vsc9.0.0.0"),
	}
	// No delivery matches, so nothing "ships with" the pin.
	detail, _ := describeSupervisor(rels, "9.0.3.0")
	if detail != "3 versions · newest 1.30.14" {
		t.Errorf("detail = %q, want the count and the newest patch", detail)
	}
}

// The matrix is the source of truth. If it lists two versions as compatible, the map
// must show that — even when no intermediate version bridges them into a whole stack.
// Hiding such a node would contradict the matrix; it is lit and flagged instead.
func TestMatrixCompatibilityIsNeverHidden(t *testing.T) {
	h := testServer(t)
	body := get(t, h, "/api/stackmap?product=vcenter&version=9.0.0.0")

	lit := map[string]bool{}
	for _, v := range body["lit"].([]any) {
		lit[v.(string)] = true
	}
	incomplete := map[string]bool{}
	for _, v := range body["incomplete"].([]any) {
		incomplete[v.(string)] = true
	}

	// VKr 1.20 is listed against this vCenter but no VKS reaches it, so no whole stack
	// can contain it. It must still be lit — hiding it would contradict the matrix.
	if !lit["vkr:1.20"] {
		t.Error("a version the matrix lists as compatible must be lit even with no whole stack")
	}
	if !incomplete["vkr:1.20"] {
		t.Error("expected it flagged as unable to complete a stack")
	}

	// A version that does complete a stack must not carry that flag.
	if !lit["vkr:1.36"] {
		t.Fatal("expected the stackable VKr to be lit")
	}
	if incomplete["vkr:1.36"] {
		t.Error("a version that completes a stack must not be flagged")
	}

	// Connectors show buildable paths, so the unstackable node has none.
	for _, raw := range body["edges"].([]any) {
		e := raw.(map[string]any)
		if e["to"] == "vkr:1.20" || e["from"] == "vkr:1.20" {
			t.Errorf("unstackable node should have no connector, got %v", e)
		}
	}
}
