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

// siteData is everything the page needs, in one file.
//
// One bundle rather than a file per answer: it makes selecting a version instant instead
// of a round trip, and it keeps versions out of file names, where upstream's `+` and `.`
// would be at the mercy of whatever the static host does to a path.
type siteData struct {
	Meta json.RawMessage `json:"meta"`
	// StackMap is the unpinned map: what the page shows before anything is selected.
	StackMap json.RawMessage `json:"stackmap"`
	// StackMaps is the pinned map for every clickable node, keyed product -> version.
	StackMaps map[string]map[string]json.RawMessage `json:"stackmaps"`
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

			data, err := generate(handler)
			if err != nil {
				return err
			}
			if err := writeSite(outDir, data); err != nil {
				return err
			}

			count := 0
			for _, versions := range data.StackMaps {
				count += len(versions)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"wrote %s: %d pinned views plus the unpinned map\n", outDir, count)
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

func generate(handler http.Handler) (*siteData, error) {
	data := &siteData{StackMaps: map[string]map[string]json.RawMessage{}}

	var err error
	if data.Meta, err = ask(handler, "/api/meta"); err != nil {
		return nil, err
	}
	if data.StackMap, err = ask(handler, "/api/stackmap"); err != nil {
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
	if err := json.Unmarshal(data.StackMap, &layout); err != nil {
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
			if _, seen := data.StackMaps[layer.Key][version]; seen {
				continue
			}

			q := url.Values{"product": {layer.Key}, "version": {version}}
			body, err := ask(handler, "/api/stackmap?"+q.Encode())
			if err != nil {
				return nil, err
			}
			if data.StackMaps[layer.Key] == nil {
				data.StackMaps[layer.Key] = map[string]json.RawMessage{}
			}
			data.StackMaps[layer.Key][version] = body
		}
	}

	return data, nil
}

// writeSite copies the UI onto disk next to the generated data.
func writeSite(outDir string, data *siteData) error {
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

	bundle, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encoding the site data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "data.json"), bundle, 0o644); err != nil {
		return fmt.Errorf("writing the site data: %w", err)
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
