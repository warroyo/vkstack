package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
)

// `vkstack path` finds the shortest upgrade route between two stacks where every state on
// the way is valid. See graph.Path for the rules the search follows.

func newPathCmd() *cobra.Command {
	var from, to, exclude []string
	cmd := &cobra.Command{
		Use:   "path --from product=version ... --to product=version ...",
		Short: "Shortest upgrade route between two stacks through valid states",
		Long: `Find the shortest sequence of single-product upgrades that takes one stack to
another, where every state on the way is valid on every enforced pair.

Pin the same products on both sides. Optional products (Avi, NSX, TMC-SM) take part only
when pinned, and they are usually what decides the route. The Supervisor and guest
clusters (VKr) move one Kubernetes minor per step. Guest clusters are constrained by VKS
alone. The ESX 8 with vsc9 Supervisor state is allowed and
flagged transitional, because the vSphere 8 → 9 upgrade order passes through it.

The matrix says which versions coexist, not which hops are supported upgrades. Use
--exclude for releases a back-in-time rule or an upgrade checklist rules out.

Exits 7 when no route exists, naming the optional product in the way when there is one.`,
		Example: `  vkstack path \
    --from vcenter=8.0U3 --from supervisor=v1.28.3+vmware.2-fips.1-vsc0.1.9 --from avi=22.1.7 \
    --to vcenter=9.1.1.0 --to supervisor=v1.33.13+vmware.1-fips-vsc9.1.1.0 --to avi=32.1.3`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gr, err := loadGraph()
			if err != nil {
				return err
			}
			src, err := resolvePairs(gr, from, "--from")
			if err != nil {
				return err
			}
			dst, err := resolvePairs(gr, to, "--to")
			if err != nil {
				return err
			}
			excl, err := resolveExclude(gr, exclude)
			if err != nil {
				return err
			}
			res, fail := gr.Path(src, dst, graph.PathOptions{Exclude: excl})
			if fail != nil {
				return pathFailureErr(fail)
			}
			if g.mode == OutputCSV {
				return csvUnavailable("path")
			}
			if g.jsonOut {
				return emit(cmd, "path", 1, pathJSON(res))
			}
			printPath(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&from, "from", nil, "starting release as product=version (repeat per product)")
	cmd.Flags().StringArrayVar(&to, "to", nil, "target release as product=version (repeat per product)")
	cmd.Flags().StringArrayVar(&exclude, "exclude", nil,
		"release the path must not land on, as product=version (repeatable)")
	return cmd
}

// resolvePairs turns ["vcenter=8.0U3", ...] into resolved releases keyed by product id.
func resolvePairs(gr *graph.Graph, vals []string, flag string) (map[int]*graph.Release, error) {
	named := map[string]string{}
	for _, kv := range vals {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(val) == "" {
			return nil, codedErr("bad_flag", ExitUsage, "%s wants product=version, got %q", flag, kv)
		}
		named[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(val)
	}
	return resolveNamedPins(gr, named)
}

func resolveExclude(gr *graph.Graph, vals []string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, kv := range vals {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, codedErr("bad_flag", ExitUsage, "--exclude wants product=version, got %q", kv)
		}
		r, err := gr.Resolve(strings.TrimSpace(key), strings.TrimSpace(val))
		if err != nil {
			return nil, err
		}
		out[r.ID] = true
	}
	return out, nil
}

// excludeFromMap resolves the MCP form, {"vcenter": ["9.0.2.0", ...]}.
func excludeFromMap(gr *graph.Graph, m map[string][]string) (map[int]bool, error) {
	var flat []string
	for k, vs := range m {
		for _, v := range vs {
			flat = append(flat, k+"="+v)
		}
	}
	return resolveExclude(gr, flat)
}

func pathFailureErr(f *graph.PathFailure) error {
	exit := ExitNoStack
	switch f.Reason {
	case "usage", "backwards":
		exit = ExitUsage
	case "too_large":
		exit = ExitError
	}
	details := map[string]any{"reason": f.Reason, "explored": f.Explored}
	if len(f.Blocking) > 0 {
		var pairs []string
		for _, v := range f.Blocking {
			pairs = append(pairs, fmt.Sprintf("%s %s × %s %s: %s",
				v.A.Label, v.ARelease.Raw, v.B.Label, v.BRelease.Raw, verdictWord(v)))
		}
		details["blocking"] = pairs
	}
	hint := ""
	if len(f.BlockedBy) > 0 {
		var keys []string
		for _, p := range f.BlockedBy {
			keys = append(keys, p.Key)
		}
		details["blockedBy"] = keys
		hint = "a route exists without " + strings.Join(keys, ", ") +
			"; check that product's windows with `vkstack stack --" + keys[0] + " <version>`"
	}
	return &CodedError{Code: "no_path", Exit: exit, Message: f.Message, Details: details, Hint: hint}
}

func stateJSON(state map[int]*graph.Release) map[string]string {
	out := map[string]string{}
	for id, r := range state {
		if p, ok := model.ByID(id); ok {
			out[p.Key] = r.Raw
		}
	}
	return out
}

func pairNames(vs []graph.PairVerdict) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, fmt.Sprintf("%s %s × %s %s", v.A.Label, v.ARelease.Raw, v.B.Label, v.BRelease.Raw))
	}
	return out
}

func pathJSON(res *graph.PathResult) map[string]any {
	keys := make([]string, 0, len(res.Products))
	for _, p := range res.Products {
		keys = append(keys, p.Key)
	}
	steps := make([]map[string]any, 0, len(res.Steps))
	for i, s := range res.Steps {
		steps = append(steps, map[string]any{
			"step":         i + 1,
			"product":      s.Product.Key,
			"from":         s.From.Raw,
			"to":           s.To.Raw,
			"state":        stateJSON(s.State),
			"notTested":    pairNames(s.NotTested),
			"transitional": s.Transitional,
		})
	}
	return map[string]any{
		"products":  keys,
		"start":     stateJSON(res.Start),
		"target":    stateJSON(res.Target),
		"stepCount": len(res.Steps),
		"steps":     steps,
		"note": "Every state is valid in the matrix. The matrix does not say whether each hop " +
			"is a supported upgrade: check back-in-time rules and each product's upgrade " +
			"checklist, and exclude releases they rule out.",
	}
}

func printPath(w io.Writer, res *graph.PathResult) {
	fmt.Fprintf(w, "%d steps over %d products\n\n", len(res.Steps), len(res.Products))
	for i, s := range res.Steps {
		fmt.Fprintf(w, "%3d  %-10s %s → %s", i+1, s.Product.Label, s.From.Raw, s.To.Raw)
		if s.Transitional {
			fmt.Fprint(w, "  [transitional: ESX 8 with vsc9 Supervisor]")
		}
		fmt.Fprintln(w)
		for _, nt := range pairNames(s.NotTested) {
			fmt.Fprintf(w, "       not tested: %s\n", nt)
		}
	}
	fmt.Fprintln(w, "\nEvery state is valid in the matrix; confirm each hop against upgrade docs.")
}
