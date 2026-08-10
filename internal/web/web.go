// Package web serves the local UI and the JSON API behind it. Everything is embedded in
// the binary and bound to localhost, so the tool works offline and nothing leaves the
// machine.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
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
type Loader func() (*graph.Graph, error)

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
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux, nil
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
	g, err := s.cfg.Load()
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
		"products":  products,
		"pairs":     pairs,
		"fetchedAt": fetched.Format(time.RFC3339),
		"ageHours":  int(time.Since(fetched).Hours()),
		"readOnly":  s.cfg.ReadOnly,
		"version":   s.cfg.Version,
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
	g, err := s.cfg.Load()
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
	g, err := s.cfg.Load()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	req, err := decode(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
	g, err := s.cfg.Load()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	req, err := decode(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
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
