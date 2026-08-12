// Package web serves the local UI and the JSON API behind it. Everything is embedded in
// the binary and bound to localhost, so the tool works offline and nothing leaves the
// machine.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
)

//go:embed all:assets
var assets embed.FS

// AssetsFS returns the UI files as they are served, so a static build can write the same
// bytes to disk rather than keeping a second copy of them somewhere else.
func AssetsFS() (fs.FS, error) {
	return fs.Sub(assets, "assets")
}

// Loader rebuilds the graph, so the UI can pick up a refresh without a restart.
//
// The generation is a parameter because it is a load-time filter: a request for vSphere 9
// is a different graph, not a different view of one. Zero means every generation.
type Loader func(generation int) (*graph.Graph, error)

// Refresher pulls fresh data from upstream, reporting progress.
type Refresher func(progress func(step, total int, message string)) error

// Config describes how this instance should behave.
type Config struct {
	Load    Loader
	Refresh Refresher
	// ReadOnly rejects client-triggered refreshes. Used for a shared instance, where
	// visitors should not be able to make the server call upstream.
	ReadOnly bool
	// RefreshInterval is the server's own refresh cadence, reported to the UI so it can
	// say when the data next updates. Zero means refreshes are on demand only.
	RefreshInterval time.Duration
	// Ledger records scheduled refresh attempts, so the UI can say whether the data is
	// still moving rather than only how old it is. Optional.
	Ledger *Ledger
	// Version is the build serving the page. A static site is a snapshot of one build,
	// so this is the only thing that says which one produced what you are reading.
	Version string
}

// Server holds the handlers.
type Server struct {
	cfg Config
}

// NewServer returns an http.Handler serving the UI and API.
func NewServer(cfg Config) (http.Handler, error) {
	if cfg.Load == nil {
		return nil, fmt.Errorf("a graph loader is required")
	}
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, fmt.Errorf("locating embedded assets: %w", err)
	}
	s := &Server{cfg: cfg}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/releases", s.handleReleases)
	mux.HandleFunc("POST /api/stack", s.handleStack)
	mux.HandleFunc("POST /api/check", s.handleCheck)
	mux.HandleFunc("GET /api/graph", s.handleGraph)
	mux.HandleFunc("GET /api/stackmap", s.handleStackMap)
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)

	static, err := staticHandler(sub)
	if err != nil {
		return nil, err
	}
	mux.Handle("/", static)
	return mux, nil
}

// staticHandler serves the embedded UI with a content validator on every file.
//
// Files in an embed.FS all report a zero modification time, so http.FileServer sends
// neither Last-Modified nor ETag and a browser has no way to tell a stylesheet that
// changed from the one it already has — a UI fix can ship and still not be visible on a
// reload. Hashing the bytes once at startup gives it something to revalidate against.
//
// The assets are served no-cache, which asks for revalidation rather than forbidding
// storage: a reload costs a conditional request that usually answers 304, and a genuinely
// changed file is picked up straight away. That is the right trade for filenames that
// never change — there is no hashed asset name here to cache-bust with.
func staticHandler(sub fs.FS) (http.Handler, error) {
	etags := map[string]string{}
	err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		// Half the digest is plenty to tell two versions of a file apart, and it keeps
		// the header short.
		etags["/"+p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hashing embedded assets: %w", err)
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasSuffix(p, "/") {
			p += "index.html"
		}
		// Setting the header before delegating is what arms the 304: http.ServeContent
		// compares If-None-Match against whatever ETag is already on the response.
		if tag, ok := etags[p]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	}), nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// productJSON describes one product to the UI.
