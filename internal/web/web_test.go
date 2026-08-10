package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

const (
	esx = 1
	vc  = 2
	sup = 1378
	vks = 1794
	vkr = 820
	nsx = 912
	avi = 1795
)

// testGraph builds a minimal but realistic graph, including version strings that contain
// the characters mermaid treats as syntax.
func testGraph(t *testing.T) *graph.Graph {
	t.Helper()
	snap := &store.Snapshot{FetchedAt: 1_700_000_000_000}
	add := func(id, product int, hybrid string) {
		snap.Releases = append(snap.Releases, store.Release{
			ID: id, ProductID: product, HybridVersion: hybrid, ReleaseType: "Minor", GADate: 1,
		})
	}
	add(21, vc, "9.0.0.0")
	add(11, esx, "9.0.0.0")
	// A second base, so a Supervisor build can exist on the 1.33 line without being
	// compatible with the 9.0.0.0 vCenter.
	add(22, vc, "8.0U3")
	add(12, esx, "8.0U3")
	// A host the vCenter accepts and the Supervisor does not. ESX gates the Supervisor,
	// so this must drop off the vCenter node's host list once a Supervisor is pinned.
	add(13, esx, "8.0U3a")
	// Deliberately awkward: "+" and a parenthetical, both of which have bitten before.
	add(31, sup, "v1.33.0+vmware.1-fips-vsc9.0.0.0")
	// Same Kubernetes minor and train as 31, but only 31 works with the vCenter below.
	// The node covering this line must report one build, not two.
	add(32, sup, "v1.33.0+vmware.1-fips-vsc9.0.0.0100")
	add(41, vks, "3.7.0+v1.36")
	add(51, vkr, "1.36.1 (TKr 1.36.1 for vSphere 9.x)")
	// Listed against vCenter but reachable by no VKS: the matrix calls it compatible
	// while no whole stack can contain it.
	add(52, vkr, "1.20.0")
	// The optional layers. Two builds on one NSX line, so the layer exercises grouping;
	// one Avi build per line.
	add(61, nsx, "9.1.0.0")
	add(62, nsx, "9.1.0.0100")
	add(63, nsx, "4.2.4")
	add(71, avi, "32.1.1")
	add(72, avi, "30.2.1")
	// Reaches vCenter and nothing above it: a dead end the reader can select, which is
	// exactly what the newest release of a sparsely published product looks like.
	add(73, avi, "32.1.2")

	for _, pr := range model.Pairs() {
		count := 1
		switch {
		case pr[0] == esx && pr[1] == vks,
			pr[0] == esx && pr[1] == vkr,
			pr[0] == vkr && pr[1] == sup,
			pr[0] == sup && pr[1] == vkr,
			pr[0] == vkr && pr[1] == nsx, pr[0] == nsx && pr[1] == vkr,
			pr[0] == nsx && pr[1] == vks, pr[0] == vks && pr[1] == nsx,
			pr[0] == vkr && pr[1] == avi, pr[0] == avi && pr[1] == vkr,
			pr[0] == vks && pr[1] == avi, pr[0] == avi && pr[1] == vks:
			count = 0
		}
		snap.Coverage = append(snap.Coverage, store.PairCoverage{
			AProduct: pr[0], BProduct: pr[1], EdgeCount: count,
		})
	}
	ok := func(a, b int) {
		snap.Compat = append(snap.Compat, store.Compat{ARelease: a, BRelease: b, Status: store.StatusCompatible})
	}
	ok(11, 21)
	ok(21, 31)
	ok(11, 31)
	ok(21, 41)
	ok(21, 51)
	ok(31, 41)
	ok(41, 51)
	ok(21, 52) // compatible with vCenter, bridged by nothing
	// The 8.0U3 base carries the other 1.33 build, and only that one.
	ok(12, 22)
	ok(13, 22) // accepted by the vCenter, but never listed against a Supervisor
	ok(22, 32)
	ok(12, 32)
	ok(22, 41)
	ok(22, 51)
	ok(32, 41)

	// NSX 9.1.x rides the 9.0.0.0 base and its Supervisor; 4.2.4 rides the 8.0U3 one.
	ok(21, 61)
	ok(11, 61)
	ok(31, 61)
	ok(21, 62)
	ok(11, 62)
	ok(31, 62)
	ok(22, 63)
	ok(12, 63)
	ok(32, 63)

	// Avi 32.1.1 goes with the new base, 30.2.1 with the old. Note there is deliberately
	// no ESX × Avi row at all: upstream's grid is nearly empty, the pair is not a
	// dependency, and a stack with Avi in it must still solve.
	ok(21, 71)
	ok(31, 71)
	ok(61, 71)
	ok(62, 71)
	ok(22, 72)
	ok(32, 72)
	ok(63, 72)
	ok(21, 73) // vCenter only — no Supervisor will take it

	g, err := graph.Load(snap, graph.Options{})
	if err != nil {
		t.Fatalf("loading test graph: %v", err)
	}
	return g
}

