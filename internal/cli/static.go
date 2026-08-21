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
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/web"
)

// siteData is one bundle file: every answer the page can give while a particular set of
// optional layers is open, under one platform generation.
//
// There is one file per state — `data.json` for the plain map, then `data-nsx.json`,
// `data-nsx-gen9.json` and so on — and the page fetches only the one it needs. See
// bundleKey for the naming.
//
// Opening a layer changes the whole answer, not a corner of it: which nodes are lit, how
// each surviving node is narrowed, the edges, the recommendation. So the states cannot
// share storage. An earlier version stored one full answer plus three small deltas on the
// grounds that `layers` and `lit` were identical across all four. They were — but only
// because three of the server's five solve sites were dropping the include set. The
// invariant was a bug wearing an optimisation's clothes, and it is not coming back. The
// generation split obeys the same rule for the same reason.
//
// Splitting by file rather than shipping every state in one is what keeps that honest and
// cheap: `layers` is three quarters of the bytes, most readers never open either optional
// layer, and nobody reads two generations at once.
type siteData struct {
	// Meta and StackMap are written only into each generation's base file; the variant
	// files carry nothing the page needs before a layer is opened.
	Meta json.RawMessage `json:"meta,omitempty"`
	// StackMap is the unpinned map: what the page shows before anything is selected.
	StackMap json.RawMessage `json:"stackmap,omitempty"`
	// StackMaps holds every clickable node's answer for this file's set of open layers,
	// keyed product -> version. The unpinned map is stored under the empty product and
	// version, so opening a layer before selecting anything is answerable too.
	StackMaps map[string]map[string]json.RawMessage `json:"stackmaps"`
}

// bundleKey identifies one bundle: a set of open optional layers, under one generation.
//
// Generation is part of the key rather than a filter applied to a shared bundle because
// it changes the answer at load time, not at display time. Under vSphere 9 a Supervisor
// node is narrowed to the releases a vCenter 9 can carry, the recommended stack is a
// different stack, and whole vCenter nodes are gone. See the note above siteData for what
// happened last time two states that differed were made to share storage.
type bundleKey struct {
	With string
	Gen  int
}

// bundleName is the file one bundle is written to.
//
// The `with` value is already canonicalised in model order by the client, so the mapping
// is stable: {"", 0} -> data.json, {"nsx,avi", 9} -> data-nsx-avi-gen9.json. Commas
// become dashes because a query-string separator has no business in a filename on a
// static host.
func bundleName(key bundleKey) string {
	name := "data"
	if key.With != "" {
		name += "-" + strings.ReplaceAll(key.With, ",", "-")
	}
	if key.Gen != 0 {
		name += fmt.Sprintf("-gen%d", key.Gen)
	}
	return name + ".json"
}

