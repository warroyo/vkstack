package model

import (
	"fmt"
	"sort"
	"strings"
)

// Coverage reports whether upstream publishes compatibility data for a product pair.
// The pair is given as upstream product ids in either order.
type Coverage func(aProductID, bProductID int) bool

// AllPublished is a Coverage that claims every pair is published.
func AllPublished(int, int) bool { return true }

// knownUnpublished is the set of product pairs upstream did not publish when this was
// last verified against the live API (the original seven on 2026-08-10, the four TMC-SM
// pairs on 2026-08-21). It exists so `vkstack explain` and the generated docs/model.md
// are correct on a cold install with no cache.
//
// Eleven of the twenty-eight pairs. Seven are against VKS or VKr — neither NSX nor Avi is
// published against the guest-cluster layer, consistent with neither being a dependency of
// it. The other four are TMC Self-Managed against everything that is not one of its three
// dependencies: it is published against vCenter, VKS and VKr, and against nothing else.
//
// A populated cache always overrides this — see the cache-backed Coverage in the CLI.
// TestCoverageMatchesUpstream in internal/graph compares the two and fails if upstream
// starts publishing a pair, which is the signal to update this list.
var knownUnpublished = map[[2]int]bool{
	{1, 1794}:    true, // ESX × VKS
	{1, 820}:     true, // ESX × VKr
	{820, 1378}:  true, // VKr × Supervisor
	{820, 912}:   true, // VKr × NSX
	{912, 1794}:  true, // NSX × VKS
	{820, 1795}:  true, // VKr × Avi
	{1794, 1795}: true, // VKS × Avi
	{1, 1771}:    true, // ESX × TMC-SM
	{912, 1771}:  true, // NSX × TMC-SM
	{1378, 1771}: true, // Supervisor × TMC-SM
	{1771, 1795}: true, // TMC-SM × Avi
}

// DefaultCoverage reports the pair coverage observed against the live API, for use when
// no cache is loaded.
func DefaultCoverage(a, b int) bool {
	if a > b {
		a, b = b, a
	}
	return !knownUnpublished[[2]int{a, b}]
}