func testServer(t *testing.T) http.Handler {
	t.Helper()
	return serverWith(t, Config{Refresh: func(func(int, int, string)) error { return nil }})
}

// serverForGraph builds a server over a graph the test already holds, so it can check
// responses against the same releases the handler saw.
func serverForGraph(t *testing.T, g *graph.Graph) http.Handler {
	t.Helper()
	h, err := NewServer(Config{
		Load:    func() (*graph.Graph, error) { return g, nil },
		Refresh: func(func(int, int, string)) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return h
}

// serverWith builds a server around the fixture graph, letting a test set only the
// config fields it cares about.
func serverWith(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	g := testGraph(t)
	cfg.Load = func() (*graph.Graph, error) { return g, nil }
	h, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET %s: decoding response: %v", path, err)
	}
	return out
}

func post(t *testing.T, h http.Handler, path, body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s: status %d, body %s", path, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("POST %s: decoding response: %v", path, err)
	}
	return out
}

// The embedded UI must actually be present, or `serve` ships a blank page.
func TestServesEmbeddedAssets(t *testing.T) {
	h := testServer(t)
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s: empty body", path)
		}
	}
}

// mermaidWellFormed applies the structural checks that catch the failures seen in
// practice: an edge pointing at a node that was never declared, and unescaped syntax
// characters leaking out of a version string into a label.
func mermaidWellFormed(t *testing.T, def string) {
	t.Helper()
	if !strings.HasPrefix(strings.TrimSpace(def), "flowchart") {
		t.Errorf("definition does not start with a flowchart declaration:\n%s", def)
	}

	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9]*)\[`).FindAllStringSubmatch(def, -1) {
		declared[m[1]] = true
	}
	linkRe := regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9]*)\s+(?:---|-\.-|-->|-\.->|<-->)(?:\|[^|]*\|)?\s*([A-Za-z][A-Za-z0-9]*)\s*$`)
	for _, m := range linkRe.FindAllStringSubmatch(def, -1) {
		for _, id := range m[1:] {
			if !declared[id] {
				t.Errorf("edge references undeclared node %q in:\n%s", id, def)
			}
		}
	}

	// Every label must be balanced: an unescaped bracket from a version string would
	// terminate the label early and produce a parse error at render time.
	for _, m := range regexp.MustCompile(`\["([^"]*)"\]`).FindAllStringSubmatch(def, -1) {
		if strings.ContainsAny(m[1], "[]{}|") {
			t.Errorf("label %q still contains mermaid syntax characters", m[1])
		}
	}
}

// Version strings carry "+", "(" and "." — the graph view has to survive all of them.
func TestGraphDiagramEscapesAwkwardVersions(t *testing.T) {
	h := testServer(t)
	for _, tc := range []struct{ product, version string }{
		{"vcenter", "9.0.0.0"},
		{"supervisor", "v1.33.0+vmware.1-fips-vsc9.0.0.0"},
		{"vks", "3.7.0+v1.36"},
		{"vkr", "1.36.1"},
	} {
		t.Run(tc.product, func(t *testing.T) {
			// "+" must be percent-encoded or it decodes to a space in a query string.
			body := get(t, h, "/api/graph?product="+url.QueryEscape(tc.product)+
				"&version="+url.QueryEscape(tc.version))
			mermaidWellFormed(t, body["mermaid"].(string))
		})
	}
}

// A product with no published pair must be called out in the note rather than silently
// rendering as though nothing is compatible.
func TestGraphNoteNamesUnpublishedProducts(t *testing.T) {
	body := get(t, testServer(t), "/api/graph?product=esx&version=9.0.0.0")
	note, _ := body["note"].(string)
	if !strings.Contains(note, "VKS") || !strings.Contains(note, "VKr") {
		t.Errorf("expected the note to name the unpublished products, got %q", note)
	}
}

