package web

import (
	"sort"
	"strings"
	"testing"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/version"
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

	vcs := map[string]bool{"9.1.0.0": true, "9.0.1.0": true, "9.0.0.0": true, "9.0.2.0": true, "9.0.2.0100": true}
	detail, labels := describeSupervisor(rels, "9.1.0.0", vcs)
	if detail != "1.30.14 · ships with this vCenter, 2 more supported" {
		t.Errorf("detail = %q", detail)
	}
	if len(labels) != 3 || labels[0] != "v1.30.14+vmware.8-fips-vsc9.1.0.0 — ships with vCenter 9.1.0.0, your selection" {
		t.Errorf("expected the shipped build first and labelled, got %v", labels)
	}

	// The build that ships with the pin leads even when it is not first in the input.
	detail, labels = describeSupervisor(rels, "9.0.0.0", vcs)
	if detail != "1.30.5 · ships with this vCenter, 2 more supported" {
		t.Errorf("detail with an older pin = %q", detail)
	}
	if labels[0] != "v1.30.5+vmware.4-fips-vsc9.0.0.0 — ships with vCenter 9.0.0.0, your selection" {
		t.Errorf("expected the matching delivery promoted, got %q", labels[0])
	}
}

// Two async deliveries of the identical Kubernetes patch must not read as two patches.
func TestSupervisorDetailCollapsesOneK8sPatch(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.15"),
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.14"),
	}
	detail, _ := describeSupervisor(rels, "8.0U3k", map[string]bool{"8.0U3k": true})
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
	detail, labels := describeSupervisor(rels, "9.0.2.0", map[string]bool{"9.0.2.0": true, "9.0.2.0100": true})
	if strings.Contains(detail, "older") {
		t.Errorf("detail %q calls a newer delivery older", detail)
	}
	if detail != "1.32.9 · ships with this vCenter, 1 more supported" {
		t.Errorf("detail = %q", detail)
	}
	if labels[0] != "v1.32.9+vmware.2-fips-vsc9.0.2.0 — ships with vCenter 9.0.2.0, your selection" {
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
	detail, _ := describeSupervisor(rels, "9.0.3.0", map[string]bool{})
	if detail != "3 versions · newest 1.30.14" {
		t.Errorf("detail = %q, want the count and the newest patch", detail)
	}
}

// Only real dependencies constrain a stack. VKr 1.20 in the fixture is listed against
// the vCenter but no VKS reaches it — and VKS is what actually provisions VKr — so no
// valid stack contains it and it must not be lit.
//
// The direct vCenter-to-VKr listing is informational: worth looking up, wrong to treat
// as permission to run the combination.
func TestNonDependencyPairsDoNotAdmitAStack(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap?product=vcenter&version=9.0.0.0")

	lit := map[string]bool{}
	for _, v := range body["lit"].([]any) {
		lit[v.(string)] = true
	}
	if lit["vkr:1.20"] {
		t.Error("a VKr with no VKS to provision it must not be lit, however the vCenter pair reads")
	}
	if !lit["vkr:1.36"] {
		t.Error("expected the VKr that a VKS does reach to be lit")
	}
}

// The two Supervisor trains number themselves differently, and "delivered by 0.1.15" is
// meaningless — 0.1.15 is not a vSphere version. Each train must be described in terms
// that are true for it.
func TestDeliveryPhraseIsTrainAware(t *testing.T) {
	vcs := map[string]bool{"9.1.0.0200": true}
	for _, tc := range []struct{ vsc, want string }{
		{"9.1.0.0200", "vCenter 9.1.0.0200"}, // verified against real releases
		// Nothing is claimed for values that match no vCenter release. What the vsc0
		// sequence denotes is not established, and 9.0.0.0100 is 9.x-shaped but matches
		// nothing, so both stay unnarrated.
		{"0.1.15", ""},
		{"9.0.0.0100", ""},
		{"", ""},
	} {
		if got := deliveryPhrase(tc.vsc, vcs); got != tc.want {
			t.Errorf("deliveryPhrase(%q) = %q, want %q", tc.vsc, got, tc.want)
		}
	}
}

