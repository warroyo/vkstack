package cli

// A static build of the web UI.
//
// This is possible only because the map pins one version at a time: the set of questions
// the UI can ask is the set of clickable nodes, which is small enough to answer in full
// ahead of time. Multi-pin solves and `check` are combinatorial and stay on the CLI, the
// HTTP API and MCP — a static site is the human sample, not the whole tool.
//
// The answers come from the same handlers `serve` runs, driven here through an in-memory
// request rather than a socket. Generating them any other way would mean a second
// implementation of every view, free to drift from the one people actually use.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/web"
)

// siteData is one bundle file: every answer the page can give while a particular set of
// optional layers is open.
//
// There is one file per combination — `data.json` for the plain map, `data-nsx.json`,
// `data-avi.json`, `data-nsx-avi.json` — and the page fetches only the one it needs.
//
// Opening a layer changes the whole answer, not a corner of it: which nodes are lit, how
// each surviving node is narrowed, the edges, the recommendation. So the four states
// cannot share storage. An earlier version stored one full answer plus three small deltas
// on the grounds that `layers` and `lit` were identical across all four. They were — but
// only because three of the server's five solve sites were dropping the include set. The
// invariant was a bug wearing an optimisation's clothes, and it is not coming back.
//
// Splitting by file rather than shipping all four in one is what keeps that honest and
// cheap: `layers` is three quarters of the bytes, and most readers never open either
// optional layer, so they should never download or parse the other three states.
type siteData struct {
	// Meta and StackMap are written only into the base file; the variant files carry
	// nothing the page needs before a layer is opened.
	Meta json.RawMessage `json:"meta,omitempty"`
	// StackMap is the unpinned map: what the page shows before anything is selected.
	StackMap json.RawMessage `json:"stackmap,omitempty"`
	// StackMaps holds every clickable node's answer for this file's set of open layers,
	// keyed product -> version. The unpinned map is stored under the empty product and
	// version, so opening a layer before selecting anything is answerable too.
	StackMaps map[string]map[string]json.RawMessage `json:"stackmaps"`
}

// bundleName is the file one set of open optional layers is written to.
//
// The `with` value is already canonicalised in model order by the client, so the mapping
// is stable: "" -> data.json, "nsx,avi" -> data-nsx-avi.json. Commas become dashes
// because a query-string separator has no business in a filename on a static host.
func bundleName(with string) string {
	if with == "" {
		return "data.json"
	}
	return "data-" + strings.ReplaceAll(with, ",", "-") + ".json"
}

// optionalSubsets returns every combination of optional layers the page can be in, in a
// stable order, canonicalised the same way the client builds the `with` parameter.
//
// Each optional product is opened on its own, so the page has 2^n states rather than n+1.
// With two optional products that is four, which is what makes enumerating them viable.
func optionalSubsets() []string {
	keys := optionalKeys()
	out := []string{""}
	for i := 1; i < 1<<len(keys); i++ {
		var chosen []string
		for bit, key := range keys {
			if i&(1<<bit) != 0 {
				chosen = append(chosen, key)
			}
		}
		out = append(out, strings.Join(chosen, ","))
	}
	return out
}

