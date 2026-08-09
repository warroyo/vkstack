package web

import (
	"fmt"
	"net/http"
	"regexp"
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

// supervisorTrainRe pulls the "vsc" train out of a Supervisor version, e.g. the "9" in
// "v1.32.9+vmware.2-fips-vsc9.1.0.0200".
var supervisorTrainRe = regexp.MustCompile(`-vsc(\d+)\.`)

// supervisorVSCRe pulls the full delivery version out of a Supervisor release: the
// "9.1.0.0200" in "v1.32.9+vmware.2-fips-vsc9.1.0.0200".
var supervisorVSCRe = regexp.MustCompile(`-vsc([\d.]+)`)

// supervisorVSC names the vSphere release that delivered a Supervisor build.
//
// This is the dimension that explains why one vCenter lists several Supervisor releases
// of the same Kubernetes minor. It is not several patches of one thing: for a given
// vCenter and a given delivery, there is exactly one Kubernetes patch per minor. The
// list is the same minor as shipped by successive vSphere releases, all still supported
// because Supervisor does not have to move when vCenter does.
func supervisorVSC(r *graph.Release) string {
	if m := supervisorVSCRe.FindStringSubmatch(r.Raw); m != nil {
		return strings.TrimRight(m[1], ".")
	}
	return ""
}

// k8sPatch renders the Kubernetes version a Supervisor build carries, e.g. "1.30.14".
func k8sPatch(r *graph.Release) string {
	if len(r.Version.Key) > 0 && len(r.Version.Key[0]) >= 3 {
		k := r.Version.Key[0]
		return fmt.Sprintf("%d.%d.%d", k[0], k[1], k[2])
	}
	return r.Raw
}

// supervisorTrain names the release train a Supervisor build belongs to.
//
// Supervisor ships on two independent trains at the same Kubernetes version: vsc9.x is
// bundled with vCenter 9.x, and vsc0.x is versioned on its own. They are NOT
// interchangeable — Supervisor 1.31 on the vsc9 train does not run on a vCenter 8
// deployment — so grouping purely by Kubernetes minor would merge two incompatible
// things into one node and hide the distinction that matters most.
//
// Which train a vCenter accepts is left entirely to the matrix: vCenter 9.1.0.0300, for
// one, accepts both.
func supervisorTrain(r *graph.Release) string {
	if m := supervisorTrainRe.FindStringSubmatch(r.Raw); m != nil {
		return "vsc" + m[1]
	}
	return ""
}

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
	case "supervisor":
		// Kubernetes minor *and* train: the two trains are not interchangeable.
		if minor, ok := version.K8sMinor(r.Version, p); ok {
			if train := supervisorTrain(r); train != "" {
				return fmt.Sprintf("1.%d %s", minor, train)
			}
			return fmt.Sprintf("1.%d", minor)
		}
	default:
		// VKr groups by the Kubernetes minor it carries.
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
	// Train is the release train a node belongs to, where a product ships more than
	// one at the same version. Supervisor has two and they are not interchangeable.
	Train string `json:"train,omitempty"`
	// Phase is where this node sits in the support lifecycle: the best phase across the
	// releases it covers, since a line stays usable while any release in it is still in
	// General Support.
	Phase string `json:"phase,omitempty"`
	// PhaseLabel is Phase rendered for display.
	PhaseLabel string `json:"phaseLabel,omitempty"`
	// Legacy marks a node with nothing left in General Support — what the interop
	// site's "hide legacy releases" checkbox removes.
	Legacy bool `json:"legacy,omitempty"`
	// Incomplete marks a node the matrix calls compatible with the selection, but which
	// cannot form a whole stack with it — no intermediate version bridges the two.
	Incomplete bool `json:"incomplete,omitempty"`
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
			setPhase(&node, members)
			if p.Key == "supervisor" {
				if train := supervisorTrain(members[0]); train != "" {
					node.Label = strings.TrimSuffix(line, " "+train)
					node.Train = train
				}
			}
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
		// Two different questions, and the matrix answers the first one.
		//
		//   1. Is this compatible with what I picked?  The matrix says so directly, and
		//      that answer is authoritative — it is never overridden here.
		//   2. Can I build a whole stack around both?  Derived, and a strictly stronger
		//      claim: vCenter 9.0.0.0 and VKS 3.7 are listed compatible, but no
		//      Supervisor bridges them.
		//
		// Everything the matrix calls compatible stays lit. Where a complete stack does
		// not exist, the node is flagged rather than dropped.
		stackable := map[string][]*graph.Release{}
		for pid, rels := range g.ViableOptions(pins, graph.StackOptions{IncludePatches: true}) {
			p, _ := model.ByID(pid)
			if p.Key == "esx" {
				continue
			}
			for _, rel := range rels {
				stackable[p.Key+":"+lineKey(p, rel)] = append(
					stackable[p.Key+":"+lineKey(p, rel)], rel)
			}
		}

		lit := map[string]bool{}
		viable := map[string][]*graph.Release{}
		incomplete := map[string]bool{}
		for _, p := range model.Products {
			if p.Key == "esx" {
				continue
			}
			if pinned, isPin := pins[p.ID]; isPin {
				id := p.Key + ":" + lineKey(p, pinned)
				lit[id] = true
				viable[id] = []*graph.Release{pinned}
				continue
			}
			for _, rel := range g.ReleasesOf(p.ID) {
				id := p.Key + ":" + lineKey(p, rel)
				if !compatibleWithAllPins(g, rel, pins) {
					continue
				}
				lit[id] = true
				viable[id] = append(viable[id], rel)
			}
			// A line is only "no complete stack" when none of its releases can form one.
			for id := range lit {
				if strings.HasPrefix(id, p.Key+":") && len(stackable[id]) == 0 {
					incomplete[id] = true
				}
			}
		}

		resp["lit"] = keysOf(lit)
		if len(incomplete) > 0 {
			resp["incomplete"] = keysOf(incomplete)
		}
		pinnedVCenter := ""
		if vc, ok := model.ByKey("vcenter"); ok {
			if rel, pinned := pins[vc.ID]; pinned {
				pinnedVCenter = rel.Raw
			}
		}
		narrowNodes(layers, viable, pinnedVCenter)
		// Connectors show buildable paths, so they use the stronger test. A node the
		// matrix calls compatible but that cannot complete a stack is lit with no
		// connector, which is exactly what it is.
		stackLit := map[string]bool{}
		for id := range stackable {
			stackLit[id] = true
		}
		for _, p := range model.Products {
			if pinned, isPin := pins[p.ID]; isPin {
				stackLit[p.Key+":"+lineKey(p, pinned)] = true
			}
		}
		resp["edges"] = adjacentEdges(g, pins, order, layers, stackLit, nodeReleases)

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

// compatibleWithAllPins reports whether the matrix lists a release as compatible with
// every pinned release. Pairs upstream does not publish cannot rule anything out, so
// they are skipped and reported separately as inferred.
func compatibleWithAllPins(g *graph.Graph, r *graph.Release, pins map[int]*graph.Release) bool {
	for pid, pinned := range pins {
		if pid == r.ProductID {
			continue
		}
		if !g.Published(r.ProductID, pid) {
			continue
		}
		status, ok := g.Status(r.ID, pinned.ID)
		if !ok || !graph.Compatible(status) {
			return false
		}
	}
	return true
}

// narrowNodes rewrites each lit node to hold only the releases that work with the
// current pin, so a build count never claims more support than the matrix gives.
func narrowNodes(layers []mapLayer, viable map[string][]*graph.Release, pinnedVCenter string) {
	for li := range layers {
		for ni := range layers[li].Nodes {
			node := &layers[li].Nodes[ni]
			rels, ok := viable[node.ID]
			if !ok {
				continue // not lit; leave the unfiltered view for context
			}
			total := len(node.Releases)
			node.Releases = node.Releases[:0]
			for _, r := range rels {
				node.Releases = append(node.Releases, r.Raw)
			}
			// The surviving releases may sit in a different phase from the full line.
			setPhase(node, rels)
			switch {
			case layers[li].Key == "vcenter":
				// vCenter nodes are single releases and keep their ESX annotation.
			case layers[li].Key == "supervisor":
				node.Detail, node.Releases = describeSupervisor(rels, pinnedVCenter)
			case len(node.Releases) < total:
				node.Detail = fmt.Sprintf("%d of %d versions", len(node.Releases), total)
			case len(node.Releases) > 1:
				node.Detail = fmt.Sprintf("%d versions", len(node.Releases))
			default:
				node.Detail = ""
			}
		}
	}
}

// describeSupervisor explains a Supervisor node in the terms that actually apply.
//
// Supervisor releases out of band from vCenter, so one vCenter can support several
// Supervisor versions of the same Kubernetes minor. They differ along two axes: the
// Kubernetes patch, and the vSphere release that delivered them. Both vary — vCenter
// 9.1.0.0 takes Supervisor 1.30 at patches 1.30.5, 1.30.10 and 1.30.14.
//
// Deliveries are not necessarily older than the selected vCenter either: vCenter 9.0.2.0
// supports Supervisor delivered by 9.0.2.0100, a later vCenter patch. So entries are
// described by which release delivered them and nothing is claimed about their age. The
// one matching the selected vCenter's own delivery leads, since that is what it ships.
func describeSupervisor(rels []*graph.Release, pinnedVCenter string) (string, []string) {
	type entry struct {
		release *graph.Release
		vsc     string
		ships   bool
	}
	entries := make([]entry, 0, len(rels))
	shipped := -1
	for _, r := range rels {
		vsc := supervisorVSC(r)
		e := entry{release: r, vsc: vsc, ships: pinnedVCenter != "" && vsc == pinnedVCenter}
		if e.ships {
			shipped = len(entries)
		}
		entries = append(entries, e)
	}
	if shipped > 0 {
		entries[0], entries[shipped] = entries[shipped], entries[0]
	}

	labels := make([]string, 0, len(entries))
	for _, e := range entries {
		switch {
		case e.ships:
			labels = append(labels, fmt.Sprintf("%s — ships with vCenter %s", e.release.Raw, e.vsc))
		case e.vsc != "":
			labels = append(labels, fmt.Sprintf("%s — delivered by %s", e.release.Raw, e.vsc))
		default:
			labels = append(labels, e.release.Raw)
		}
		if p := e.release.Phase(); !p.Supported() {
			labels[len(labels)-1] += " · " + p.Label()
		}
	}

	// Where every delivery carries the same Kubernetes patch, say the patch once. It is
	// one Supervisor version shipped in several bundles, not a choice between patches,
	// and reading it as the latter is the easiest mistake to make here.
	onePatch := ""
	for i, e := range entries {
		p := k8sPatch(e.release)
		if i == 0 {
			onePatch = p
		} else if p != onePatch {
			onePatch = ""
			break
		}
	}

	var detail string
	switch {
	case len(entries) == 0:
		detail = ""
	case len(entries) == 1:
		detail = k8sPatch(entries[0].release)
		if entries[0].ships {
			detail += " · ships with this vCenter"
		}
	case entries[0].ships:
		detail = fmt.Sprintf("%s · ships with this vCenter, %d more supported",
			k8sPatch(entries[0].release), len(entries)-1)
	case onePatch != "":
		detail = fmt.Sprintf("%s · in %d releases", onePatch, len(entries))
	default:
		// Genuinely different Kubernetes patches, because Supervisor ships out of band.
		detail = fmt.Sprintf("%d versions · newest %s", len(entries), k8sPatch(entries[0].release))
	}
	return detail, labels
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

// setPhase records the support lifecycle of a node from the releases behind it.
//
// The matrix publishes this per release as two flags, and the interop site's "hide
// legacy releases" checkbox is exactly these two — so "legacy" here means the same thing
// it means there: nothing left in General Support.
//
// A node takes the best phase among its releases: a version line is still generally
// supported while any release in it is.
func setPhase(node *mapNode, members []*graph.Release) {
	best := graph.PhaseEndOfSupport
	for _, r := range members {
		switch r.Phase() {
		case graph.PhaseGeneral:
			best = graph.PhaseGeneral
		case graph.PhaseTechnicalGuidance:
			if best != graph.PhaseGeneral {
				best = graph.PhaseTechnicalGuidance
			}
		}
	}
	node.Phase = string(best)
	node.PhaseLabel = best.Label()
	node.Legacy = !best.Supported()
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