func TestStackEndpointSolvesAndReportsInferred(t *testing.T) {
	body := post(t, testServer(t), "/api/stack", `{"pins":{"vcenter":"9.0.0.0"}}`)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("expected a solution, got %v", body["error"])
	}
	rec := body["recommended"].(map[string]any)
	for _, key := range []string{"esx", "vcenter", "supervisor", "vks", "vkr"} {
		if rec[key] == nil {
			t.Errorf("recommended stack is missing %s", key)
		}
	}
	// Nothing needs inferring: every gap in the matrix falls on a pair that is not a
	// dependency, so none of them are consulted to validate a stack.
	if inferred, present := body["inferred"]; present && inferred != nil {
		if got := inferred.([]any); len(got) != 0 {
			t.Errorf("expected nothing inferred, got %v", got)
		}
	}
	// Options drive the narrowing dropdowns; without them the UI cannot constrain.
	if _, ok := body["options"].(map[string]any); !ok {
		t.Error("expected per-product options in the response")
	}
}

func TestCheckEndpointSeparatesUnverifiedFromIncompatible(t *testing.T) {
	body := post(t, testServer(t), "/api/check",
		`{"pins":{"vcenter":"9.0.0.0","esx":"9.0.0.0","supervisor":"v1.33.0+vmware.1-fips-vsc9.0.0.0","vks":"3.7.0+v1.36","vkr":"1.36.1"}}`)
	if ok, _ := body["ok"].(bool); !ok {
		t.Error("a consistent stack must pass")
	}
	states := map[string]int{}
	for _, v := range body["verdicts"].([]any) {
		states[v.(map[string]any)["state"].(string)]++
	}
	// The three unpublished pairs are still reported per pair — they are just not
	// dependencies, so they never fail the check.
	if states["unverified"] != 3 {
		t.Errorf("expected the 3 unpublished pairs still reported, got %d (%v)",
			states["unverified"], states)
	}
	if states["bad"] != 0 {
		t.Errorf("expected no incompatible pairs, got %d", states["bad"])
	}
}

func TestUnknownProductIsABadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	testServer(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/releases?product=nope", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an unknown product, got %d", rec.Code)
	}
}

// A read-only instance must refuse client-triggered refreshes outright: the whole point
// of hosting one is that visitors cannot make the server call upstream.
func TestReadOnlyRejectsClientRefresh(t *testing.T) {
	called := false
	h := serverWith(t, Config{
		ReadOnly: true,
		Refresh:  func(func(int, int, string)) error { called = true; return nil },
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/refresh", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 from a read-only instance, got %d", rec.Code)
	}
	if called {
		t.Error("a read-only instance must not reach the refresher at all")
	}

	// Reads must still work.
	if body := get(t, h, "/api/meta"); body["readOnly"] != true {
		t.Error("expected /api/meta to advertise read-only mode")
	}
}

func TestWritableInstanceAllowsRefresh(t *testing.T) {
	called := false
	h := serverWith(t, Config{Refresh: func(func(int, int, string)) error { called = true; return nil }})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Error("expected the refresher to run")
	}
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Errorf("expected a done event in the stream, got %q", rec.Body.String())
	}
}

func TestMetaAdvertisesRefreshInterval(t *testing.T) {
	h := serverWith(t, Config{ReadOnly: true, RefreshInterval: 6 * time.Hour})
	if got := get(t, h, "/api/meta")["refreshInterval"]; got != "6h0m0s" {
		t.Errorf("expected the refresh interval in meta, got %v", got)
	}
}

// Health has to distinguish "up" from "up but serving nothing", or a rollout goes green
// on an instance with an empty cache.
func TestHealthReflectsCacheState(t *testing.T) {
	rec := httptest.NewRecorder()
	testServer(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with a populated cache, got %d", rec.Code)
	}

	empty, err := NewServer(Config{Load: func() (*graph.Graph, error) { return &graph.Graph{}, nil }})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	rec = httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 with an empty cache, got %d", rec.Code)
	}
}