// The vsc0 sequence does not correspond to a vCenter release, and what it does denote is
// not established. Labels must state the version and nothing more — no invented
// provenance, and no vCenter framing that does not apply.
func TestVsc0LabelsMakeNoProvenanceClaim(t *testing.T) {
	rels := []*graph.Release{
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.15"),
		supRelease("v1.32.9+vmware.2-fips-vsc0.1.14"),
	}
	_, labels := describeSupervisor(rels, "8.0U3k", map[string]bool{"8.0U3k": true})
	for _, l := range labels {
		if strings.Contains(l, "vCenter") || strings.Contains(l, "ships with") {
			t.Errorf("label should not mention vCenter: %q", l)
		}
		if strings.Contains(l, "async") || strings.Contains(l, "from ") {
			t.Errorf("label should claim no provenance: %q", l)
		}
	}
	if labels[0] != "v1.32.9+vmware.2-fips-vsc0.1.15" {
		t.Errorf("expected the bare version, got %q", labels[0])
	}
}

// Everything the client can send back as a pin has to resolve. Releases carries machine
// identity — the pin token and the key the recommended stack is matched against — while
// prose about a release belongs in Notes.
//
// Conflating the two made every lit Supervisor node unclickable: describeSupervisor's
// decorated labels landed in Releases, the client posted one back as a version, and the
// server answered 400 for a release that plainly exists. It happened on the second click
// of a fresh session, since the map auto-pins the newest vCenter on arrival.
func TestEveryNodePinResolves(t *testing.T) {
	g := testGraph(t)
	h := serverForGraph(t, g)

	for _, path := range []string{
		"/api/stackmap",
		"/api/stackmap?product=vcenter&version=9.0.0.0",
		"/api/stackmap?product=vcenter&version=8.0U3",
		"/api/stackmap?product=supervisor&version=v1.33.0%2Bvmware.1-fips-vsc9.0.0.0",
	} {
		body := get(t, h, path)
		for _, l := range body["layers"].([]any) {
			layer := l.(map[string]any)
			key := layer["key"].(string)
			for _, n := range layer["nodes"].([]any) {
				node := n.(map[string]any)
				pin, _ := node["pin"].(string)
				if pin == "" {
					t.Errorf("%s: %s node %v has no pin token", path, key, node["id"])
					continue
				}
				if _, err := g.Resolve(key, pin); err != nil {
					t.Errorf("%s: pin %q on %v does not resolve: %v", path, pin, node["id"], err)
				}
				for _, r := range node["releases"].([]any) {
					if _, err := g.Resolve(key, r.(string)); err != nil {
						t.Errorf("%s: release %q on %v does not resolve: %v",
							path, r, node["id"], err)
					}
				}
			}
		}
	}
}