type productJSON struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Name  string `json:"name"`
	Floor string `json:"floor,omitempty"`
	Count int    `json:"count"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	// Meta carries per-product release counts, so it answers for the active generation
	// rather than always for the whole cache — otherwise the manifest would contradict
	// the map sitting next to it.
	g, err := s.cfg.Load(generationFromQuery(r.URL.Query().Get("gen")))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}

	products := make([]productJSON, 0, len(model.Products))
	for _, p := range model.Products {
		products = append(products, productJSON{
			Key: p.Key, Label: p.Label, Name: p.Name,
			Floor: p.MinVersion, Count: len(g.ReleasesOf(p.ID)),
		})
	}

	type pairJSON struct {
		A         string `json:"a"`
		B         string `json:"b"`
		Published bool   `json:"published"`
	}
	var pairs []pairJSON
	for _, pr := range model.Pairs() {
		a, _ := model.ByID(pr[0])
		b, _ := model.ByID(pr[1])
		pairs = append(pairs, pairJSON{A: a.Key, B: b.Key, Published: g.Published(pr[0], pr[1])})
	}

	fetched := time.UnixMilli(g.FetchedAt)
	resp := map[string]any{
		"products": products,
		"pairs":    pairs,
		// The generations the filter offers, so the client renders tabs from the server's
		// list rather than hardcoding a second copy that can drift.
		"generations": model.Generations,
		"fetchedAt":   fetched.Format(time.RFC3339),
		"ageHours":    int(time.Since(fetched).Hours()),
		"readOnly":    s.cfg.ReadOnly,
		"version":     s.cfg.Version,
	}
	if s.cfg.RefreshInterval > 0 {
		resp["refreshInterval"] = s.cfg.RefreshInterval.String()
	}
	if ledger := s.cfg.Ledger.Snapshot(); ledger != nil {
		resp["refresh"] = ledger
	}
	writeJSON(w, http.StatusOK, resp)
}

type releaseJSON struct {
	Version string `json:"version"`
	Type    string `json:"type"`
	GADate  string `json:"gaDate,omitempty"`
}

func toReleaseJSON(rs []*graph.Release) []releaseJSON {
	out := make([]releaseJSON, 0, len(rs))
	for i := len(rs) - 1; i >= 0; i-- { // newest first
		r := rs[i]
		j := releaseJSON{Version: r.Raw, Type: r.ReleaseType}
		if r.GADate > 0 {
			j.GADate = time.UnixMilli(r.GADate).UTC().Format("2006-01-02")
		}
		out = append(out, j)
	}
	return out
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	g, err := s.cfg.Load(generationFromQuery(r.URL.Query().Get("gen")))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	key := r.URL.Query().Get("product")
	p, ok := model.ByKey(key)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown product %q", key))
		return
	}
	writeJSON(w, http.StatusOK, toReleaseJSON(g.ReleasesOf(p.ID)))
}

// pinsRequest is the shared request shape: product short key -> version string.
type pinsRequest struct {
	Pins        map[string]string `json:"pins"`
	HidePatches bool              `json:"hidePatches"`
	// Include opts optional products (NSX, Avi) into the solve, by product key. Each is
	// independent; a pin on one is already an opt-in for that one alone.
	Include []string `json:"include"`
	// Generation restricts the solve to one vSphere platform generation by vCenter major.
	// Zero, and anything the model does not know, means every generation.
	Generation int `json:"generation"`
}

// generation is the request's generation, ignoring one the model does not offer. A body
// is less likely than a query string to be stale, but the failure mode is the same and
// silently widening beats an error page.
func (r pinsRequest) generation() int {
	if !model.KnownGeneration(r.Generation) {
		return 0
	}
	return r.Generation
}

// include returns the requested optional product keys, dropping anything that is not an
// optional product. The browser is not a trusted caller, and a stray key here should
// widen nothing rather than fail the request.
func (r pinsRequest) include() []string {
	var out []string
	for _, key := range r.Include {
		if p, ok := model.ByKey(key); ok && p.Optional {
			out = append(out, p.Key)
		}
	}
	return out
}

func resolve(g *graph.Graph, in map[string]string) (map[int]*graph.Release, error) {
	out := map[int]*graph.Release{}
	for key, version := range in {
		if key == "" || version == "" {
			continue
		}
		r, err := g.Resolve(key, version)
		if err != nil {
			return nil, err
		}
		out[r.ProductID] = r
	}
	return out, nil
}

func decode(r *http.Request) (pinsRequest, error) {
	var req pinsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, fmt.Errorf("decoding request: %w", err)
	}
	return req, nil
}

func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	// Decoded before the load, because the body names the generation and the generation
	// decides which graph to build.
	req, err := decode(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.cfg.Load(req.generation())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	pins, err := resolve(g, req.Pins)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	opts := graph.StackOptions{Limit: 1, HidePatches: req.HidePatches, Include: req.include()}
	stacks, failure := g.Stacks(pins, opts)

	resp := map[string]any{}
	if len(stacks) == 0 {
		resp["ok"] = false
		if failure != nil {
			if failure.PinConflict {
				reason := "cannot appear in the same stack"
				if failure.Unlisted {
					reason = "are never listed together by upstream, so they cannot appear in the same stack"
				}
				resp["error"] = fmt.Sprintf("the pinned %s and %s versions %s",
					failure.Against.Label, failure.BlockedProduct.Label, reason)
			} else if failure.Against.Key != "" {
				resp["error"] = fmt.Sprintf("no %s release works with the pinned %s",
					failure.BlockedProduct.Label, failure.Against.Label)
			}
		}
		if resp["error"] == nil {
			resp["error"] = "no valid stack for those selections"
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	best := stacks[0]
	recommended := map[string]string{}
	for _, p := range model.Products {
		if rel := best.Releases[p.ID]; rel != nil {
			recommended[p.Key] = rel.Raw
		}
	}

	// Options drive the narrowing dropdowns: each product's list is exactly the
	// versions that can still form a valid stack given the current pins.
	options := map[string][]string{}
	for pid, rels := range g.ViableOptions(pins, opts) {
		p, _ := model.ByID(pid)
		for _, rel := range rels {
			options[p.Key] = append(options[p.Key], rel.Raw)
		}
	}

	var inferred []string
	for _, v := range best.Inferred() {
		inferred = append(inferred, v.A.Label+" × "+v.B.Label)
	}

	resp["ok"] = true
	resp["recommended"] = recommended
	resp["options"] = options
	resp["inferred"] = inferred
	resp["verdicts"] = verdictsJSON(best.Verdicts)
	writeJSON(w, http.StatusOK, resp)
}

type verdictJSON struct {
	A        string `json:"a"`
	B        string `json:"b"`
	AVersion string `json:"aVersion"`
	BVersion string `json:"bVersion"`
	State    string `json:"state"` // ok | bad | unverified
	Detail   string `json:"detail"`
}

func verdictsJSON(vs []graph.PairVerdict) []verdictJSON {
	out := make([]verdictJSON, 0, len(vs))
	for _, v := range vs {
		j := verdictJSON{A: v.A.Label, B: v.B.Label}
		if v.ARelease != nil {
			j.AVersion = v.ARelease.Raw
		}
		if v.BRelease != nil {
			j.BVersion = v.BRelease.Raw
		}
		switch {
		case !v.Published:
			j.State, j.Detail = "unverified", "not published upstream"
		case !v.HasEdge:
			j.State, j.Detail = "unverified", "these releases were never evaluated together"
		case graph.Compatible(v.Status):
			j.State, j.Detail = "ok", statusWord(v.Status)
		default:
			j.State, j.Detail = "bad", statusWord(v.Status)
		}
		out = append(out, j)
	}
	return out
}

func statusWord(status int) string {
	switch status {
	case 1:
		return "compatible"
	case 2:
		return "incompatible"
	case 3:
		return "compatible (not tested)"
	case 4:
		return "not supported"
	default:
		return fmt.Sprintf("status %d", status)
	}
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	req, err := decode(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	g, err := s.cfg.Load(req.generation())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	pins, err := resolve(g, req.Pins)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res := g.Check(pins)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       res.OK(),
		"verdicts": verdictsJSON(res.Pairs),
	})
}
