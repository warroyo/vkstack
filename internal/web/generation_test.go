package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mapLayers pulls the layer/node shape out of a stackmap response.
func mapLayers(t *testing.T, handler http.Handler, path string) map[string][]string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: got %d, body %s", path, rec.Code, rec.Body.String())
	}
	var body struct {
		Layers []struct {
			Key   string `json:"key"`
			Nodes []struct {
				Label string `json:"label"`
			} `json:"nodes"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	out := map[string][]string{}
	for _, l := range body.Layers {
		for _, n := range l.Nodes {
			out[l.Key] = append(out[l.Key], n.Label)
		}
	}
	return out
}

// TestStackMapHonoursGeneration checks that `gen` reaches the loader rather than being
// dropped on the floor — the failure that would leave the tabs cosmetic.
func TestStackMapHonoursGeneration(t *testing.T) {
	h := generationServer(t)

	all := mapLayers(t, h, "/api/stackmap")
	if len(all["vcenter"]) != 2 {
		t.Fatalf("unfiltered map: expected both vCenter releases, got %v", all["vcenter"])
	}

	nine := mapLayers(t, h, "/api/stackmap?gen=9")
	if len(nine["vcenter"]) != 1 || nine["vcenter"][0] != "9.0.0.0" {
		t.Errorf("gen=9 vCenter: got %v, want just 9.0.0.0", nine["vcenter"])
	}

	eight := mapLayers(t, h, "/api/stackmap?gen=8")
	if len(eight["vcenter"]) != 1 || eight["vcenter"][0] != "8.0U3" {
		t.Errorf("gen=8 vCenter: got %v, want just 8.0U3", eight["vcenter"])
	}
}

// TestGenerationWidensOnJunk covers the stale-bookmark case. A query string is not a
// promise, so an unusable value shows the whole map instead of an error page.
func TestGenerationWidensOnJunk(t *testing.T) {
	h := generationServer(t)
	want := mapLayers(t, h, "/api/stackmap")

	for _, raw := range []string{"99", "abc", "", "-1", "9.5"} {
		got := mapLayers(t, h, "/api/stackmap?gen="+raw)
		if len(got["vcenter"]) != len(want["vcenter"]) {
			t.Errorf("gen=%q: got %v, want the unfiltered %v",
				raw, got["vcenter"], want["vcenter"])
		}
	}
}

// TestMetaReportsGenerations is what the client builds its tabs from, and what stops a
// second hardcoded copy of the list drifting in the browser.
func TestMetaReportsGenerations(t *testing.T) {
	h := generationServer(t)

	read := func(path string) struct {
		Generations []int `json:"generations"`
		Products    []struct {
			Key   string `json:"key"`
			Count int    `json:"count"`
		} `json:"products"`
	} {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		var body struct {
			Generations []int `json:"generations"`
			Products    []struct {
				Key   string `json:"key"`
				Count int    `json:"count"`
			} `json:"products"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
		return body
	}

	meta := read("/api/meta")
	if len(meta.Generations) == 0 {
		t.Fatal("meta carries no generations; the client has nothing to build tabs from")
	}

	// Counts have to follow the filter, or the manifest contradicts the map beside it.
	countOf := func(products []struct {
		Key   string `json:"key"`
		Count int    `json:"count"`
	}, key string) int {
		for _, p := range products {
			if p.Key == key {
				return p.Count
			}
		}
		return -1
	}
	if all, nine := countOf(meta.Products, "vcenter"),
		countOf(read("/api/meta?gen=9").Products, "vcenter"); nine >= all {
		t.Errorf("vCenter count under gen=9 is %d against %d unfiltered; expected fewer", nine, all)
	}
}