// bundleKeys returns every bundle the page can ask for, in a stable order.
//
// That is every set of open optional layers under every generation, plus the unfiltered
// one — four subsets across three generations today, so twelve files. The reader still
// downloads exactly one.
func bundleKeys() []bundleKey {
	gens := append([]int{0}, model.Generations...)
	out := make([]bundleKey, 0, len(gens)*(1<<len(optionalKeys())))
	for _, gen := range gens {
		for _, with := range optionalSubsets() {
			out = append(out, bundleKey{With: with, Gen: gen})
		}
	}
	return out
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
			if _, err := load.get(0); err != nil {
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

			// Counted across every generation, because each enumerates its own node list
			// and the whole point of the split is that they differ.
			nodes := 0
			for _, key := range bundleKeys() {
				if key.With != "" {
					continue // the same nodes, answered with a layer open
				}
				for product, versions := range bundles[key].StackMaps {
					if product != "" {
						nodes += len(versions)
					}
				}
			}
			names := make([]string, 0, len(bundles))
			for _, key := range bundleKeys() {
				names = append(names, bundleName(key))
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

// generate produces one bundle per combination of open optional layers per generation.
func generate(handler http.Handler) (map[bundleKey]*siteData, error) {
	keys := bundleKeys()
	bundles := make(map[bundleKey]*siteData, len(keys))
	for _, key := range keys {
		bundles[key] = &siteData{StackMaps: map[string]map[string]json.RawMessage{}}
	}

	// Each generation is enumerated on its own, because it has its own node list: the
	// vCenter row under vSphere 9 is a different row, not a subset of one view.
	for _, gen := range append([]int{0}, model.Generations...) {
		if err := generateGeneration(handler, bundles, gen); err != nil {
			return nil, err
		}
	}
	return bundles, nil
}

// generateGeneration fills in every bundle belonging to one generation.
func generateGeneration(handler http.Handler, bundles map[bundleKey]*siteData, gen int) error {
	subsets := optionalSubsets()
	base := bundles[bundleKey{Gen: gen}]

	// Query parameters common to every request in this generation.
	genQuery := func() url.Values {
		q := url.Values{}
		if gen != 0 {
			q.Set("gen", strconv.Itoa(gen))
		}
		return q
	}
	withQuery := func(q url.Values) string {
		path := "/api/stackmap"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		return path
	}

	var err error
	// Meta carries per-product release counts, which narrow with the generation, so each
	// generation's base file gets its own rather than inheriting the unfiltered one.
	metaPath := "/api/meta"
	if gen != 0 {
		metaPath += "?" + genQuery().Encode()
	}
	if base.Meta, err = ask(handler, metaPath); err != nil {
		return err
	}
	if base.StackMap, err = ask(handler, withQuery(genQuery())); err != nil {
		return err
	}

	// put records one node's answer. Guarded because the workers below write different
	// slots of these maps concurrently and Go maps are not safe for concurrent writes. The
	// lock covers only the insert, never the solve that produced body, so the expensive
	// work still runs fully in parallel.
	var mu sync.Mutex
	put := func(product, version, with string, body json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		maps := bundles[bundleKey{With: with, Gen: gen}].StackMaps
		if maps[product] == nil {
			maps[product] = map[string]json.RawMessage{}
		}
		maps[product][version] = body
	}

	// answer fetches one node's view once per combination of open optional layers.
	answer := func(product, version string, pin url.Values) error {
		for _, with := range subsets {
			q := genQuery()
			for k, v := range pin {
				q[k] = v
			}
			if with != "" {
				q.Set("with", with)
			}
			body, err := ask(handler, withQuery(q))
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
		return err
	}

	// The unpinned map already lists every node the page can offer, and each node carries
	// every release it groups plus the one a plain click pins. Enumerating from the
	// response rather than from the graph means this asks for exactly what the UI would
	// ask for, no more and no less.
	var layout struct {
		Layers []struct {
			Key   string `json:"key"`
			Nodes []struct {
				Label    string   `json:"label"`
				Pin      string   `json:"pin"`
				Releases []string `json:"releases"`
			} `json:"nodes"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(base.StackMap, &layout); err != nil {
		return fmt.Errorf("reading the unpinned map for generation %d: %w", gen, err)
	}

	// One work item per clickable (layer, version). The list is deduped up front — a
	// grouped node's default pin also appears in its Releases, and two nodes can share a
	// release — so the workers below share no bookkeeping and the result never depends on
	// which one finished first. This replaces the old inline "already in base.StackMaps"
	// check, which was a running dedup only a sequential loop could rely on.
	type work struct{ product, version string }
	var items []work
	seen := map[work]bool{}
	for _, layer := range layout.Layers {
		for _, node := range layer.Nodes {
			// Mirrors the client's pinVersionFor: an explicit pin when the server sent
			// one, the label otherwise.
			version := node.Pin
			if version == "" {
				version = node.Label
			}
			// A grouped node's <select> lets a reader pin any release it stands for, not
			// only the default — see nodeSelect in app.js. Every one of those has to be
			// answerable here too, or picking anything but the default 404s on a static
			// build while working fine against `vkstack serve`.
			versions := node.Releases
			if len(versions) == 0 {
				versions = []string{version}
			}
			for _, v := range versions {
				it := work{layer.Key, v}
				if seen[it] {
					continue
				}
				seen[it] = true
				items = append(items, it)
			}
		}
	}

	// Answer them across GOMAXPROCS workers. Each item is independent read-only work
	// against an immutable graph — the solve touches no shared state and the loader hands
	// out one cached graph per generation — so the only synchronisation needed is the lock
	// inside put. A parallel run and a sequential one produce byte-identical bundles,
	// because json.Marshal sorts map keys and every item writes only its own slot.
	workers := runtime.GOMAXPROCS(0)
	if workers > len(items) {
		workers = len(items)
	}
	if workers < 1 {
		workers = 1
	}

	ch := make(chan work)
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	stop := make(chan struct{}) // closed on the first error, to drain the feed promptly

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				if err := answer(it.product, it.version,
					url.Values{"product": {it.product}, "version": {it.version}}); err != nil {
					errOnce.Do(func() { firstErr = err; close(stop) })
					return
				}
			}
		}()
	}

feed:
	for _, it := range items {
		select {
		case ch <- it:
		case <-stop:
			break feed
		}
	}
	close(ch)
	wg.Wait()
	return firstErr
}

// writeSite copies the UI onto disk next to the generated bundles.
func writeSite(outDir string, bundles map[bundleKey]*siteData) error {
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

	for key, data := range bundles {
		name := bundleName(key)
		body, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("encoding %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(outDir, name), body, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}

	// Cloudflare Pages caches static assets for four hours by default, and this build gives
	// every asset a stable name across deploys — so without an override a publish is invisible
	// to anyone who visited in the last four hours, the JS and CSS pinned to their old copies
	// while a hard reload is the only way through. A _headers file at the site root drops the
	// asset TTL to zero: the browser revalidates each load, and the etag Pages already sends
	// turns the check into a 304 when nothing changed, so the cost is a conditional request,
	// not a re-download. index.html is already served this way; this extends it to the rest.
	//
	// The rules are wildcards on purpose: Pages honoured a `/*.json` pattern but silently
	// ignored exact `/app.js` and `/style.css` paths for files it already serves itself, so
	// the extension globs are what actually take.
	const headers = `# Managed by "vkstack static" — see writeSite in internal/cli/static.go.
/*.js
  Cache-Control: public, max-age=0, must-revalidate
/*.css
  Cache-Control: public, max-age=0, must-revalidate
/*.json
  Cache-Control: public, max-age=0, must-revalidate
`
	if err := os.WriteFile(filepath.Join(outDir, "_headers"), []byte(headers), 0o644); err != nil {
		return fmt.Errorf("writing _headers: %w", err)
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
