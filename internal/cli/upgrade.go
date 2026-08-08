package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
)

func newUpgradeCmd() *cobra.Command {
	var fromSpec, toSpec []string
	var explain, allHops, patches bool

	cmd := &cobra.Command{
		Use:   "upgrade --from <product=version,...> --to <product=version,...>",
		Short: "Plan an ordered upgrade from one stack to another",
		Long: `Plan an ordered sequence of single-product upgrades.

Upgrade paths are derived locally from the compatibility data rather than taken from
upstream, which is not reliable enough to build on. Three rules apply: moves are
forward-only, the Kubernetes minor may advance by at most one per step (so 1.31 to 1.33
must pass through 1.32), and every intermediate stack — not just the endpoints — must be
compatible on every published pair.

  interop upgrade --from vcenter=8.0U3,esx=8.0U3 --to vcenter=9.1.0.0

Use --explain to see why candidate moves were rejected.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gr, err := loadGraph()
			if err != nil {
				return err
			}
			current, err := parseStackSpec(gr, fromSpec, "--from")
			if err != nil {
				return err
			}
			target, err := parseStackSpec(gr, toSpec, "--to")
			if err != nil {
				return err
			}
			if len(current) == 0 {
				return fmt.Errorf("--from is required, e.g. --from vcenter=8.0U3,esx=8.0U3")
			}
			if len(target) == 0 {
				return fmt.Errorf("--to is required, e.g. --to vcenter=9.1.0.0")
			}
			for pid := range target {
				if _, ok := current[pid]; !ok {
					p, _ := model.ByID(pid)
					return fmt.Errorf("--to names %s but --from does not say where it starts", p.Label)
				}
			}
			if !gr.Check(current).OK() {
				return fmt.Errorf("the --from stack is not itself compatible; run `interop check` on it first")
			}

			opts := graph.PlanOptions{AllHops: allHops, IncludePatches: patches, Explain: explain}
			plan, failure := gr.Upgrade(current, target, opts)
			if plan == nil {
				return reportPlanFailure(cmd, gr, failure, explain)
			}

			if g.jsonOut {
				return writeJSON(cmd.OutOrStdout(), planToJSON(plan))
			}
			printPlan(cmd.OutOrStdout(), plan, current)
			if explain {
				fmt.Fprintf(cmd.OutOrStdout(), "\nexplored %d states\n", plan.Explored)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&fromSpec, "from", nil, "current stack, e.g. vcenter=8.0U3,esx=8.0U3")
	cmd.Flags().StringSliceVar(&toSpec, "to", nil, "target stack, e.g. vcenter=9.1.0.0")
	cmd.Flags().BoolVar(&explain, "explain", false, "show why candidate moves were rejected")
	cmd.Flags().BoolVar(&allHops, "all-hops", false, "consider every forward release, not just the newest per line")
	cmd.Flags().BoolVar(&patches, "patches", false, "allow patch releases as intermediate steps")
	return cmd
}

// parseStackSpec turns "vcenter=8.0U3,esx=8.0U3" into resolved releases.
func parseStackSpec(gr *graph.Graph, specs []string, flag string) (map[int]*graph.Release, error) {
	out := map[int]*graph.Release{}
	for _, spec := range specs {
		key, val, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("%s wants product=version, got %q", flag, spec)
		}
		r, err := gr.Resolve(strings.TrimSpace(key), strings.TrimSpace(val))
		if err != nil {
			return nil, err
		}
		out[r.ProductID] = r
	}
	return out, nil
}

func printPlan(w interface{ Write([]byte) (int, error) }, plan *graph.Plan, current map[int]*graph.Release) {
	if len(plan.Steps) == 0 {
		fmt.Fprintln(w, "Already at the target stack — nothing to do.")
		return
	}

	fmt.Fprintln(w, "Starting from:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range model.Products {
		if r := current[p.ID]; r != nil {
			fmt.Fprintf(tw, "  %s\t%s\n", p.Label, r.Raw)
		}
	}
	tw.Flush()

	windows := plan.Windows()
	fmt.Fprintf(w, "\n%d step(s) in %d maintenance window(s):\n", len(plan.Steps), len(windows))

	n := 0
	for wi, win := range windows {
		note := ""
		if win.Transitional {
			note = "  — do not stop partway: the stack is unsupported until the last step"
		}
		fmt.Fprintf(w, "\n  Window %d%s\n", wi+1, note)
		for _, s := range win.Steps {
			n++
			fmt.Fprintf(w, "    %d. Upgrade %s\n       %s  →  %s\n", n, s.Product.Label, s.From.Raw, s.To.Raw)
			if !s.Supported && s.Blocking != "" {
				fmt.Fprintf(w, "       transitional: %s\n", s.Blocking)
			}
		}
	}

	if last := plan.Steps[len(plan.Steps)-1]; len(last.Unverified) > 0 {
		var names []string
		for _, v := range last.Unverified {
			names = append(names, v.A.Label+"×"+v.B.Label)
		}
		fmt.Fprintf(w, "\nUnverified in the final stack (no upstream data): %s\n", strings.Join(names, ", "))
	}

	last := plan.Steps[len(plan.Steps)-1].State
	fmt.Fprintln(w, "\nEnding at:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range model.Products {
		if r := last[p.ID]; r != nil {
			fmt.Fprintf(tw, "  %s\t%s\n", p.Label, r.Raw)
		}
	}
	tw.Flush()
}

func reportPlanFailure(cmd *cobra.Command, gr *graph.Graph, f *graph.PlanFailure, explain bool) error {
	out := cmd.OutOrStdout()
	if f == nil {
		return fmt.Errorf("no upgrade path found")
	}

	if len(f.Reached) > 0 {
		fmt.Fprintln(out, "Furthest reachable stack:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, p := range model.Products {
			if r := f.Reached[p.ID]; r != nil {
				fmt.Fprintf(tw, "  %s\t%s\n", p.Label, r.Raw)
			}
		}
		tw.Flush()
	}
	if explain && len(f.Rejections) > 0 {
		fmt.Fprintln(out, "\nWhy the remaining moves were rejected:")
		seen := map[string]bool{}
		for _, r := range f.Rejections {
			line := fmt.Sprintf("  %s %s → %s: %s", r.Product.Label, r.From.Raw, r.To.Raw, r.Reason)
			if seen[line] {
				continue
			}
			seen[line] = true
			fmt.Fprintln(out, line)
		}
	}
	if f.HitLimit {
		return fmt.Errorf("no valid path found within the search limit (%d states explored)", f.Explored)
	}
	return fmt.Errorf("no valid upgrade path exists (%d states explored); rerun with --explain for why", f.Explored)
}

// planJSON is the machine-readable plan, keyed by product short key.
type planJSON struct {
	Steps []planStepJSON `json:"steps"`
}

type planStepJSON struct {
	Product    string            `json:"product"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	State      map[string]string `json:"state"`
	Unverified []string          `json:"unverified,omitempty"`
}

func planToJSON(plan *graph.Plan) planJSON {
	out := planJSON{}
	for _, s := range plan.Steps {
		step := planStepJSON{
			Product: s.Product.Key, From: s.From.Raw, To: s.To.Raw,
			State: map[string]string{},
		}
		for _, p := range model.Products {
			if r := s.State[p.ID]; r != nil {
				step.State[p.Key] = r.Raw
			}
		}
		for _, v := range s.Unverified {
			step.Unverified = append(step.Unverified, v.A.Key+"×"+v.B.Key)
		}
		out.Steps = append(out.Steps, step)
	}
	return out
}