// The stack map is the main view. Its layers must run bottom-up, vCenter must not be
// collapsed by patch (the patch letter changes what a release supports), and ESX must
// not be a layer at all. NSX and Avi sit between the hypervisor and the Supervisor and
// are flagged optional, which is what keeps them collapsed on screen.
func TestStackMapLayers(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap")
	layers := body["layers"].([]any)

	var keys []string
	optional := map[string]bool{}
	for _, l := range layers {
		layer := l.(map[string]any)
		key := layer["key"].(string)
		keys = append(keys, key)
		if opt, _ := layer["optional"].(bool); opt {
			optional[key] = true
		}
	}
	want := []string{"vcenter", "nsx", "avi", "supervisor", "vks", "vkr"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("layers = %v, want %v (bottom-up, no ESX layer)", keys, want)
	}
	if len(optional) != 2 || !optional["nsx"] || !optional["avi"] {
		t.Errorf("optional layers = %v, want exactly nsx and avi", optional)
	}
	// The note is what a collapsed row says for itself, and Avi's has to make clear that
	// it stands alone. A reader who thinks Avi needs NSX will pick the wrong stack.
	for _, l := range layers {
		layer := l.(map[string]any)
		if opt, _ := layer["optional"].(bool); !opt {
			continue
		}
		if note, _ := layer["note"].(string); note == "" {
			t.Errorf("optional layer %v has no note saying when it applies", layer["key"])
		}
	}
}

// Opening one optional layer must never open the other, and the recommended stack must
// name exactly the optional products asked for.
func TestStackMapOptionalLayersAreIndependent(t *testing.T) {
	h := testServer(t)

	cases := []struct {
		with    string
		wantNSX bool
		wantAvi bool
	}{
		{with: "", wantNSX: false, wantAvi: false},
		{with: "nsx", wantNSX: true, wantAvi: false},
		{with: "avi", wantNSX: false, wantAvi: true},
		{with: "nsx,avi", wantNSX: true, wantAvi: true},
	}

	for _, tc := range cases {
		t.Run("with="+tc.with, func(t *testing.T) {
			q := url.Values{"product": {"vcenter"}, "version": {"9.0.0.0"}}
			if tc.with != "" {
				q.Set("with", tc.with)
			}
			body := get(t, h, "/api/stackmap?"+q.Encode())

			rec, ok := body["recommended"].(map[string]any)
			if !ok {
				t.Fatalf("no recommended stack for with=%q", tc.with)
			}
			if _, has := rec["nsx"]; has != tc.wantNSX {
				t.Errorf("NSX in the recommendation = %v, want %v (keys %v)", has, tc.wantNSX, keysOfMap(rec))
			}
			if _, has := rec["avi"]; has != tc.wantAvi {
				t.Errorf("Avi in the recommendation = %v, want %v (keys %v)", has, tc.wantAvi, keysOfMap(rec))
			}
		})
	}
}

// Every solve behind the map has to use the same include set.
//
// Three of the five sites used to build their own StackOptions and drop it, so `lit` and
// the node narrowing described a stack without the optional layers while the
// recommendation and the edges described one with them: the map came back fully lit and
// drawn with a fraction of the connections, and clicking a lit node returned no stack.
func TestStackMapLitFollowsOpenLayers(t *testing.T) {
	h := testServer(t)

	seen := map[string]string{}
	for _, with := range []string{"", "nsx", "avi", "nsx,avi"} {
		q := url.Values{"product": {"vcenter"}, "version": {"9.0.0.0"}}
		if with != "" {
			q.Set("with", with)
		}
		body := get(t, h, "/api/stackmap?"+q.Encode())

		var lit []string
		for _, id := range body["lit"].([]any) {
			lit = append(lit, id.(string))
		}
		sort.Strings(lit)
		seen[with] = strings.Join(lit, ",")

		// Whatever is lit must be reachable: every lit node has to belong to a layer that
		// is actually part of this solve.
		for _, id := range lit {
			key, _, _ := strings.Cut(id, ":")
			if (key == "nsx" || key == "avi") && !strings.Contains(with, key) {
				t.Errorf("with=%q lit %s, a layer that is not in the solve", with, id)
			}
		}
	}

	// Opening Avi genuinely constrains the stack, so it cannot leave the lit set alone.
	if seen["avi"] == seen[""] {
		t.Error("opening the Avi layer left the lit set unchanged — Include is being dropped")
	}
	if seen["nsx"] == seen[""] {
		t.Error("opening the NSX layer left the lit set unchanged — Include is being dropped")
	}
}

