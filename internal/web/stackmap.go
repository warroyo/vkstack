package web

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
	"github.com/warroyo/vkstack/internal/version"
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
	// Releases are the exact releases this node stands for, verbatim as upstream
	// publishes them. This field is machine-readable identity: it is what the client
	// sends back as a pin and what it matches the recommended stack against, so nothing
	// may ever decorate it. Prose about a release goes in Notes.
	Releases []string `json:"releases"`
	// Pin is the release selected by clicking this node — the newest one it stands for.
	// Sent explicitly so the client never has to derive a pin from a display string.
	Pin string `json:"pin,omitempty"`
	// Notes are the releases rendered for a reader, one line each: where a build came
	// from, whether it is out of General Support. Display only — never sent back, and
	// not necessarily in Releases order, since the build a selection ships with leads.
	Notes []string `json:"notes,omitempty"`
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
	// WorstPhase is the *worst* phase among the releases a node covers. A grouped node
	// keeps its best phase as its headline — the line is still usable — but a green node
	// holding End of Support builds has to say so, or picking from it is a guess.
	WorstPhase string `json:"worstPhase,omitempty"`
	// WorstPhaseLabel is WorstPhase rendered for display.
	WorstPhaseLabel string `json:"worstPhaseLabel,omitempty"`
	// Mixed marks a node whose releases do not all sit in the same support phase.
	Mixed bool `json:"mixed,omitempty"`
	// Legacy marks a node with nothing left in General Support — what the interop
	// site's "hide legacy releases" checkbox removes.
	Legacy bool `json:"legacy,omitempty"`
	// NoData marks a release upstream has published nothing for yet — usually one that
	// has not shipped. Distinct from "nothing is compatible".
	NoData bool `json:"noData,omitempty"`
	// Untested marks a node whose own product is joined to the selection by a result
	// upstream flags "compatible, not tested". The solver counts those as a yes, so
	// without this the picture presents them as though somebody had run them.
	Untested bool `json:"untested,omitempty"`
	// Unverified marks a node whose own product is joined to the rest of the stack by a
	// pair upstream never evaluated. Lit on trust rather than on a lookup.
	Unverified bool `json:"unverified,omitempty"`
	// Provenance spells out, pair by pair, where the warrant for this node is weaker
	// than a tested lookup — including pairs elsewhere in the stack it is lit through,
	// which is why the flags above are not the whole story.
	Provenance []string `json:"provenance,omitempty"`
	// Footnotes are the upstream caveats attached to the result linking this node to the
	// selection. Published by the matrix, and dropped everywhere until now.
	Footnotes []string `json:"footnotes,omitempty"`
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
			node.Notes = phaseNotes(members)
			setPin(&node)
			// Upstream publishes nothing at all for some releases — normally ones that
			// have not shipped. That is not the same as "nothing is compatible", and it
			// happens on every layer, not only on vCenter.
			node.NoData = !hasAnyCompatible(g, members)
			switch {
			case p.Key == "vcenter":
				hosts := hostCandidates(g, members)
				node.Hosts = rawsOf(hosts)
				// Listing every host patch is unreadable — a vCenter release can run
				// nineteen of them. Collapse to the host lines; the exact list stays
				// available on hover.
				node.Detail = joinLimited(linesOf(hosts), 3)
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
		// Lit means a complete valid stack exists containing this and the selection,
		// enforcing only the real dependencies. The matrix also publishes vCenter
		// against VKS and VKr, but those are not dependencies — VKS runs on the
		// Supervisor — so they are looked up, never used to include or exclude.
		lit := map[string]bool{}
		viable := map[string][]*graph.Release{}
		for pid, rels := range g.ViableOptions(pins, graph.StackOptions{IncludePatches: true}) {
			p, _ := model.ByID(pid)
			if p.Key == "esx" {
				continue
			}
			for _, rel := range rels {
				id := p.Key + ":" + lineKey(p, rel)
				lit[id] = true
				viable[id] = append(viable[id], rel)
			}
		}
		resp["lit"] = keysOf(lit)
		pinnedVCenter := ""
		if vc, ok := model.ByKey("vcenter"); ok {
			if rel, pinned := pins[vc.ID]; pinned {
				pinnedVCenter = rel.Raw
			}
		}
		vcenterReleases := map[string]bool{}
		if vc, ok := model.ByKey("vcenter"); ok {
			for _, r := range g.ReleasesOf(vc.ID) {
				vcenterReleases[r.Raw] = true
			}
		}
		narrowNodes(g, layers, pins, viable, pinnedVCenter, vcenterReleases)
		resp["edges"] = adjacentEdges(g, pins, order, layers, lit, nodeReleases)

		// The newest stack the solver can build from here, with the support phase of
		// every release in it. The phase matters because the legacy filter is a view
		// setting: the solver still reaches past it, so a recommendation can name a
		// release the map is hiding, and it has to say so rather than look ordinary.
		if best, _ := g.Stacks(pins, graph.StackOptions{Limit: 1, IncludePatches: true}); len(best) > 0 {
			recommended := map[string]any{}
			for _, p := range model.Products {
				rel := best[0].Releases[p.ID]
				if rel == nil {
					continue
				}
				phase := rel.Phase()
				recommended[p.Key] = map[string]any{
					"version":    rel.Raw,
					"phase":      string(phase),
					"phaseLabel": phase.Label(),
					"legacy":     !phase.Supported(),
				}
			}
			resp["recommended"] = recommended
			resp["provenance"] = summarizeProvenance(best[0])
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// narrowNodes rewrites each lit node to hold only the releases that work with the
// current pin, so a build count never claims more support than the matrix gives.
//
// Releases stays raw throughout: it is the pin token and the key the client matches the
// recommended stack against. Everything written for a reader goes to Notes. Conflating
// the two made every lit Supervisor node unclickable, because the decorated string was
// sent back as a version and no release matched it.
func narrowNodes(
	g *graph.Graph,
	layers []mapLayer,
	pins map[int]*graph.Release,
	viable map[string][]*graph.Release,
	pinnedVCenter string,
	vcenterReleases map[string]bool,
) {
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
			node.Notes = phaseNotes(rels)
			setPin(node)
			setProvenance(g, node, pins, rels)
			switch {
			case layers[li].Key == "vcenter":
				// A vCenter node is one release, and it keeps its ESX annotation — but
				// the hosts have to answer the question being asked. ESX gates the
				// Supervisor as much as vCenter does, so with something pinned above,
				// the hosts that cannot carry it are not hosts for this selection.
				hosts := hostsWithPin(g, pins, rels)
				node.Hosts = rawsOf(hosts)
				node.Detail = joinLimited(linesOf(hosts), 3)
			case layers[li].Key == "supervisor":
				node.Detail, node.Notes = describeSupervisor(rels, pinnedVCenter, vcenterReleases)
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

// setPin records the release a click on this node selects: the newest it stands for,
// which is the first, since every list here is newest-first.
func setPin(node *mapNode) {
	if len(node.Releases) > 0 {
		node.Pin = node.Releases[0]
		return
	}
	node.Pin = ""
}

// phaseNotes renders one line per release, flagging the ones that are out of General
// Support. A grouped node can hold both, and the peek list is where that becomes visible.
func phaseNotes(rels []*graph.Release) []string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		note := r.Raw
		if p := r.Phase(); !p.Supported() {
			note += " · " + p.Label()
		}
		out = append(out, note)
	}
	return out
}

// setProvenance records how well-founded a lit node actually is.
//
// Lit means a complete valid stack exists. It does not mean every pair in that stack was
// tested, or even evaluated: the solver counts "compatible, not tested" as a yes and
// cannot enforce pairs upstream never published. Both are defensible; presenting them as
// a plain yes is not.
//
// A node is flagged only for pairs its own product is party to — a VKS node tagged "not
// tested" because of something two layers down would be read as a claim about VKS. The
// rest of the stack's weak spots are still reported, in words, as belonging to the stack.
// The check runs against the newest stack the solver can build through this node, which
// is the stack a click on it leads to.
func setProvenance(g *graph.Graph, node *mapNode, pins map[int]*graph.Release, rels []*graph.Release) {
	trial := make(map[int]*graph.Release, len(pins)+1)
	for k, v := range pins {
		trial[k] = v
	}
	best := (*graph.Stack)(nil)
	for _, r := range rels {
		trial[r.ProductID] = r
		if stacks, _ := g.Stacks(trial, graph.StackOptions{Limit: 1, IncludePatches: true}); len(stacks) > 0 {
			best = &stacks[0]
			break
		}
	}
	if best == nil {
		return
	}
	productKey := rels[0].ProductKey
	seen := map[string]bool{}
	for _, v := range best.Verdicts {
		if !v.Dependency {
			continue // published for reference, never enforced; not this node's warrant
		}
		own := v.A.Key == productKey || v.B.Key == productKey
		pair := v.A.Label + " × " + v.B.Label
		switch {
		case v.Unverified():
			node.Unverified = node.Unverified || own
			node.Provenance = append(node.Provenance, pair+
				" was never evaluated upstream — that link is inferred")
		case v.Status == store.StatusCompatibleNT:
			node.Untested = node.Untested || own
			node.Provenance = append(node.Provenance, pair+
				" is listed compatible but not tested")
		}
		if v.Footnotes != "" && !seen[v.Footnotes] {
			seen[v.Footnotes] = true
			node.Footnotes = append(node.Footnotes, pair+": "+v.Footnotes)
		}
	}
}

// deliveryPhrase names where a Supervisor build came from, but only when that can be
// checked.
//
// "vsc9.1.0.0200" is literally vCenter 9.1.0.0200, and saying so is verifiable — the
// value is matched against the cached vCenter releases, not inferred from its shape.
// One value in the data, 9.0.0.0100, looks like a vCenter patch and matches none.
//
// The vsc0.x sequence is something else, and what exactly is not established. Rather
// than narrate a guess, nothing is claimed: the release string already carries the tail,
// so the reader sees the identifier without being told a story about it.
func deliveryPhrase(vsc string, vcenterReleases map[string]bool) string {
	if vsc != "" && vcenterReleases[vsc] {
		return "vCenter " + vsc
	}
	return ""
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
func describeSupervisor(rels []*graph.Release, pinnedVCenter string, vcenterReleases map[string]bool) (string, []string) {
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
		phrase := deliveryPhrase(e.vsc, vcenterReleases)
		switch {
		case e.ships:
			labels = append(labels, fmt.Sprintf("%s — ships with %s, your selection", e.release.Raw, phrase))
		case phrase != "":
			labels = append(labels, fmt.Sprintf("%s — from %s", e.release.Raw, phrase))
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
) []mapEdge {
	byKey := map[string]mapLayer{}
	for _, l := range layers {
		byKey[l.Key] = l
	}

	var out []mapEdge
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
				ra, rb, ok := stackExistsWith(g, pins, nodeReleases[a.ID], nodeReleases[b.ID], probe)
				if !ok {
					continue
				}
				out = append(out, describeEdge(g, a.ID, b.ID, ra, rb))
			}
		}
	}
	return out
}

// mapEdge is one connection to draw, with the provenance of the result behind it.
//
// Every edge used to be a bare from/to pair, drawn identically whether the matrix had
// tested the combination, merely called it compatible, or never evaluated these two
// releases at all. The picture cannot claim more than the data, so the difference travels
// with the edge.
type mapEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Untested is set for status 3, "compatible, not tested": upstream's own hedge.
	Untested bool `json:"untested,omitempty"`
	// Unverified is set when the two releases behind this edge were never evaluated
	// against each other — the edge is drawn because a stack exists, not because the
	// pair was looked up.
	Unverified bool `json:"unverified,omitempty"`
	// Footnote is the upstream caveat on the result, verbatim.
	Footnote string `json:"footnote,omitempty"`
	// Releases names the two releases the verdict is about, so a reader can tell which
	// members of two grouped nodes the connection is actually about.
	Releases []string `json:"releases,omitempty"`
}