func newStaticCmd() *cobra.Command {
	var outDir string

	cmd := &cobra.Command{
		Use:   "static",
		Short: "Generate a static copy of the web UI",
		Long: strings.TrimSpace(`
Generate a self-contained copy of the web UI that needs no server.

Every answer the map can give is produced here by the same handlers that answer over HTTP
in ` + "`vkstack serve`" + `, so a static site and a hosted one cannot disagree. They are
written into a single bundle the page loads on start.

The result is plain files. Publish the output directory to any static host.

This covers the UI's questions, not the tool's. The map pins one version at a time, so
its answers can be enumerated; multi-pin solves and ` + "`check`" + ` cannot, and remain on
the CLI, the HTTP API and MCP.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openCache()
			if err != nil {
				return err
			}
			defer db.Close()

			load := newCachedLoader(db)
			// Fail here rather than writing a site whose every answer is an error page.
			if _, err := load.get(); err != nil {
				return err
			}

			handler, err := web.NewServer(web.Config{
				Load:     load.get,
				ReadOnly: true,
				Refresh: func(func(int, int, string)) error {
					return errors.New("a static build cannot refresh; rebuild it instead")
				},
				// Stamped into the page: a snapshot with no build behind it cannot be
				// traced back to the code that produced it.
				Version: cmd.Root().Version,
			})
			if err != nil {
				return err
			}

			bundles, err := generate(handler)
			if err != nil {
				return err
			}
			if err := writeSite(outDir, bundles); err != nil {
				return err
			}

			nodes := 0
			for product, versions := range bundles[""].StackMaps {
				if product != "" {
					nodes += len(versions)
				}
			}
			names := make([]string, 0, len(bundles))
			for _, with := range optionalSubsets() {
				names = append(names, bundleName(with))
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"wrote %s: %d pinned views across %d bundles (%s)\n",
				outDir, nodes, len(bundles), strings.Join(names, ", "))
			return nil
		},
	}

	cmd.Flags().StringVar(&outDir, "out", "dist", "directory to write the site into")
	return cmd
}

// ask drives one GET through the handler and returns the response body.
func ask(handler http.Handler, path string) (json.RawMessage, error) {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d: %s", path, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	// The recorder's buffer is reused, so the bytes have to be copied out.
	return json.RawMessage(append([]byte(nil), rec.Body.Bytes()...)), nil
}

// generate produces one bundle per combination of open optional layers, keyed by the
// `with` value the client sends.
func generate(handler http.Handler) (map[string]*siteData, error) {
	subsets := optionalSubsets()
	bundles := make(map[string]*siteData, len(subsets))
	for _, with := range subsets {
		bundles[with] = &siteData{StackMaps: map[string]map[string]json.RawMessage{}}
	}
	base := bundles[""]

	var err error
	if base.Meta, err = ask(handler, "/api/meta"); err != nil {
		return nil, err
	}
	if base.StackMap, err = ask(handler, "/api/stackmap"); err != nil {
		return nil, err
	}

	put := func(product, version, with string, body json.RawMessage) {
		maps := bundles[with].StackMaps
		if maps[product] == nil {
			maps[product] = map[string]json.RawMessage{}
		}
		maps[product][version] = body
	}

	// answer fetches one node's view once per combination of open optional layers.
	answer := func(product, version string, base url.Values) error {
		for _, with := range subsets {
			q := url.Values{}
			for k, v := range base {
				q[k] = v
			}
			if with != "" {
				q.Set("with", with)
			}
			path := "/api/stackmap"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			body, err := ask(handler, path)
			if err != nil {
				return err
			}
			put(product, version, with, body)
		}
		return nil
	}

	// The unpinned map, under the empty product and version. Opening a layer before
	// selecting anything is an ordinary thing to do.
	if err := answer("", "", url.Values{}); err != nil {
		return nil, err
	}

	// The unpinned map already lists every node the page can offer, and each node carries
	// the release a click on it pins. Enumerating from the response rather than from the
	// graph means this asks for exactly what the UI would ask for, no more and no less.
	var layout struct {
		Layers []struct {
			Key   string `json:"key"`
			Nodes []struct {
				Label string `json:"label"`
				Pin   string `json:"pin"`
			} `json:"nodes"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(base.StackMap, &layout); err != nil {
		return nil, fmt.Errorf("reading the unpinned map: %w", err)
	}

	for _, layer := range layout.Layers {
		for _, node := range layer.Nodes {
			// Mirrors the client's pinVersionFor: an explicit pin when the server sent
			// one, the label otherwise.
			version := node.Pin
			if version == "" {
				version = node.Label
			}
			if _, seen := base.StackMaps[layer.Key][version]; seen {
				continue
			}

			if err := answer(layer.Key, version,
				url.Values{"product": {layer.Key}, "version": {version}}); err != nil {
				return nil, err
			}
		}
	}

	return bundles, nil
}

// writeSite copies the UI onto disk next to the generated bundles.
func writeSite(outDir string, bundles map[string]*siteData) error {
	assets, err := web.AssetsFS()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	err = fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == "." {
			return err
		}
		target := filepath.Join(outDir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		src, err := assets.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		content, err := io.ReadAll(src)
		if err != nil {
			return err
		}
		if path == "index.html" {
			if content, err = markStatic(content); err != nil {
				return err
			}
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		return fmt.Errorf("writing the UI: %w", err)
	}

	for with, data := range bundles {
		body, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("encoding the %q site data: %w", with, err)
		}
		name := bundleName(with)
		if err := os.WriteFile(filepath.Join(outDir, name), body, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// markStatic tells the page it has no server behind it.
//
// A flag set in the document rather than sniffed at run time: a failed request is not a
// reliable signal, and guessing would leave the page silently slow instead of loudly
// wrong when the bundle is missing.
func markStatic(page []byte) ([]byte, error) {
	const anchor = `<script src="app.js"></script>`
	if !strings.Contains(string(page), anchor) {
		return nil, errors.New("index.html no longer loads app.js the way the static build expects")
	}
	return []byte(strings.Replace(string(page), anchor,
		"<script>window.VKSTACK_STATIC = true;</script>\n  "+anchor, 1)), nil
}
