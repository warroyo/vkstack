package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/model"
)

func newExplainCmd() *cobra.Command {
	var ascii, diagramOnly bool

	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Show how vCenter, ESX, Supervisor, VKS, VKr, NSX, Avi and TMC-SM fit together",
		Long: `Show how the eight components depend on each other.

Five are in every deployment; NSX, Avi and TMC Self-Managed are optional and independent
of each other, and are drawn as such.

Prints mermaid by default, so it can be pasted straight into a PR, an issue or a
markdown doc. Use --ascii for a plain diagram in a bare terminal.

Works without a cache; if one exists, pairs that upstream does not publish are drawn
dashed and called out.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			covered := coverageFromCache()
			switch {
			case ascii:
				fmt.Fprint(cmd.OutOrStdout(), model.ASCII(covered))
			case diagramOnly:
				fmt.Fprint(cmd.OutOrStdout(), model.Mermaid(covered))
			case g.jsonOut:
				// The model is the part an agent most needs before reasoning: it says
				// which relationships are real, which are merely published, and which
				// are not knowable at all.
				return emit(cmd, "model", 1, modelJSON(covered))
			default:
				fmt.Fprint(cmd.OutOrStdout(), model.Doc(covered))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&ascii, "ascii", false, "plain box-drawing output instead of mermaid")
	cmd.Flags().BoolVar(&diagramOnly, "diagram-only", false, "emit just the mermaid flowchart, no prose")
	return cmd
}

// modelJSON is the dependency model as data.
//
// The prose forms are for people; this is the same knowledge shaped so a program can act
// on it — in particular the distinction between a relationship that constrains a stack
// and one that is merely published, which is the difference between a right answer and a
// combination that is listed compatible yet cannot exist.
func modelJSON(covered model.Coverage) map[string]any {
	products := make([]map[string]any, 0, len(model.Products))
	for _, p := range model.Products {
		entry := map[string]any{
			"key": p.Key, "label": p.Label, "name": p.Name,
			"example": p.Example, "upgradeOrder": p.UpgradeOrder,
			"versionScheme": string(p.Scheme),
		}
		if len(p.Trains) > 0 {
			entry["trains"] = p.Trains
			entry["trainNote"] = p.TrainNote
		}
		products = append(products, entry)
	}

	edges := make([]map[string]any, 0, len(model.Edges))
	for _, e := range model.Edges {
		from, _ := model.ByKey(e.From)
		to, _ := model.ByKey(e.To)
		edges = append(edges, map[string]any{
			"from": e.From, "to": e.To,
			"label": e.Label, "summary": e.Summary, "prose": e.Prose,
			"bidirectional": e.Bidirectional,
			// The key is named "dependency" for compatibility, but it reports the
			// constraint set: ESX × NSX is a real dependency and is false here.
			"dependency":    e.Primary,
			"evidence":      string(e.Evidence),
			"evidenceMeans": e.Evidence.Describe(),
			"publishedNow":  covered(from.ID, to.ID),
		})
	}

	// Every limit is stated rather than glossed: a compatibility tool that presents a
	// gap as an answer is worse than no tool, and that is truer for an agent, which has
	// no way to notice the omission for itself.
	unknowns := make([]map[string]any, 0)
	for _, u := range model.Unknowns(covered) {
		unknowns = append(unknowns, map[string]any{
			"title":       u.Title,
			"detail":      u.Detail,
			"consequence": u.Consequence,
			"workaround":  u.Workaround,
		})
	}

	return map[string]any{
		"products":     products,
		"edges":        edges,
		"upgradeOrder": model.UpgradeOrder(),
		"unknowns":     unknowns,
	}
}

// coverageFromCache reports which product pairs upstream publishes, so the diagram
// reflects the real state of the data rather than a claim baked in at authoring time.
//
// With no cache, it falls back to the coverage observed the last time this was verified
// against the live API, so `vkstack explain` still works on a cold install.
func coverageFromCache() model.Coverage {
	db, err := openCache()
	if err != nil {
		return model.DefaultCoverage
	}
	defer db.Close()

	snap, err := db.Load()
	if err != nil || len(snap.Coverage) == 0 {
		return model.DefaultCoverage
	}
	published := map[[2]int]bool{}
	for _, pc := range snap.Coverage {
		published[normalisePair(pc.AProduct, pc.BProduct)] = pc.EdgeCount > 0
	}
	return func(a, b int) bool {
		return published[normalisePair(a, b)]
	}
}

func normalisePair(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}
