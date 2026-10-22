package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// upgradesFixture is trimmed from a real Supervisor response. Upstream sends status null
// for cells it has no answer for, and status 2 with a footnote for a blocked hop.
const upgradesFixture = `{
  "id": 1378, "name": "VMware vSphere Supervisor",
  "upgradeProducts": [
    {"id": 19476, "version": "v1.28.3+vmware.2-fips.1-vsc0.1.11", "dummy": false, "releases": [
      {"id": 19794, "version": "v1.29.7+vmware.1-fips-vsc0.1.12", "status": 1, "footnotes": "", "dummy": false},
      {"id": 19472, "version": "v1.29.7+vmware.1-fips-vsc0.1.10", "status": 2, "dummy": false,
       "footnotes": "Upgrading VMware vSphere Supervisor from source version v1.28.3+vmware.2-fips.1-vsc0.1.11 to target version v1.29.7+vmware.1-fips-vsc0.1.10 is not supported"},
      {"id": 19470, "version": "v1.28.3+vmware.2-fips.1-vsc0.1.10", "status": null, "footnotes": null, "dummy": false},
      {"id": 99999, "version": "placeholder", "status": 1, "dummy": true},
      {"id": 19476, "version": "v1.28.3+vmware.2-fips.1-vsc0.1.11", "status": 1, "dummy": false}
    ]},
    {"id": 88888, "version": "placeholder", "dummy": true, "releases": [
      {"id": 19794, "version": "v1.29.7+vmware.1-fips-vsc0.1.12", "status": 1, "dummy": false}
    ]}
  ]
}`

func TestUpgradesKeepsOnlyCellsUpstreamAnswered(t *testing.T) {
	site, _ := rotatingSite(t, "k")
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upgrades" || r.Method != http.MethodPost {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, upgradesFixture)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "")
	c.site = site
	edges, err := c.Upgrades(context.Background(), 1378)
	if err != nil {
		t.Fatalf("Upgrades: %v", err)
	}

	if body["product_id"] != float64(1378) {
		t.Errorf("product_id = %v, want 1378", body["product_id"])
	}
	for _, k := range []string{"isHidePatch", "isHideGenSupported", "isHideTechSupported"} {
		if v, ok := body[k]; !ok || v != false {
			t.Errorf("%s = %v (present %v), want false", k, v, ok)
		}
	}

	// The null cell, the dummy target, the dummy source and the self-hop are all dropped.
	if len(edges) != 2 {
		t.Fatalf("edges = %+v, want the supported and the blocked hop only", edges)
	}
	byTo := map[int]UpgradeEdge{}
	for _, e := range edges {
		if e.FromRelease != 19476 {
			t.Errorf("edge from %d, want every edge from the source 19476", e.FromRelease)
		}
		byTo[e.ToRelease] = e
	}
	if e := byTo[19794]; e.Status != 1 || e.Footnotes != "" {
		t.Errorf("supported hop = %+v", e)
	}
	if e := byTo[19472]; e.Status != 2 || e.Footnotes == "" {
		t.Errorf("blocked hop = %+v, want status 2 with its footnote", e)
	}
}
