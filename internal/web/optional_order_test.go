package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/warroyo/vkstack/internal/model"
)

// TestClientOptionalOrderMatchesModel guards app.js's OPTIONAL_ORDER against the model's
// own optional-product order.
//
// The two must agree exactly. The client filters state.include through OPTIONAL_ORDER to
// build the `with` parameter, which is both the server's opt-in signal and a static
// bundle's cache key. An optional product missing from the array can never be opened —
// its layer renders but its connectors never draw, because layerPairs only draws an
// optional layer that is included. That is precisely how TMC-SM shipped invisible once,
// so it earns a test: add an optional product to the model without adding it here and
// this fails.
func TestClientOptionalOrderMatchesModel(t *testing.T) {
	assets, err := AssetsFS()
	if err != nil {
		t.Fatalf("AssetsFS: %v", err)
	}
	src, err := fs.ReadFile(assets, "app.js")
	if err != nil {
		t.Fatalf("reading app.js: %v", err)
	}

	m := regexp.MustCompile(`OPTIONAL_ORDER\s*=\s*\[([^\]]*)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find OPTIONAL_ORDER in app.js")
	}
	var client []string
	for _, raw := range strings.Split(string(m[1]), ",") {
		if k := strings.Trim(strings.TrimSpace(raw), `"'`); k != "" {
			client = append(client, k)
		}
	}

	var want []string
	for _, p := range model.Products {
		if p.Optional {
			want = append(want, p.Key)
		}
	}

	if strings.Join(client, ",") != strings.Join(want, ",") {
		t.Errorf("app.js OPTIONAL_ORDER = %v, model optional keys = %v — keep them in step",
			client, want)
	}
}