// A selection that reaches no stack must say why, and offer the way out when an optional
// layer the reader opened is what closed the door.
func TestStackMapDeadEndExplainsItself(t *testing.T) {
	// Avi 32.1.2 reaches vCenter and nothing above it, so no stack can contain it.
	q := url.Values{"product": {"avi"}, "version": {"32.1.2"}, "with": {"avi"}}
	body := get(t, testServer(t), "/api/stackmap?"+q.Encode())

	if lit, _ := body["lit"].([]any); len(lit) != 0 {
		t.Fatalf("expected a dead end, but %d nodes are lit", len(lit))
	}
	dead, ok := body["deadEnd"].(map[string]any)
	if !ok {
		t.Fatal("a dead end must explain itself, got no deadEnd")
	}
	if reason, _ := dead["reason"].(string); reason == "" {
		t.Error("deadEnd carries no reason")
	}
	if closeLayer, _ := dead["closeLayer"].(string); closeLayer != "avi" {
		t.Errorf("closeLayer = %q, want avi — the layer that made this impossible", closeLayer)
	}
}

// An Avi pin has to solve on its own. Upstream publishes almost nothing for ESX × Avi,
// and that pair is not a dependency, so it must not stand in the way.
func TestStackMapAviPinSolvesWithoutNSX(t *testing.T) {
	q := url.Values{"product": {"avi"}, "version": {"32.1.1"}}
	body := get(t, testServer(t), "/api/stackmap?"+q.Encode())

	rec, ok := body["recommended"].(map[string]any)
	if !ok {
		t.Fatal("an Avi pin produced no stack")
	}
	if _, has := rec["avi"]; !has {
		t.Errorf("the pinned Avi release is missing from the recommendation: %v", keysOfMap(rec))
	}
	if _, has := rec["nsx"]; has {
		t.Errorf("an Avi pin must not pull NSX in: %v", keysOfMap(rec))
	}
}

// NSX and Avi carry no Kubernetes minor, so without an explicit grouping rule every
// release would become its own node.
func TestStackMapGroupsOptionalLayersByLine(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap")

	want := map[string][]string{
		"nsx": {"9.1", "4.2"},
		"avi": {"32.1", "30.2"},
	}
	for _, l := range body["layers"].([]any) {
		layer := l.(map[string]any)
		key := layer["key"].(string)
		expect, tracked := want[key]
		if !tracked {
			continue
		}
		var labels []string
		for _, n := range layer["nodes"].([]any) {
			labels = append(labels, n.(map[string]any)["label"].(string))
		}
		if strings.Join(labels, ",") != strings.Join(expect, ",") {
			t.Errorf("%s nodes = %v, want %v", key, labels, expect)
		}
	}
}

func keysOfMap(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ESX rides along on the vCenter node rather than occupying a row, so the base node has
// to say which hosts it runs on.
func TestStackMapAnnotatesVCenterWithHosts(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap")
	base := body["layers"].([]any)[0].(map[string]any)
	nodes := base["nodes"].([]any)
	if len(nodes) == 0 {
		t.Fatal("expected at least one vCenter node")
	}
	first := nodes[0].(map[string]any)

	// The visible annotation is collapsed to host lines, because a vCenter release can
	// run nineteen host patches and listing them all is unreadable.
	if detail, _ := first["detail"].(string); detail != "9.0" {
		t.Errorf("expected the collapsed host line on the vCenter node, got %q", detail)
	}
	// The exact list stays on the node for the hover.
	hosts, _ := first["hosts"].([]any)
	if len(hosts) == 0 || hosts[0].(string) != "9.0.0.0" {
		t.Errorf("expected the exact host releases to remain available, got %v", hosts)
	}
}

// Pinning must narrow the upper layers, and "lit" has to mean a complete stack exists —
// so the pinned node itself is always lit.
func TestStackMapPinNarrows(t *testing.T) {
	h := testServer(t)
	all := get(t, h, "/api/stackmap")
	if _, pinned := all["lit"]; pinned {
		t.Error("nothing should be lit before a pin")
	}

	body := get(t, h, "/api/stackmap?product=vcenter&version=9.0.0.0")
	litAny, ok := body["lit"].([]any)
	if !ok || len(litAny) == 0 {
		t.Fatalf("expected a lit set for a pinned vCenter, got %v", body["lit"])
	}
	lit := map[string]bool{}
	for _, v := range litAny {
		lit[v.(string)] = true
	}
	if !lit["vcenter:9.0.0.0"] {
		t.Error("the pinned node must be lit")
	}
	if body["recommended"] == nil {
		t.Error("expected a recommended stack alongside a pin")
	}

	// Every lit node must belong to a layer that exists.
	for id := range lit {
		product, _, found := strings.Cut(id, ":")
		if !found {
			t.Errorf("malformed lit id %q", id)
			continue
		}
		if _, ok := model.ByKey(product); !ok {
			t.Errorf("lit id %q names an unknown product", id)
		}
		if product == "esx" {
			t.Errorf("ESX is not a layer, so %q should not be lit", id)
		}
	}
}

func TestStackMapRejectsUnknownPin(t *testing.T) {
	rec := httptest.NewRecorder()
	testServer(t).ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/api/stackmap?product=vcenter&version=1.2.3", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for a version that does not exist, got %d", rec.Code)
	}
}