// KnownUnpublishedPairs returns the observed-unpublished pairs, lower product id first.
func KnownUnpublishedPairs() [][2]int {
	out := make([][2]int, 0, len(knownUnpublished))
	for p := range knownUnpublished {
		a, b := p[0], p[1]
		if a > b {
			a, b = b, a
		}
		out = append(out, [2]int{a, b})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// nodeID is the mermaid node identifier for a product key.
func nodeID(key string) string { return strings.ToUpper(key) }

// Backbone reports whether an edge belongs on the on-screen explainer: the conceptual
// chain, plus the one gap worth knowing about. The direct vCenter-to-VKS and
// vCenter-to-VKr edges are real and the solver benefits from them, but they describe how
// the matrix is laid out rather than how the components relate, so they stay in the doc.
func Backbone(e Edge) bool {
	return e.Primary || e.Evidence == EvidenceInferred
}

// Mermaid renders the conceptual model as a mermaid flowchart.
//
// Edges whose product pair is not published upstream are drawn dashed and labelled, so
// the diagram reflects the real state of the data rather than a claim baked in at
// authoring time. Pass AllPublished when no cache has been loaded.
func Mermaid(covered Coverage) string { return mermaid(covered, false) }

// MermaidBackbone renders only the conceptual chain, for the on-screen explainer.
func MermaidBackbone(covered Coverage) string { return mermaid(covered, true) }

func mermaid(covered Coverage, backboneOnly bool) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")

	for _, p := range Products {
		if len(p.Trains) > 0 {
			// Two trains at the same version is the single most misread fact here, so
			// it belongs on the node, not in a footnote.
			fmt.Fprintf(&b, "    %s[\"<b>%s</b><br/><code>%s</code><br/>two trains: %s\"]\n",
				nodeID(p.Key), p.Label, p.Example, strings.Join(p.Trains, " · "))
			continue
		}
		if p.Optional {
			// Say it on the node. A reader who sees NSX drawn like the rest will assume
			// every stack has one.
			fmt.Fprintf(&b, "    %s[\"<b>%s</b><br/><code>%s</code><br/><i>optional</i>\"]\n",
				nodeID(p.Key), p.Label, p.Example)
			continue
		}
		fmt.Fprintf(&b, "    %s[\"<b>%s</b><br/><code>%s</code>\"]\n",
			nodeID(p.Key), p.Label, p.Example)
	}
	b.WriteString("\n")

	for _, e := range Edges {
		if backboneOnly && !Backbone(e) {
			continue
		}
		from, _ := ByKey(e.From)
		to, _ := ByKey(e.To)
		published := covered(from.ID, to.ID)

		label := e.Label
		arrow := "-->"
		switch {
		case !published:
			// Unpublished pairs are the ones a reader most needs flagged.
			arrow = "-.->"
			label = e.Label + "<br/><i>no published data</i>"
		case e.Evidence == EvidenceDomain:
			arrow = "-.->"
			label = e.Label + "<br/><i>not in the matrix</i>"
		case e.Bidirectional:
			arrow = "<-->"
		case !e.Primary:
			arrow = "-.->"
		}
		fmt.Fprintf(&b, "    %s %s|\"%s\"| %s\n", nodeID(e.From), arrow, label, nodeID(e.To))
	}

	b.WriteString("\n")
	b.WriteString("    classDef base fill:#e8f0fe,stroke:#4a6fa5,stroke-width:3px,color:#12263f\n")
	b.WriteString("    classDef k8s fill:#eaf6ec,stroke:#4a8a5c,stroke-width:3px,color:#12331c\n")
	// Optional components get their own class and a dashed border, so "not every stack
	// has this" survives being read at a glance.
	b.WriteString("    classDef optional fill:#fdf4e7,stroke:#a5784a,stroke-width:3px,stroke-dasharray:5 4,color:#3f2c12\n")

	var base, k8s, optional []string
	for _, p := range Products {
		switch {
		case p.Optional:
			optional = append(optional, nodeID(p.Key))
		case p.Scheme == SchemeVSphere:
			base = append(base, nodeID(p.Key))
		default:
			k8s = append(k8s, nodeID(p.Key))
		}
	}
	fmt.Fprintf(&b, "    class %s base\n", strings.Join(base, ","))
	fmt.Fprintf(&b, "    class %s k8s\n", strings.Join(k8s, ","))
	if len(optional) > 0 {
		fmt.Fprintf(&b, "    class %s optional\n", strings.Join(optional, ","))
	}

	return b.String()
}

// ASCII renders a plain box-and-line fallback for a bare terminal. Mermaid is the
// default everywhere else; this exists so `vkstack explain --ascii` is readable over ssh.
func ASCII(covered Coverage) string {
	var b strings.Builder
	b.WriteString("  ┌──────────────┐   version pairing   ┌──────────────┐\n")
	b.WriteString("  │   vCenter    │◄───────────────────►│     ESX      │   base layer\n")
	b.WriteString("  │   8.0U3k     │   vCenter ≥ ESX     │   8.0U3k     │\n")
	b.WriteString("  └──────┬───────┘                     └──────┬───────┘\n")
	b.WriteString("         │ manages / delivers                 │ hosts run it\n")
	b.WriteString("         ├─────────────────┬──────────────────┤\n")
	b.WriteString("         ▼                 │                  ▼\n")
	b.WriteString("  ╎ ─ ─ ─ ─ ─ ─ ╎          │           ╎ ─ ─ ─ ─ ─ ─ ╎\n")
	b.WriteString("  ╎     NSX     ╎◄─────────┼──────────►╎     Avi     ╎   OPTIONAL: each is\n")
	b.WriteString("  ╎ 9.1.0.0200  ╎ segments │           ╎   32.1.2    ╎   opted into on its\n")
	b.WriteString("  ╎ ─ ─ ─ ─ ─ ─ ╎          │           ╎ ─ ─ ─ ─ ─ ─ ╎   own; Avi does NOT\n")
	b.WriteString("         │ networking      │    load balancer │          require NSX\n")
	b.WriteString("         └─────────────────┬──────────────────┘\n")
	b.WriteString("                           ▼\n")
	b.WriteString("                  ┌──────────────────┐\n")
	b.WriteString("                  │    Supervisor    │  TWO TRAINS, not interchangeable:\n")
	b.WriteString("                  │  vsc9.x / vsc0.x │  vsc9.x ships with vCenter 9.x,\n")
	b.WriteString("                  │ v1.32.9+vmware.2 │  vsc0.x is versioned on its own.\n")
	b.WriteString("                  └────────┬─────────┘\n")
	b.WriteString("                           │ runs\n")
	b.WriteString("                           ▼\n")
	b.WriteString("                  ┌──────────────────┐\n")
	b.WriteString("                  │       VKS        │  version declares the k8s\n")
	b.WriteString("                  │   3.7.0+v1.36    │  minor it serves: +v1.36\n")
	b.WriteString("                  └────────┬─────────┘\n")
	b.WriteString("                           │ provisions\n")
	b.WriteString("                           ▼\n")
	b.WriteString("                  ┌──────────────────┐\n")
	b.WriteString("                  │       VKr        │  the guest cluster's actual\n")
	b.WriteString("                  │      1.36.1      │  Kubernetes release\n")
	b.WriteString("                  └────────┬─────────┘\n")
	b.WriteString("                           │ runs on\n")
	b.WriteString("                           ▼\n")
	b.WriteString("                  ╎ ─ ─ ─ ─ ─ ─ ─ ─ ╎  OPTIONAL: a self-hosted management\n")
	b.WriteString("                  ╎     TMC-SM      ╎  plane, versioned against vCenter,\n")
	b.WriteString("                  ╎      1.4.5      ╎  VKS and VKr — all three published\n")
	b.WriteString("                  ╎ ─ ─ ─ ─ ─ ─ ─ ─ ╎  and enforced when it is present\n")
	b.WriteString("\n")

	var unpublished []string
	for _, e := range Edges {
		from, _ := ByKey(e.From)
		to, _ := ByKey(e.To)
		if !covered(from.ID, to.ID) {
			unpublished = append(unpublished, from.Label+" × "+to.Label)
		}
	}
	if len(unpublished) > 0 {
		sort.Strings(unpublished)
		fmt.Fprintf(&b, "  Not published upstream: %s\n", strings.Join(unpublished, ", "))
	}
	fmt.Fprintf(&b, "  Upgrade order: %s\n", strings.Join(orderedLabels(), " → "))
	return b.String()
}

// Explanation is the per-edge prose, rendered as a list under the diagram.
func Explanation(covered Coverage) string {
	var b strings.Builder
	for _, e := range Edges {
		from, _ := ByKey(e.From)
		to, _ := ByKey(e.To)
		// Say how each claim is known, so an asserted rule never reads like a lookup.
		evidence := e.Evidence
		if evidence == "" {
			evidence = EvidenceDomain
		}
		if !covered(from.ID, to.ID) {
			evidence = EvidenceInferred
		}
		fmt.Fprintf(&b, "- **%s → %s** *(%s)* — %s\n",
			from.Label, to.Label, evidence.Describe(), e.Prose)
	}
	return b.String()
}

// DecidingPairs states which relationships validate a stack and which are looked up but
// never allowed to decide anything.
//
// Both lists are derived from Edges rather than written down, so a change to Primary
// shows up here instead of leaving the prose behind.
func DecidingPairs(covered Coverage) string {
	var deps, rest, restUnpublished []string
	for _, e := range Edges {
		from, _ := ByKey(e.From)
		to, _ := ByKey(e.To)
		label := from.Label + " × " + to.Label
		switch {
		case e.Primary:
			deps = append(deps, label)
		default:
			rest = append(rest, "**"+label+"**")
			if !covered(from.ID, to.ID) {
				restUnpublished = append(restUnpublished, label)
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s of the %s relationships above are dependencies, and those alone decide "+
		"whether a stack is valid: %s.\n\n",
		sentenceCase(countWord(len(deps))), countWord(len(Edges)), commaAnd(deps))
	fmt.Fprintf(&b, "The other %s are looked up but never allowed to decide a stack: %s. The reason "+
		"differs by pair and is given with each relationship above. Some are not dependencies "+
		"at all — VKr is provisioned by VKS, so a direct vCenter verdict on it decides nothing. "+
		"Others are real dependencies that still constrain nothing in practice: NSX does prepare "+
		"the ESX hosts as transport nodes, but that grid is nearly as permissive as the vCenter "+
		"one and the two move together, so enforcing it rules out nothing the vCenter pair has "+
		"not already ruled out. ESX × Avi is the opposite shape — almost every cell is empty, so "+
		"enforcing it would collapse every Avi-bearing stack rather than narrow it.\n\n",
		countWord(len(rest)), commaAnd(rest))
	published := "Upstream publishes all of them"
	if len(restUnpublished) > 0 {
		published = "Upstream publishes all of them but " + commaAnd(restUnpublished) +
			", which has no data at all"
	}
	fmt.Fprintf(&b, "%s, and marks some combinations NOT SUPPORTED "+
		"that this tool will still recommend. That is expected rather than a contradiction: VKr "+
		"1.32.10, for one, is published against vCenter 9.0.0.0 but not 8.0U3, while VKr 1.32.7 "+
		"covers both — even though VKS 3.3.3 serves either one and nothing in the dependency "+
		"chain differs. `vkstack stack` therefore reports the dependency chain and nothing else. "+
		"To see a non-dependency pair as upstream publishes it, ask for it directly with "+
		"`vkstack compat` or name the whole combination with `vkstack check`; both report every "+
		"published pair.\n", published)
	return b.String()
}

// countWord spells small counts, which read better than digits in the middle of prose.
func countWord(n int) string {
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
		"nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen"}
	if n < len(words) {
		return words[n]
	}
	return fmt.Sprint(n)
}

// sentenceCase uppercases the first letter, for a spelled count opening a sentence.
func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// commaAnd joins a list the way prose does, with "and" before the last item.
func commaAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func orderedLabels() []string {
	keys := UpgradeOrder()
	labels := make([]string, len(keys))
	for i, k := range keys {
		p, _ := ByKey(k)
		labels[i] = p.Label
	}
	return labels
}

// Doc renders the full standalone explainer: diagram, prose, and upgrade order. This is
// what lands in docs/model.md and what `vkstack explain` prints.
func Doc(covered Coverage) string {
	var b strings.Builder
	b.WriteString("# How vCenter, ESX, NSX, Avi, Supervisor, VKS, VKr and TMC-SM fit together\n\n")
	b.WriteString("The Broadcom interoperability matrix answers one question at a time:\n")
	b.WriteString("\"is A compatible with B\". This is the shape of the whole dependency,\n")
	b.WriteString("which is the part that usually has to be drawn on a whiteboard.\n\n")
	b.WriteString("Five of these are in every stack. **NSX, Avi and TMC-SM are optional and\n")
	b.WriteString("independent**: a Supervisor runs on NSX networking, or on a vSphere\n")
	b.WriteString("Distributed Switch with Avi in front of it, or on neither; TMC Self-Managed\n")
	b.WriteString("is installed on top of the guest-cluster layer where a self-hosted management\n")
	b.WriteString("plane is wanted. Opt into each on its own — asking for one never brings in\n")
	b.WriteString("another.\n\n")
	b.WriteString("```mermaid\n")
	b.WriteString(Mermaid(covered))
	b.WriteString("```\n\n")
	b.WriteString("## What each relationship means\n\n")
	b.WriteString(Explanation(covered))
	b.WriteString("\n## Which pairs decide a stack\n\n")
	b.WriteString(DecidingPairs(covered))
	b.WriteString("\n## Reading the versions\n\n")
	for _, p := range Products {
		if len(p.Trains) == 0 {
			continue
		}
		fmt.Fprintf(&b, "**%s ships %d trains: %s.** %s\n\n",
			p.Label, len(p.Trains), strings.Join(p.Trains, " and "), p.TrainNote)
	}

	b.WriteString("## Where the answers get summarised\n\n")
	for _, note := range GroupingNotes() {
		fmt.Fprintf(&b, "- %s\n", note)
	}

	b.WriteString("\n## What this tool does not know\n\n")
	b.WriteString("Every limit below is stated rather than glossed. A compatibility tool that\n")
	b.WriteString("presents a gap as an answer is worse than no tool.\n\n")
	for _, u := range Unknowns(covered) {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", u.Title, u.Detail)
		fmt.Fprintf(&b, "*What it means:* %s\n\n", u.Consequence)
		if u.Workaround != "" {
			fmt.Fprintf(&b, "*What to do:* %s\n\n", u.Workaround)
		}
	}

	b.WriteString("## Upgrade order\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.Join(orderedLabels(), " → "))
	b.WriteString("vCenter moves first because it must be at or ahead of the hosts and the\n")
	b.WriteString("Supervisor it manages; the guest clusters move last. This ordering is\n")
	b.WriteString("operational knowledge, not something the matrix states — see the limits above.\n")
	b.WriteString("\n<!-- Generated by `vkstack explain`. Edit internal/model, not this file. -->\n")
	return b.String()
}