// The decorated Supervisor labels still have to exist — they are the reason a reader can
// tell one delivery from another. They just live where nothing sends them back.
func TestSupervisorProseLivesInNotes(t *testing.T) {
	g := testGraph(t)
	h := serverForGraph(t, g)
	body := get(t, h, "/api/stackmap?product=vcenter&version=9.0.0.0")

	found := false
	for _, l := range body["layers"].([]any) {
		layer := l.(map[string]any)
		if layer["key"] != "supervisor" {
			continue
		}
		for _, n := range layer["nodes"].([]any) {
			node := n.(map[string]any)
			notes, _ := node["notes"].([]any)
			for _, note := range notes {
				if strings.Contains(note.(string), "vCenter 9.0.0.0") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected a Supervisor note naming the delivering vCenter")
	}
}

// A grouped node keeps its best phase as its headline, because the line really is still
// usable. It must also name its worst, or a green node covering an End of Support build
// invites picking one blind.
func TestNodePhaseNamesItsWorstReleaseToo(t *testing.T) {
	general := supRelease("v1.30.14+vmware.8-fips-vsc9.1.0.0")
	eos := supRelease("v1.30.5+vmware.4-fips-vsc9.0.0.0")
	eos.GenGuided, eos.TechGuided = false, false

	var node mapNode
	setPhase(&node, []*graph.Release{general, eos})
	switch {
	case node.Phase != string(graph.PhaseGeneral):
		t.Errorf("headline phase = %q, want general", node.Phase)
	case node.WorstPhase != string(graph.PhaseEndOfSupport):
		t.Errorf("worst phase = %q, want end-of-support", node.WorstPhase)
	case !node.Mixed:
		t.Error("a line holding both phases must be marked mixed")
	case node.Legacy:
		t.Error("a line with a generally supported release is not legacy")
	}

	setPhase(&node, []*graph.Release{general})
	if node.Mixed || node.WorstPhase != string(graph.PhaseGeneral) {
		t.Errorf("uniform line should not be mixed, got mixed=%v worst=%q",
			node.Mixed, node.WorstPhase)
	}
}

// ESX gates the Supervisor as much as vCenter does, so its own row has to narrow through
// the pin. ESX used to be an annotation on the vCenter node, and the annotation listed
// every host the vCenter paired with — hosts that cannot carry the Supervisor on screen.
func TestESXLayerNarrowsThroughThePin(t *testing.T) {
	h := serverForGraph(t, testGraph(t))

	lit := litSet(t, get(t, h, "/api/stackmap?product=vcenter&version=8.0U3"))
	if !lit["esx:8.0U3"] {
		t.Fatalf("8.0U3 carries this vCenter and its Supervisor, so it must be lit: %v", keysOfSet(lit))
	}
	// The vCenter accepts 8.0U3a and no Supervisor is published against it. The old
	// annotation on the vCenter node listed it anyway, because it asked the pairwise
	// question; the row asks whether a whole stack exists.
	if lit["esx:8.0U3a"] {
		t.Errorf("8.0U3a reaches no Supervisor, so it must not be lit: %v", keysOfSet(lit))
	}

	lit = litSet(t, get(t, h, "/api/stackmap?product=vcenter&version=9.0.0.0"))
	if !lit["esx:9.0.0.0"] {
		t.Errorf("expected the 9.0.0.0 host to be lit for the 9.0.0.0 vCenter: %v", keysOfSet(lit))
	}
	if lit["esx:8.0U3"] {
		t.Errorf("the 8.0U3 host cannot carry a 9.0.0.0 vCenter: %v", keysOfSet(lit))
	}
}

// ESX is in every stack, so it is not Optional — but it follows vCenter, so the row is
// folded until a reader asks for it. The distinction is the whole point: Optional reaches
// the solver through `with`, Collapsed never leaves the client.
func TestESXLayerIsCollapsedButNotOptional(t *testing.T) {
	body := get(t, serverForGraph(t, testGraph(t)), "/api/stackmap")
	for _, raw := range body["layers"].([]any) {
		layer := raw.(map[string]any)
		if layer["key"] != "esx" {
			continue
		}
		if collapsed, _ := layer["collapsed"].(bool); !collapsed {
			t.Error("the ESX row has to come back collapsed")
		}
		if optional, _ := layer["optional"].(bool); optional {
			t.Error("ESX is in every stack, so it must not be marked optional")
		}
		if note, _ := layer["note"].(string); note == "" {
			t.Error("a folded row needs a note saying when it applies")
		}
		return
	}
	t.Fatal("no ESX layer in the map")
}

// litSet reads the lit node ids out of a stack-map response.
func litSet(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()
	raw, ok := body["lit"].([]any)
	if !ok {
		t.Fatalf("expected a lit set, got %v", body["lit"])
	}
	out := map[string]bool{}
	for _, v := range raw {
		out[v.(string)] = true
	}
	return out
}

func keysOfSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The map used to draw an identical empty frame for "nothing selected" and "this
// selection has no complete stack". The second is an answer, so the response has to make
// it distinguishable: a pin with no stack lights nothing and recommends nothing.
func TestDeadEndPinIsDistinguishableFromNoPin(t *testing.T) {
	h := serverForGraph(t, testGraph(t))

	none := get(t, h, "/api/stackmap")
	if _, ok := none["lit"]; ok {
		t.Error("an unpinned map should not carry a lit set")
	}

	dead := get(t, h, "/api/stackmap?product=vkr&version=1.20.0")
	lit, ok := dead["lit"].([]any)
	if !ok {
		t.Fatal("a pinned map must carry a lit set even when nothing is lit")
	}
	if len(lit) != 0 {
		t.Errorf("VKr 1.20.0 reaches no stack, so nothing should be lit: %v", lit)
	}
	if _, ok := dead["recommended"]; ok {
		t.Error("a dead end must not recommend a stack")
	}
}

// The recommended stack has to carry the support phase of every release in it. The legacy
// filter is a view setting and the solver reaches past it, so a recommendation can name a
// release the map is hiding — which is only honest if the client can tell.
func TestRecommendedStackCarriesPhases(t *testing.T) {
	h := serverForGraph(t, testGraph(t))
	body := get(t, h, "/api/stackmap?product=vcenter&version=9.0.0.0")

	rec, ok := body["recommended"].(map[string]any)
	if !ok {
		t.Fatal("expected a recommended stack")
	}
	for key, v := range rec {
		pick, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s: recommendation is not an object: %T", key, v)
		}
		for _, field := range []string{"version", "phase", "phaseLabel"} {
			if s, _ := pick[field].(string); s == "" {
				t.Errorf("%s: recommendation is missing %s", key, field)
			}
		}
	}
	if _, ok := body["provenance"].(map[string]any); !ok {
		t.Error("expected a provenance summary alongside the recommendation")
	}
}