// The map draws connectors between adjacent layers only, and only between lit nodes —
// an edge that skips a layer or lands on a faded node would be a lie about the stack.
func TestStackMapEdges(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap?product=vcenter&version=9.0.0.0")

	lit := map[string]bool{}
	for _, v := range body["lit"].([]any) {
		lit[v.(string)] = true
	}
	edges, ok := body["edges"].([]any)
	if !ok || len(edges) == 0 {
		t.Fatalf("expected edges for a pinned selection, got %v", body["edges"])
	}

	rank := map[string]int{"vcenter": 0, "supervisor": 1, "vks": 2, "vkr": 3}
	for _, raw := range edges {
		e := raw.(map[string]any)
		from, to := e["from"].(string), e["to"].(string)
		if !lit[from] || !lit[to] {
			t.Errorf("edge %s -> %s touches a node that is not lit", from, to)
		}
		fp, _, _ := strings.Cut(from, ":")
		tp, _, _ := strings.Cut(to, ":")
		if rank[tp]-rank[fp] != 1 {
			t.Errorf("edge %s -> %s is not between adjacent layers", from, to)
		}
	}
}

// Nothing pinned means nothing to draw: no lit set, and no edges implying a path.
func TestStackMapHasNoEdgesWithoutAPin(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap")
	if edges, present := body["edges"]; present && edges != nil {
		t.Errorf("expected no edges before a selection, got %v", edges)
	}
}

// A node's build count has to describe what works with the current selection. Reporting
// the whole line would claim support the matrix does not give: a Supervisor line can
// hold several builds while a given vCenter patch takes only some of them.
func TestStackMapNarrowsBuildCountsToThePin(t *testing.T) {
	h := testServer(t)

	unpinned := get(t, h, "/api/stackmap")
	supLine := func(body map[string]any) map[string]any {
		for _, l := range body["layers"].([]any) {
			layer := l.(map[string]any)
			if layer["key"] != "supervisor" {
				continue
			}
			for _, n := range layer["nodes"].([]any) {
				node := n.(map[string]any)
				if node["label"] == "1.33" {
					return node
				}
			}
		}
		t.Fatal("no Supervisor 1.33 node")
		return nil
	}

	before := supLine(unpinned)
	if got := len(before["releases"].([]any)); got != 2 {
		t.Fatalf("fixture should give the 1.33 line 2 builds, got %d", got)
	}

	after := supLine(get(t, h, "/api/stackmap?product=vcenter&version=9.0.0.0"))
	releases := after["releases"].([]any)
	if len(releases) != 1 {
		t.Errorf("with a pin the node should list only the compatible build, got %v", releases)
	}
	// Supervisor entries are labelled with the vSphere release that delivered them.
	if got := releases[0].(string); !strings.HasPrefix(got, "v1.33.0+vmware.1-fips-vsc9.0.0.0") {
		t.Errorf("wrong build survived the pin: %v", got)
	}
	if detail, _ := after["detail"].(string); detail != "1.33.0 · ships with this vCenter" {
		t.Errorf("detail = %q, want the surviving build named", detail)
	}
}