// describeEdge reads the matrix's own verdict for the pair a connection rests on.
func describeEdge(g *graph.Graph, fromID, toID string, ra, rb *graph.Release) mapEdge {
	e := mapEdge{From: fromID, To: toID, Releases: []string{ra.Raw, rb.Raw}}
	status, ok := g.Status(ra.ID, rb.ID)
	switch {
	case !g.Published(ra.ProductID, rb.ProductID) || !ok:
		// Adjacent layers are all dependency pairs, so a missing edge here means the
		// solver had nothing to enforce and the connection is taken on trust.
		e.Unverified = true
	case status == store.StatusCompatibleNT:
		e.Untested = true
	}
	for _, edge := range g.Compat[ra.ID] {
		if edge.Peer == rb.ID {
			e.Footnote = edge.Footnotes
			break
		}
	}
	return e
}

// summarizeProvenance counts how much of a stack is a lookup and how much is trust.
func summarizeProvenance(s graph.Stack) map[string]any {
	verified, untested := 0, 0
	var inferred, untestedPairs, footnotes []string
	for _, v := range s.Verdicts {
		if !v.Dependency {
			continue // published for reference, never enforced
		}
		name := v.A.Label + " × " + v.B.Label
		switch {
		case v.Unverified():
			inferred = append(inferred, name)
		case v.Status == store.StatusCompatibleNT:
			untested++
			untestedPairs = append(untestedPairs, name)
		default:
			verified++
		}
		if v.Footnotes != "" {
			footnotes = append(footnotes, name+": "+v.Footnotes)
		}
	}
	return map[string]any{
		"verified":      verified,
		"untested":      untested,
		"pairs":         verified + untested + len(inferred),
		"inferredPairs": inferred,
		"untestedPairs": untestedPairs,
		"footnotes":     footnotes,
	}
}

