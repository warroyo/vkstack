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

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
)

//go:embed all:assets
var assets embed.FS

// Loader rebuilds the graph, so the UI can pick up a refresh without a restart.
type Loader func() (*graph.Graph, error)

// Refresher pulls fresh data from upstream, reporting progress.
type Refresher func(progress func(step, total int, message string)) error

// Server holds the handlers.
type Server struct {
	load    Loader
	refresh Refresher
}

// NewServer returns an http.Handler serving the UI and API.
func NewServer(load Loader, refresh Refresher) (http.Handler, error) {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, fmt.Errorf("locating embedded assets: %w", err)
	}
	s := &Server{load: load, refresh: refresh}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/model", s.handleModel)
	mux.HandleFunc("GET /api/releases", s.handleReleases)
	mux.HandleFunc("POST /api/stack", s.handleStack)
	mux.HandleFunc("POST /api/check", s.handleCheck)
	mux.HandleFunc("POST /api/plan", s.handlePlan)
	mux.HandleFunc("GET /api/graph", s.handleGraph)
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
	g, err := s.load()
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
	writeJSON(w, http.StatusOK, map[string]any{
		"products":  products,
		"pairs":     pairs,
		"fetchedAt": fetched.Format(time.RFC3339),
		"ageHours":  int(time.Since(fetched).Hours()),
	})
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	covered := model.DefaultCoverage
	if g, err := s.load(); err == nil {
		covered = g.Published
	}

	type edgeJSON struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Label     string `json:"label"`
		Prose     string `json:"prose"`
		Published bool   `json:"published"`
	}
	var edges []edgeJSON
	for _, e := range model.Edges {
		from, _ := model.ByKey(e.From)
		to, _ := model.ByKey(e.To)
		edges = append(edges, edgeJSON{
			From: from.Label, To: to.Label, Label: e.Label, Prose: e.Prose,
			Published: covered(from.ID, to.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mermaid":      model.Mermaid(covered),
		"edges":        edges,
		"upgradeOrder": model.UpgradeOrder(),
	})
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
	g, err := s.load()
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
	Pins    map[string]string `json:"pins"`
	Patches bool              `json:"patches"`
	AllHops bool              `json:"allHops"`
	From    map[string]string `json:"from"`
	To      map[string]string `json:"to"`
}

func resolve(g *graph.Graph, in map[string]string) (map[int]*graph.Release, error) {
	out := map[int]*graph.Release{}
	for key, version := range in {
		if version == "" {
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
	g, err := s.load()
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

	opts := graph.StackOptions{Limit: 1, IncludePatches: req.Patches}
	stacks, failure := g.Stacks(pins, opts)

	resp := map[string]any{}
	if len(stacks) == 0 {
		resp["ok"] = false
		if failure != nil {
			if failure.PinConflict {
				resp["error"] = fmt.Sprintf("the pinned %s and %s versions cannot appear in the same stack",
					failure.Against.Label, failure.BlockedProduct.Label)
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
	g, err := s.load()
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

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	g, err := s.load()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	req, err := decode(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	current, err := resolve(g, req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	target, err := resolve(g, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(current) == 0 || len(target) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("both a current and a target stack are required"))
		return
	}

	plan, failure := g.Upgrade(current, target, graph.PlanOptions{
		IncludePatches: req.Patches, AllHops: req.AllHops,
	})
	if plan == nil {
		reached := map[string]string{}
		if failure != nil {
			for pid, rel := range failure.Reached {
				p, _ := model.ByID(pid)
				reached[p.Key] = rel.Raw
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"error":   "no valid upgrade path found",
			"reached": reached,
		})
		return
	}

	type stepJSON struct {
		Product   string            `json:"product"`
		From      string            `json:"from"`
		To        string            `json:"to"`
		Supported bool              `json:"supported"`
		Blocking  string            `json:"blocking,omitempty"`
		State     map[string]string `json:"state"`
	}
	type windowJSON struct {
		Transitional bool       `json:"transitional"`
		Steps        []stepJSON `json:"steps"`
	}

	var windows []windowJSON
	for _, win := range plan.Windows() {
		wj := windowJSON{Transitional: win.Transitional}
		for _, st := range win.Steps {
			sj := stepJSON{
				Product: st.Product.Label, From: st.From.Raw, To: st.To.Raw,
				Supported: st.Supported, Blocking: st.Blocking,
				State: map[string]string{},
			}
			for _, p := range model.Products {
				if rel := st.State[p.ID]; rel != nil {
					sj.State[p.Key] = rel.Raw
				}
			}
			wj.Steps = append(wj.Steps, sj)
		}
		windows = append(windows, wj)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"windows": windows,
		"steps":   len(plan.Steps),
	})
}
