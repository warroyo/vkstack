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
// last verified against the live API (2026-08-08). It exists so `interop explain` and
// the generated docs/model.md are correct on a cold install with no cache.
//
// A populated cache always overrides this — see the cache-backed Coverage in the CLI.
// TestCoverageMatchesUpstream in internal/graph compares the two and fails if upstream
// starts publishing a pair, which is the signal to update this list.
var knownUnpublished = map[[2]int]bool{
	{1, 1794}:   true, // ESX × VKS
	{1, 820}:    true, // ESX × VKr
	{820, 1378}: true, // VKr × Supervisor
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

// Mermaid renders the conceptual model as a mermaid flowchart.
//
// Edges whose product pair is not published upstream are drawn dashed and labelled, so
// the diagram reflects the real state of the data rather than a claim baked in at
// authoring time. Pass AllPublished when no cache has been loaded.
func Mermaid(covered Coverage) string {
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
		fmt.Fprintf(&b, "    %s[\"<b>%s</b><br/><code>%s</code>\"]\n",
			nodeID(p.Key), p.Label, p.Example)
	}
	b.WriteString("\n")

	for _, e := range Edges {
		from, _ := ByKey(e.From)
		to, _ := ByKey(e.To)
		published := covered(from.ID, to.ID)

		label := e.Label
		arrow := "-->"
		switch {
		case !published:
			// Unpublished pairs are the ones a reader most needs flagged.
			arrow = "-.->"
			label = e.Label + "<br/><i>no data — inferred</i>"
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

	var base, k8s []string
	for _, p := range Products {
		if p.Scheme == SchemeVSphere {
			base = append(base, nodeID(p.Key))
		} else {
			k8s = append(k8s, nodeID(p.Key))
		}
	}
	fmt.Fprintf(&b, "    class %s base\n", strings.Join(base, ","))
	fmt.Fprintf(&b, "    class %s k8s\n", strings.Join(k8s, ","))

	return b.String()
}

// ASCII renders a plain box-and-line fallback for a bare terminal. Mermaid is the
// default everywhere else; this exists so `interop explain --ascii` is readable over ssh.
func ASCII(covered Coverage) string {
	var b strings.Builder
	b.WriteString("  ┌──────────────┐   version pairing   ┌──────────────┐\n")
	b.WriteString("  │   vCenter    │◄───────────────────►│     ESX      │   base layer\n")
	b.WriteString("  │   8.0U3k     │   vCenter ≥ ESX     │   8.0U3k     │\n")
	b.WriteString("  └──────┬───────┘                     └──────┬───────┘\n")
	b.WriteString("         │ manages / delivers                 │ hosts run it\n")
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
	b.WriteString("                  └──────────────────┘\n")
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
// what lands in docs/model.md and what `interop explain` prints.
func Doc(covered Coverage) string {
	var b strings.Builder
	b.WriteString("# How vCenter, ESX, Supervisor, VKS and VKr fit together\n\n")
	b.WriteString("The Broadcom interoperability matrix answers one question at a time:\n")
	b.WriteString("\"is A compatible with B\". This is the shape of the whole dependency,\n")
	b.WriteString("which is the part that usually has to be drawn on a whiteboard.\n\n")
	b.WriteString("```mermaid\n")
	b.WriteString(Mermaid(covered))
	b.WriteString("```\n\n")
	b.WriteString("## What each relationship means\n\n")
	b.WriteString(Explanation(covered))
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
	b.WriteString("\n<!-- Generated by `interop explain`. Edit internal/model, not this file. -->\n")
	return b.String()
}