// stackExistsWith reports whether any release of a and any release of b can appear in a
// complete valid stack together with the pins, and returns the pair that worked so the
// caller can read the matrix's verdict for it.
func stackExistsWith(
	g *graph.Graph,
	pins map[int]*graph.Release,
	as, bs []*graph.Release,
	opts graph.StackOptions,
) (*graph.Release, *graph.Release, bool) {
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
				return a, b, true
			}
		}
	}
	return nil, nil, false
}

// setPhase records the support lifecycle of a node from the releases behind it.
//
// The matrix publishes this per release as two flags, and the interop site's "hide
// legacy releases" checkbox is exactly these two — so "legacy" here means the same thing
// it means there: nothing left in General Support.
//
// A node takes the best phase among its releases: a version line is still generally
// supported while any release in it is.
//
// The best phase alone is not the whole truth, though. One node can cover five builds
// where the newest is in General Support and the oldest is End of Support, and a plain
// green node invites picking any of them. So the worst phase is recorded alongside, and
// a node whose releases disagree is marked mixed.
func setPhase(node *mapNode, members []*graph.Release) {
	best, worst := graph.PhaseEndOfSupport, graph.PhaseGeneral
	for _, r := range members {
		p := r.Phase()
		if phaseRank(p) > phaseRank(best) {
			best = p
		}
		if phaseRank(p) < phaseRank(worst) {
			worst = p
		}
	}
	node.Phase = string(best)
	node.PhaseLabel = best.Label()
	node.WorstPhase = string(worst)
	node.WorstPhaseLabel = worst.Label()
	node.Mixed = best != worst
	node.Legacy = !best.Supported()
}

