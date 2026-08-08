package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/version"
)

// The stack map is the main view: every layer of the stack at once, bottom-up, with the
// compatible set for a given selection lit up.
//
// Nodes are version *lines* rather than individual releases, because the raw release
// count is unreadable — except for vCenter, where the patch letter is the real
// compatibility axis (8.0U3 supports Supervisor 1.26-1.28; 8.0U3k supports 1.31-1.33), so
// collapsing those would throw away the answer.
//
// ESX is not a layer. Its release lines are identical to vCenter's and it has no
// published data against VKS or VKr, so it rides along as an annotation on the vCenter
// node rather than a row nobody can branch from.

// lineKey groups a release into the unit shown as one node.
func lineKey(p model.Product, r *graph.Release) string {
	switch p.Key {
	case "vcenter", "esx":
		// Every release stands alone: the patch letter changes what it supports.
		return r.Raw
	case "vks":
		// VKS's own minor line, e.g. "3.6" from "3.6.3+1.35".
		if len(r.Version.Key) > 0 && len(r.Version.Key[0]) >= 2 {
			return fmt.Sprintf("%d.%d", r.Version.Key[0][0], r.Version.Key[0][1])
		}
	default:
		// Supervisor and VKr group by the Kubernetes minor they carry.
		if minor, ok := version.K8sMinor(r.Version, p); ok {
			return fmt.Sprintf("1.%d", minor)
		}
	}
	return r.Raw
}

type mapNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Detail is the secondary line: compatible ESX versions for vCenter, or the number
	// of builds behind a grouped line.
	Detail string `json:"detail,omitempty"`
	// Releases are the exact releases this node stands for.
	Releases []string `json:"releases"`
	// Hosts are the ESX releases a vCenter node can run on. ESX is not a layer of its
	// own: its lines mirror vCenter's and it has no data against VKS or VKr.
	Hosts []string `json:"hosts,omitempty"`
	// NoData marks a release upstream has published nothing for yet — usually one that
	// has not shipped. Distinct from "nothing is compatible".
	NoData bool `json:"noData,omitempty"`
}

type mapLayer struct {
	Key   string    `json:"key"`
	Label string    `json:"label"`
	Nodes []mapNode `json:"nodes"`
}

