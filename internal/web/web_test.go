package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/store"
)

const (
	esx = 1
	vc  = 2
	sup = 1378
	vks = 1794
	vkr = 820
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
	// Deliberately awkward: "+" and a parenthetical, both of which have bitten before.
	add(31, sup, "v1.33.0+vmware.1-fips-vsc9.0.0.0")
	add(41, vks, "3.7.0+v1.36")
	add(51, vkr, "1.36.1 (TKr 1.36.1 for vSphere 9.x)")

	for _, pr := range model.Pairs() {
		count := 1
		switch {
		case pr[0] == esx && pr[1] == vks,
			pr[0] == esx && pr[1] == vkr,
			pr[0] == vkr && pr[1] == sup,
			pr[0] == sup && pr[1] == vkr:
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
	for _, path := range []string{"/", "/app.js", "/style.css", "/vendor/mermaid.min.js"} {
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

func TestModelDiagramIsWellFormed(t *testing.T) {
	body := get(t, testServer(t), "/api/model")
	mermaidWellFormed(t, body["mermaid"].(string))
	// The screen carries the conceptual backbone only. The direct vCenter-to-VKS and
	// vCenter-to-VKr edges describe how the matrix is laid out, not how the components
	// relate, so they belong in the doc rather than the picture.
	want := 0
	for _, e := range model.Edges {
		if model.Backbone(e) {
			want++
		}
	}
	got := len(body["edges"].([]any))
	if got != want {
		t.Errorf("got %d edges, want the %d backbone edges", got, want)
	}
	if got >= len(model.Edges) {
		t.Error("expected the on-screen explainer to be a subset of the full model")
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
	if len(body["inferred"].([]any)) != 3 {
		t.Errorf("expected 3 inferred pairs, got %v", body["inferred"])
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
	if states["unverified"] != 3 {
		t.Errorf("expected 3 unverified pairs, got %d (%v)", states["unverified"], states)
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
// not be a layer at all.
func TestStackMapLayers(t *testing.T) {
	body := get(t, testServer(t), "/api/stackmap")
	layers := body["layers"].([]any)

	var keys []string
	for _, l := range layers {
		keys = append(keys, l.(map[string]any)["key"].(string))
	}
	want := []string{"vcenter", "supervisor", "vks", "vkr"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("layers = %v, want %v (bottom-up, no ESX layer)", keys, want)
	}
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
