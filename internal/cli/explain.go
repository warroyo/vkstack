package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/warroyo/interop-visualizer/internal/model"
)

func newExplainCmd() *cobra.Command {
	var ascii, diagramOnly bool

	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Show how vCenter, ESX, Supervisor, VKS and VKr fit together",
		Long: `Show how the five components depend on each other.

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

// coverageFromCache reports which product pairs upstream publishes, so the diagram
// reflects the real state of the data rather than a claim baked in at authoring time.
//
// With no cache, it falls back to the coverage observed the last time this was verified
// against the live API, so `interop explain` still works on a cold install.
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