// handleStackMap returns every layer bottom-up, plus — when something is pinned — the set
// of node ids that can appear in a complete valid stack alongside it.
func (s *Server) handleStackMap(w http.ResponseWriter, r *http.Request) {
	g, err := s.cfg.Load()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}

	// Layers run bottom-up: the base you build on first.
	order := []string{"vcenter", "supervisor", "vks", "vkr"}
	layers := make([]mapLayer, 0, len(order))
	nodeReleases := map[string][]*graph.Release{}

	for _, key := range order {
		p, _ := model.ByKey(key)
		layer := mapLayer{Key: p.Key, Label: p.Label}

		grouped := map[string][]*graph.Release{}
		var lineOrder []string
		// ReleasesOf is ascending; reverse so the newest reads first within a layer.
		rels := g.ReleasesOf(p.ID)
		for i := len(rels) - 1; i >= 0; i-- {
			line := lineKey(p, rels[i])
			if _, seen := grouped[line]; !seen {
				lineOrder = append(lineOrder, line)
			}
			grouped[line] = append(grouped[line], rels[i])
		}

		for _, line := range lineOrder {
			id := p.Key + ":" + line
			members := grouped[line]
			nodeReleases[id] = members

			node := mapNode{ID: id, Label: line}
			for _, rel := range members {
				node.Releases = append(node.Releases, rel.Raw)
			}
			switch {
			case p.Key == "vcenter":
				hosts := hostsFor(g, members)
				node.Hosts = hosts
				// Listing every host patch is unreadable — a vCenter release can run
				// nineteen of them. Collapse to the host lines; the exact list stays
				// available on hover.
				node.Detail = joinLimited(hostLines(g, members), 3)
				node.NoData = len(hosts) == 0 && !hasAnyCompatible(g, members)
			case len(members) > 1:
				node.Detail = fmt.Sprintf("%d builds", len(members))
			}
			layer.Nodes = append(layer.Nodes, node)
		}
		layers = append(layers, layer)
	}

	resp := map[string]any{"layers": layers}

	// A pin narrows every other layer to what can still form a complete stack. This is
	// the same solver the CLI uses, so "lit" means a real stack exists — not merely that
	// a pairwise edge happens to be present.
	pins, err := resolve(g, map[string]string{
		r.URL.Query().Get("product"): r.URL.Query().Get("version"),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(pins) > 0 {
		lit := map[string]bool{}
		for pid, rels := range g.ViableOptions(pins, graph.StackOptions{IncludePatches: true}) {
			p, _ := model.ByID(pid)
			if p.Key == "esx" {
				continue
			}
			for _, rel := range rels {
				lit[p.Key+":"+lineKey(p, rel)] = true
			}
		}
		resp["lit"] = keysOf(lit)
		resp["edges"] = adjacentEdges(g, pins, order, layers, lit, nodeReleases)

		// The exact hosts for the pinned vCenter, so the base node can state them.
		if best, _ := g.Stacks(pins, graph.StackOptions{Limit: 1, IncludePatches: true}); len(best) > 0 {
			recommended := map[string]string{}
			for _, p := range model.Products {
				if rel := best[0].Releases[p.ID]; rel != nil {
					recommended[p.Key] = rel.Raw
				}
			}
			resp["recommended"] = recommended
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// adjacentEdges finds the connections to draw between neighbouring layers.
//
// An edge is only drawn when a complete valid stack exists containing the pin and both
// endpoints — the same standard as a lit node. Checking "some release of A works with
// some release of B" would be cheaper but would draw branches that fall apart the moment
// you follow them.
func adjacentEdges(
	g *graph.Graph,
	pins map[int]*graph.Release,
	order []string,
	layers []mapLayer,
	lit map[string]bool,
	nodeReleases map[string][]*graph.Release,
) []map[string]string {
	byKey := map[string]mapLayer{}
	for _, l := range layers {
		byKey[l.Key] = l
	}

	var out []map[string]string
	probe := graph.StackOptions{Limit: 1, IncludePatches: true}

	for i := 0; i+1 < len(order); i++ {
		lower, upper := byKey[order[i]], byKey[order[i+1]]
		for _, a := range lower.Nodes {
			if !lit[a.ID] {
				continue
			}
			for _, b := range upper.Nodes {
				if !lit[b.ID] {
					continue
				}
				if stackExistsWith(g, pins, nodeReleases[a.ID], nodeReleases[b.ID], probe) {
					out = append(out, map[string]string{"from": a.ID, "to": b.ID})
				}
			}
		}
	}
	return out
}

// stackExistsWith reports whether any release of a and any release of b can appear in a
// complete valid stack together with the pins.
func stackExistsWith(
	g *graph.Graph,
	pins map[int]*graph.Release,
	as, bs []*graph.Release,
	opts graph.StackOptions,
) bool {
	trial := make(map[int]*graph.Release, len(pins)+2)
	for _, a := range as {
		for _, b := range bs {
			clear(trial)
			for k, v := range pins {
				trial[k] = v
			}
			trial[a.ProductID] = a
			trial[b.ProductID] = b
			if stacks, _ := g.Stacks(trial, opts); len(stacks) > 0 {
				return true
			}
		}
	}
	return false
}

// hostsFor lists the ESX releases compatible with any of the given vCenter releases.
// ESX rides along on the vCenter node rather than occupying a layer of its own.
func hostsFor(g *graph.Graph, vcenters []*graph.Release) []string {
	esx, ok := model.ByKey("esx")
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []*graph.Release
	for _, vc := range vcenters {
		for _, e := range g.Compat[vc.ID] {
			peer := g.Releases[e.Peer]
			if peer == nil || peer.ProductID != esx.ID || !graph.Compatible(e.Status) {
				continue
			}
			if !seen[peer.Raw] {
				seen[peer.Raw] = true
				out = append(out, peer)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return version.Compare(out[i].Version, out[j].Version) > 0
	})
	labels := make([]string, 0, len(out))
	for _, r := range out {
		labels = append(labels, r.Raw)
	}
	return labels
}

// hostLines collapses compatible ESX releases to their version lines: "9.1", "9.0",
// "8.0U3". That is the granularity anyone picking a host actually cares about.
func hostLines(g *graph.Graph, vcenters []*graph.Release) []string {
	esx, ok := model.ByKey("esx")
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, vc := range vcenters {
		for _, e := range g.Compat[vc.ID] {
			peer := g.Releases[e.Peer]
			if peer == nil || peer.ProductID != esx.ID || !graph.Compatible(e.Status) {
				continue
			}
			line := vsphereLine(peer)
			if !seen[line] {
				seen[line] = true
				out = append(out, line)
			}
		}
	}
	return out
}

// vsphereLine renders "8.0U3" for the update-form and "9.1" for the dotted form.
func vsphereLine(r *graph.Release) string {
	if len(r.Version.Key) == 0 || len(r.Version.Key[0]) < 4 {
		return r.Raw
	}
	k := r.Version.Key[0]
	if k[3] > 0 {
		return fmt.Sprintf("%d.%dU%d", k[0], k[1], k[3])
	}
	return fmt.Sprintf("%d.%d", k[0], k[1])
}

func joinLimited(items []string, max int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= max {
		return strings.Join(items, " · ")
	}
	return strings.Join(items[:max], " · ") + fmt.Sprintf(" +%d", len(items)-max)
}

func hasAnyCompatible(g *graph.Graph, rels []*graph.Release) bool {
	for _, r := range rels {
		for _, e := range g.Compat[r.ID] {
			if graph.Compatible(e.Status) {
				return true
			}
		}
	}
	return false
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