// phaseRank orders the lifecycle so best and worst are comparable. Higher is better.
func phaseRank(p graph.SupportPhase) int {
	switch p {
	case graph.PhaseGeneral:
		return 2
	case graph.PhaseTechnicalGuidance:
		return 1
	default:
		return 0
	}
}

// hostCandidates lists the ESX releases compatible with any of the given vCenter
// releases, newest first. ESX rides along on the vCenter node rather than occupying a
// layer of its own.
//
// This is the pairwise answer only. With something pinned above vCenter, use
// hostsWithPin: ESX gates the Supervisor too, so being compatible with the vCenter is
// not enough to be a host for the stack on screen.
func hostCandidates(g *graph.Graph, vcenters []*graph.Release) []*graph.Release {
	esx, ok := model.ByKey("esx")
	if !ok {
		return nil
	}
	seen := map[int]bool{}
	var out []*graph.Release
	for _, vc := range vcenters {
		for _, e := range g.Compat[vc.ID] {
			peer := g.Releases[e.Peer]
			if peer == nil || peer.ProductID != esx.ID || !graph.Compatible(e.Status) {
				continue
			}
			if !seen[peer.ID] {
				seen[peer.ID] = true
				out = append(out, peer)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return version.Compare(out[i].Version, out[j].Version) > 0
	})
	return out
}

// hostsWithPin narrows the hosts on a vCenter node to the ones that can carry the whole
// selected stack, not merely that vCenter.
//
// ESX -> Supervisor is an enforced dependency: the Supervisor control plane runs on the
// hosts. Listing every ESX release the vCenter pairs with therefore overstates the
// answer whenever something above vCenter is pinned — pin Supervisor 1.33 and the
// unnarrowed list offers eleven ESX patches for a Supervisor the matrix publishes
// against one of them. Each candidate is put through the same solver that decides
// whether a node is lit, so the annotation and the recommended stack cannot disagree.
func hostsWithPin(g *graph.Graph, pins map[int]*graph.Release, vcenters []*graph.Release) []*graph.Release {
	candidates := hostCandidates(g, vcenters)
	if len(pins) == 0 {
		return candidates
	}
	probe := graph.StackOptions{Limit: 1, IncludePatches: true}
	trial := make(map[int]*graph.Release, len(pins)+2)
	var out []*graph.Release
	for _, host := range candidates {
		for _, vc := range vcenters {
			clear(trial)
			for k, v := range pins {
				trial[k] = v
			}
			trial[vc.ProductID] = vc
			trial[host.ProductID] = host
			if stacks, _ := g.Stacks(trial, probe); len(stacks) > 0 {
				out = append(out, host)
				break
			}
		}
	}
	return out
}

// rawsOf renders releases as the strings upstream publishes.
func rawsOf(rels []*graph.Release) []string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, r.Raw)
	}
	return out
}

// linesOf collapses releases to their version lines: "9.1", "9.0", "8.0U3". That is the
// granularity anyone picking a host actually cares about.
func linesOf(rels []*graph.Release) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rels {
		line := vsphereLine(r)
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
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
