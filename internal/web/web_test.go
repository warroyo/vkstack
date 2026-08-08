package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

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
	g := testGraph(t)
	h, err := NewServer(
		func() (*graph.Graph, error) { return g, nil },
		func(func(int, int, string)) error { return nil },
	)
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
	if len(body["edges"].([]any)) != len(model.Edges) {
		t.Errorf("expected %d edges in the response", len(model.Edges))
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
