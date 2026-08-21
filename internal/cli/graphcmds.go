package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

// loadGraph opens the cache and builds the in-memory graph with the active floors.
func loadGraph() (*graph.Graph, error) {
	db, err := openCache()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if err := warnIfStale(db); err != nil {
		return nil, err
	}
	rememberSnapshot(db)
	return loadGraphFrom(db, g.generation)
}

// rememberSnapshot records which cache an answer came from, so every JSON envelope can
// say so. An agent cannot see the header a person reads in the UI, and a compatibility
// answer is only as current as the data behind it.
func rememberSnapshot(db *store.DB) {
	at, err := db.FetchedAt()
	if err != nil || at.IsZero() {
		return
	}
	snapshotFn = func() *snapshotJSON {
		age := time.Since(at)
		return &snapshotJSON{
			FetchedAt: at.Format(time.RFC3339),
			AgeHours:  int(age.Hours()),
			Stale:     age > g.maxAge,
		}
	}
}

// loadGraphFrom builds the graph from an already-open cache. `serve` holds one handle for
// its lifetime rather than opening and migrating the database on every request.
// loadGraphFrom builds the graph for one generation.
//
// The generation is a parameter rather than a global because the long-lived callers — the
// MCP server and the web server — take it per request, where the flag is only the CLI's
// default. Zero means every generation.
func loadGraphFrom(db *store.DB, generation int) (*graph.Graph, error) {
	snap, err := db.Load()
	if err != nil {
		return nil, err
	}
	opts, err := graphOptions()
	if err != nil {
		return nil, err
	}
	opts.Generation = generation
	return graph.Load(snap, opts)
}

// graphOptions turns the global flags into load options.
func graphOptions() (graph.Options, error) {
	mins := map[string]string{}
	for _, mv := range g.minVersions {
		key, val, ok := strings.Cut(mv, "=")
		if !ok {
			return graph.Options{}, fmt.Errorf("--min-version wants product=version, got %q", mv)
		}
		mins[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	// A generation the model does not know is rejected rather than ignored: on the CLI a
	// typo should say so, where a stale query string on the web should still render.
	if !model.KnownGeneration(g.generation) {
		return graph.Options{}, fmt.Errorf("--generation wants one of %s, got %d",
			generationList(), g.generation)
	}
	return graph.Options{
		AllVersions: g.allVersions,
		MinVersions: mins,
		Generation:  g.generation,
	}, nil
}

// generationList renders the known generations for an error message.
func generationList() string {
	out := make([]string, 0, len(model.Generations))
	for _, gen := range model.Generations {
		out = append(out, strconv.Itoa(gen))
	}
	return strings.Join(out, ", ")
}

// statusWord renders a status code the way the interop matrix labels it.
func statusWord(status int) string {
	switch status {
	case store.StatusCompatible:
		return "compatible"
	case store.StatusIncompatible:
		return "INCOMPATIBLE"
	case store.StatusCompatibleNT:
		return "compatible (not tested)"
	case store.StatusNotSupported:
		return "NOT SUPPORTED"
	default:
		return fmt.Sprintf("status %d", status)
	}
}

func newReleasesCmd() *cobra.Command {
	var hidePatches, patches, legacy bool
	cmd := &cobra.Command{
		Use:   "releases <product>",
		Short: "List a product's releases, oldest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			gr, err := loadGraph()
			if err != nil {
				return err
			}
			p, ok := model.ByKey(args[0])
			if !ok {
				return fmt.Errorf("unknown product %q", args[0])
			}
			// Filter before rendering so --hide-patches applies to every output
			// format, not just the table.
			var releases []*graph.Release
			for _, r := range gr.ReleasesOf(p.ID) {
				if r.IsPatch() && hidePatches {
					continue
				}
				// Match the interop site's default: legacy releases are hidden unless
				// asked for.
				if !r.EffectivePhase().Supported() && !legacy {
					continue
				}
				releases = append(releases, r)
			}

			switch g.mode {
			case OutputJSON:
				return emit(cmd, "releases", 1, map[string]any{
					"product": args[0], "releases": releases,
				})
			case OutputCSV:
				return releasesCSV(cmd, p.Key, releases)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "VERSION\tTYPE\tGA\tEOGS\tSUPPORT")
			for _, r := range releases {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					r.Raw, r.ReleaseType, shortDate(r.GADate), shortDate(r.EOGSDate),
					r.EffectivePhase().Label())
			}
			return tw.Flush()
		},
	}
	addPatchFlags(cmd, &hidePatches, &patches)
	cmd.Flags().BoolVar(&legacy, "legacy", false,
		"include releases past General Support (what the interop site calls legacy)")
	return cmd
}

// addPatchFlags wires --hide-patches, plus the --patches flag it replaced.
//
// Patch releases are shown by default, unlike on the interop site, because the patch
// letter routinely decides the answer: vCenter 8.0U3 takes Supervisor 1.26 to 1.28 and
// 8.0U3k takes 1.31 to 1.33. Hiding them by default answers a different question from the
// one being asked.
//
// --patches therefore now describes the default and is kept only so existing scripts and
// agent calls do not fail on an unknown flag.
func addPatchFlags(cmd *cobra.Command, hide, deprecated *bool) {
	cmd.Flags().BoolVar(hide, "hide-patches", false,
		"hide patch releases (the interop site hides them by default; this tool shows them)")
	cmd.Flags().BoolVar(deprecated, "patches", false, "include patch releases")
	_ = cmd.Flags().MarkDeprecated("patches", "patch releases are shown by default; use --hide-patches to hide them")
}

func newProductsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "products",
		Short: "List products in scope and which pairs upstream publishes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gr, err := loadGraph()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			switch g.mode {
			case OutputJSON:
				return emit(cmd, "products", 1, map[string]any{
					"products": productsJSON(gr),
					"pairs":    pairsJSON(gr),
				})
			case OutputCSV:
				return productsCSV(cmd, gr)
			}

			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tPRODUCT\tID\tRELEASES\tFLOOR")
			for _, p := range model.Products {
				floor := p.MinVersion
				if floor == "" {
					floor = "(by reachability)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\n",
					p.Key, p.Name, p.ID, len(gr.ReleasesOf(p.ID)), floor)
			}
			tw.Flush()

			// Counted rather than stated: the number moves whenever a product is added
			// or upstream starts publishing a pair, and a stale count here would be the
			// tool misreporting its own data.
			unpublished := 0
			for _, pr := range model.Pairs() {
				if !gr.Published(pr[0], pr[1]) {
					unpublished++
				}
			}
			fmt.Fprintf(out, "\nPair coverage — %d of %d pairs have no upstream data at all:\n",
				unpublished, len(model.Pairs()))
			tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PAIR\tPUBLISHED")
			for _, pr := range model.Pairs() {
				first, _ := model.ByID(pr[0])
				second, _ := model.ByID(pr[1])
				a, b := model.OrderPair(first, second)
				state := "yes"
				if !gr.Published(pr[0], pr[1]) {
					state = "no — inferred, not verified"
				}
				fmt.Fprintf(tw, "%s × %s\t%s\n", a.Label, b.Label, state)
			}
			return tw.Flush()
		},
	}
}

func newCompatCmd() *cobra.Command {
	var hidePatches, patches bool
	var all bool
	cmd := &cobra.Command{
		Use:   "compat <product> <version>",
		Short: "Show everything compatible with one release",
		Long: `Show everything compatible with one release, grouped by product.

This is the raw pairwise answer. For a whole valid stack from a single pinned version,
use ` + "`vkstack stack`" + ` instead.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			gr, err := loadGraph()
			if err != nil {
				return err
			}
			r, err := gr.Resolve(args[0], args[1])
			if err != nil {
				return err
			}
			opts := graph.CompatOptions{HidePatches: hidePatches}
			if all {
				opts.Statuses = []int{
					store.StatusCompatible, store.StatusIncompatible,
					store.StatusCompatibleNT, store.StatusNotSupported,
				}
			}
			groups := gr.CompatibleWith(r, opts)
			self, _ := model.ByID(r.ProductID)

			switch g.mode {
			case OutputJSON:
				return emit(cmd, "compat", 1, map[string]any{
					"product": self.Key, "version": r.Raw, "groups": groups,
				})
			case OutputCSV:
				return compatCSV(cmd, self, r.Raw, groups)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s %s is compatible with:\n\n", self.Label, r.Raw)
			for _, grp := range groups {
				switch {
				case !grp.Published:
					fmt.Fprintf(out, "  %-12s no data published upstream — inferred only\n", grp.Product.Label+":")
					continue
				case len(grp.Releases) == 0 && grp.HiddenPatches > 0:
					// Every compatible release was a patch, and patches are hidden by
					// default. "nothing compatible" would be false — and it was, for
					// Supervisor builds whose only compatible vCenters are patches.
					fmt.Fprintf(out, "  %-12s %d compatible, all patch releases — drop --hide-patches\n",
						grp.Product.Label+":", grp.HiddenPatches)
					continue
				case len(grp.Releases) == 0:
					fmt.Fprintf(out, "  %-12s nothing compatible\n", grp.Product.Label+":")
					continue
				}
				fmt.Fprintf(out, "  %s:\n", grp.Product.Label)
				for _, hit := range grp.Releases {
					note := ""
					if hit.Status != store.StatusCompatible {
						note = "  (" + statusWord(hit.Status) + ")"
					}
					fmt.Fprintf(out, "    %s%s\n", hit.Release.Raw, note)
				}
			}
			return nil
		},
	}
	addPatchFlags(cmd, &hidePatches, &patches)
	cmd.Flags().BoolVar(&all, "all-statuses", false, "include incompatible and unsupported results")
	return cmd
}

// pinFlags adds one --<product> flag per product and returns the bound values.
func pinFlags(cmd *cobra.Command) map[string]*string {
	vals := map[string]*string{}
	for _, p := range model.Products {
		v := new(string)
		cmd.Flags().StringVar(v, p.Key, "", fmt.Sprintf("%s version", p.Label))
		vals[p.Key] = v
	}
	return vals
}

// optionalKeys names the products `--with` accepts, in model order.
func optionalKeys() []string {
	var out []string
	for _, p := range model.Products {
		if p.Optional {
			out = append(out, p.Key)
		}
	}
	return out
}

// resolveInclude validates the `--with` values and returns them de-duplicated.
//
// Naming a required product is rejected rather than ignored: `--with vcenter` means the
// caller has the wrong model in mind, and silently accepting it would let them believe
// vCenter had been optional all along.
func resolveInclude(with []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range with {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		p, ok := model.ByKey(key)
		switch {
		case !ok:
			return nil, codedErr("unknown_product", ExitUsage,
				"unknown product %q in --with; optional products are %s",
				raw, strings.Join(optionalKeys(), ", "))
		case !p.Optional:
			return nil, codedErr("not_optional", ExitUsage,
				"%s is always part of a stack and cannot be passed to --with; optional products are %s",
				p.Label, strings.Join(optionalKeys(), ", "))
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out, nil
}

func resolvePins(gr *graph.Graph, vals map[string]*string) (map[int]*graph.Release, error) {
	pins := map[int]*graph.Release{}
	for key, raw := range vals {
		if *raw == "" {
			continue
		}
		r, err := gr.Resolve(key, *raw)
		if err != nil {
			return nil, err
		}
		pins[r.ProductID] = r
	}
	return pins, nil
}

func newCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate a fully pinned stack",
		Long: `Validate every pair of a pinned stack.

Exits non-zero if any pair is actively incompatible. Pairs that upstream does not
publish are reported as unverified and do not fail the check — eleven of the 28 pairs
have no data by design.`,
		Args: cobra.NoArgs,
	}
	vals := pinFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		gr, err := loadGraph()
		if err != nil {
			return err
		}
		pins, err := resolvePins(gr, vals)
		if err != nil {
			return err
		}
		if len(pins) < 2 {
			return fmt.Errorf("pin at least two products, e.g. --vcenter 8.0U3k --supervisor v1.32.9")
		}
		res := gr.Check(pins)

		if g.mode == OutputCSV {
			return csvUnavailable("check")
		}
		if g.jsonOut {
			if err := emit(cmd, "check", 1, checkJSON(res)); err != nil {
				return err
			}
		} else {
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PAIR\tVERDICT")
			for _, v := range res.Pairs {
				if !v.Constrains {
					continue
				}
				fmt.Fprintf(tw, "%s %s × %s %s\t%s\n",
					v.A.Label, v.ARelease.Raw, v.B.Label, v.BRelease.Raw, verdictWord(v))
			}
			tw.Flush()

			// Published pairs outside the constraint set are shown apart, so they cannot
			// be read as part of the verdict. VKS runs on the Supervisor, not on vCenter,
			// so a vCenter-VKS mismatch decides nothing; ESX × NSX is a real dependency
			// whose grid is too broad to decide anything either.
			if info := res.Informational(); len(info) > 0 {
				fmt.Fprintln(out, "\nAlso published, but not part of the verdict:")
				tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				for _, v := range info {
					fmt.Fprintf(tw, "  %s %s × %s %s\t%s\n",
						v.A.Label, v.ARelease.Raw, v.B.Label, v.BRelease.Raw, statusWord(v.Status))
				}
				tw.Flush()
			}

			if bad := res.Incompatible(); len(bad) > 0 {
				fmt.Fprintf(out, "\n%d pair(s) incompatible.\n", len(bad))
			} else if un := res.Unverified(); len(un) > 0 {
				fmt.Fprintf(out, "\nStack is valid on every published pair. %d pair(s) could not be verified.\n", len(un))
			} else {
				fmt.Fprintln(out, "\nStack is fully verified compatible.")
			}
		}
		if !res.OK() {
			// A valid question with the answer "no". The verdict has already been
			// written to stdout, so the exit code carries the answer and nothing is
			// printed twice.
			return &CodedError{
				Code: "stack_incompatible", Exit: ExitIncompatible, Silent: true,
				Message: "the pinned stack has incompatible pairs",
				Details: map[string]any{"incompatible": len(res.Incompatible())},
			}
		}
		return nil
	}
	return cmd
}

func verdictWord(v graph.PairVerdict) string {
	switch {
	case !v.Published:
		return "unverified — not published upstream"
	case !v.HasEdge:
		return "unverified — these releases were never evaluated together"
	default:
		return statusWord(v.Status)
	}
}

func newStackCmd() *cobra.Command {
	var limit int
	var hidePatches, patches bool
	var list bool
	var with []string

	cmd := &cobra.Command{
		Use:   "stack [<product> <version>]",
		Short: "Solve a whole valid stack from one or more pinned versions",
		Long: `Solve for complete, valid stacks across the five core products.

Pin as little as one version and the rest is filled in with the newest combination that
is compatible on every published pair:

  vkstack stack vcenter 8.0U3k
  vkstack stack --vcenter 8.0U3k --vks 3.6.2

NSX, Avi and TMC Self-Managed are optional and independent. None is in a stack unless you
pin it or ask for it, and asking for one never brings in another:

  vkstack stack vcenter 9.1.0.0300 --with nsx        # NSX, no Avi
  vkstack stack vcenter 9.1.0.0300 --with avi        # Avi on VDS, no NSX
  vkstack stack vcenter 9.1.0.0300 --with nsx,avi    # both
  vkstack stack --avi 32.1.2                         # a pin is its own opt-in
  vkstack stack vcenter 9.1.0.0300 --with tmc        # TMC Self-Managed on top

Pairs upstream does not publish cannot constrain the answer; they are reported at the
bottom so you know which parts of the stack are verified and which are inferred.`,
		Args: cobra.MaximumNArgs(2),
	}
	vals := pinFlags(cmd)
	cmd.Flags().IntVar(&limit, "limit", 5, "with --list, maximum number of stacks to return")
	addPatchFlags(cmd, &hidePatches, &patches)
	cmd.Flags().BoolVar(&list, "list", false, "list whole stack combinations instead of the recommendation plus alternatives")
	cmd.Flags().StringSliceVar(&with, "with", nil,
		"optional products to include in the stack ("+strings.Join(optionalKeys(), ", ")+"); repeatable or comma-separated")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		gr, err := loadGraph()
		if err != nil {
			return err
		}
		if len(args) == 2 {
			if _, ok := model.ByKey(args[0]); !ok {
				return fmt.Errorf("unknown product %q", args[0])
			}
			*vals[args[0]] = args[1]
		} else if len(args) == 1 {
			return fmt.Errorf("positional form is `vkstack stack <product> <version>`; got only %q", args[0])
		}
		pins, err := resolvePins(gr, vals)
		if err != nil {
			return err
		}
		if len(pins) == 0 {
			return fmt.Errorf("pin at least one version, e.g. `vkstack stack vcenter 8.0U3k`")
		}

		include, err := resolveInclude(with)
		if err != nil {
			return err
		}

		opts := graph.StackOptions{Limit: limit, HidePatches: hidePatches, Include: include}
		if g.mode == OutputCSV {
			return csvUnavailable("stack")
		}
		stacks, failure := gr.Stacks(pins, opts)
		if len(stacks) == 0 {
			// "No stack exists" is an answer to a well-formed question, not a mistake by
			// the caller, so it gets its own exit code and carries what blocked it.
			err := codedErr("no_valid_stack", ExitNoStack, "no valid stack found for those pins")
			switch {
			case failure != nil && failure.PinConflict && failure.Unlisted:
				err = codedErr("pins_never_listed_together", ExitNoStack,
					"upstream publishes %s against %s but never lists those two releases together — they cannot appear in the same stack",
					failure.Against.Label, failure.BlockedProduct.Label)
			case failure != nil && failure.PinConflict:
				err = codedErr("pins_incompatible", ExitNoStack,
					"the pinned %s and %s versions are %s — they cannot appear in the same stack",
					failure.Against.Label, failure.BlockedProduct.Label, statusWord(failure.Status))
			case failure != nil && failure.Against.Key != "":
				err = codedErr("no_candidate", ExitNoStack,
					"no valid stack: no %s release works with the pinned %s",
					failure.BlockedProduct.Label, failure.Against.Label)
			}
			if failure != nil {
				err.Details = map[string]any{
					"blockedProduct": failure.BlockedProduct.Key,
					"against":        failure.Against.Key,
					"pinConflict":    failure.PinConflict,
					"neverListed":    failure.Unlisted,
				}
				err.Hint = "loosen a pin, or drop --hide-patches so patch releases can be chosen"
			}
			return err
		}

		if list {
			if g.jsonOut {
				return emit(cmd, "stacks", 1, map[string]any{"stacks": stacksToJSON(stacks)})
			}
			printStacks(cmd.OutOrStdout(), stacks)
			return nil
		}

		options := gr.ViableOptions(pins, opts)
		if g.jsonOut {
			return emit(cmd, "stack", 1, buildRecommendationJSON(gr, stacks[0], options))
		}
		printRecommendation(cmd.OutOrStdout(), gr, stacks[0], options)
		return nil
	}
	return cmd
}

// printRecommendation shows the newest valid stack, then every alternative each product
// could take while still forming a valid stack.
func printRecommendation(w io.Writer, gr *graph.Graph, best graph.Stack, options map[int][]*graph.Release) {
	fmt.Fprintln(w, "Recommended stack (newest valid combination):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range model.Products {
		r := best.Releases[p.ID]
		if r == nil {
			continue
		}
		marker := ""
		if best.Pinned[p.ID] {
			marker = "  (pinned)"
		}
		fmt.Fprintf(tw, "  %s\t%s%s\n", p.Label, r.Raw, marker)
	}
	tw.Flush()

	// The full alternative lists run to dozens of entries per product, which is
	// unreadable and rarely what anyone wants. Show the newest few and a count.
	const shown = 5
	fmt.Fprintln(w, "\nAlso valid, holding everything else compatible:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	var absent []model.Product
	for _, p := range model.Products {
		if best.Pinned[p.ID] {
			continue
		}
		// An optional product the stack does not contain has no alternatives to list —
		// it has no place in the stack at all. Showing it here as "(no alternative)"
		// read as "there is no NSX that works", which is the opposite of the truth.
		if _, inStack := best.Releases[p.ID]; p.Optional && !inStack {
			absent = append(absent, p)
			continue
		}
		alts := options[p.ID]
		if len(alts) <= 1 {
			fmt.Fprintf(tw, "  %s\t(no alternative)\n", p.Label)
			continue
		}
		names := make([]string, 0, shown)
		for _, r := range alts[:min(shown, len(alts))] {
			names = append(names, r.Raw)
		}
		line := strings.Join(names, ", ")
		if extra := len(alts) - len(names); extra > 0 {
			line += fmt.Sprintf(", +%d older", extra)
		}
		fmt.Fprintf(tw, "  %s\t%s\n", p.Label, line)
	}
	tw.Flush()

	// Say plainly that the optional components were left out, and how to ask for them.
	// Silence here reads as "this stack has no NSX", which is a claim the tool cannot
	// make — it only knows the five components every deployment has.
	if len(absent) > 0 {
		var names, flags []string
		for _, p := range absent {
			names = append(names, p.Label)
			flags = append(flags, "--with "+p.Key)
		}
		note := "optional"
		if len(absent) > 1 {
			note = "optional, and independent of each other"
		}
		fmt.Fprintf(w, "\nNot in this stack: %s — %s. Add with %s.\n",
			strings.Join(names, " and "), note, strings.Join(flags, " or "))
	}

	fmt.Fprintln(w, "\n  (--json lists every option; --list shows whole combinations)")

	// Non-dependency pairs are deliberately absent here. A stack answers "what runs
	// together", and that is decided by the dependency chain alone; a pair like vCenter ×
	// VKr says nothing about it, so surfacing upstream's verdict on one would read as a
	// caveat on an answer it does not qualify. `check` still reports them, because there
	// the caller named a combination and asked what is known about it. Which pairs are
	// left out, and why, is documented rather than repeated per stack.
	if inferred := best.Inferred(); len(inferred) > 0 {
		var names []string
		for _, v := range inferred {
			names = append(names, v.A.Label+" × "+v.B.Label)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "\nNot published upstream, so inferred rather than verified: %s\n",
			strings.Join(names, ", "))
	}
}

// recommendationJSON is the machine-readable form of the default output.
type recommendationJSON struct {
	Recommended map[string]string   `json:"recommended"`
	Pinned      []string            `json:"pinned"`
	Options     map[string][]string `json:"options"`
	Inferred    []string            `json:"inferred"`
	// Omitted names the optional products this stack does not contain, so a caller never
	// has to read absence from `recommended` and guess what it means. Their entries in
	// `options` still list what could join if they were asked for.
	//
	// Absence is not a claim that the deployment lacks them: they were not asked for.
	Omitted []string `json:"omitted"`
}

func buildRecommendationJSON(gr *graph.Graph, best graph.Stack, options map[int][]*graph.Release) recommendationJSON {
	out := recommendationJSON{
		Recommended: map[string]string{},
		Options:     map[string][]string{},
	}
	for _, p := range model.Products {
		if r := best.Releases[p.ID]; r != nil {
			out.Recommended[p.Key] = r.Raw
		} else if p.Optional {
			out.Omitted = append(out.Omitted, p.Key)
		}
		if best.Pinned[p.ID] {
			out.Pinned = append(out.Pinned, p.Key)
		}
		for _, r := range options[p.ID] {
			out.Options[p.Key] = append(out.Options[p.Key], r.Raw)
		}
	}
	for _, v := range best.Inferred() {
		out.Inferred = append(out.Inferred, v.A.Key+"×"+v.B.Key)
	}
	return out
}

func printStacks(w io.Writer, stacks []graph.Stack) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// An optional product no stack contains gets no column at all. A "-" in a row of
	// version strings reads as "nothing compatible", which is the same false negative
	// the recommendation output was fixed to avoid.
	var columns []model.Product
	var absent []model.Product
	for _, p := range model.Products {
		if p.Optional && stacks[0].Releases[p.ID] == nil {
			absent = append(absent, p)
			continue
		}
		columns = append(columns, p)
	}

	header := []string{"#"}
	for _, p := range columns {
		label := p.Label
		if stacks[0].Pinned[p.ID] {
			label += "*"
		}
		header = append(header, strings.ToUpper(label))
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))

	for i, s := range stacks {
		row := []string{fmt.Sprint(i + 1)}
		for _, p := range columns {
			if r := s.Releases[p.ID]; r != nil {
				row = append(row, r.Raw)
			} else {
				row = append(row, "-")
			}
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()

	fmt.Fprintln(w, "\n* pinned")
	if len(absent) > 0 {
		var names, flags []string
		for _, p := range absent {
			names = append(names, p.Label)
			flags = append(flags, "--with "+p.Key)
		}
		note := "optional"
		if len(absent) > 1 {
			note = "optional, and independent of each other"
		}
		fmt.Fprintf(w, "Not in these stacks: %s — %s. Add with %s.\n",
			strings.Join(names, " and "), note, strings.Join(flags, " or "))
	}
	if inferred := stacks[0].Inferred(); len(inferred) > 0 {
		var names []string
		for _, v := range inferred {
			names = append(names, v.A.Label+" × "+v.B.Label)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "inferred, not published upstream: %s\n", strings.Join(names, ", "))
	}
}

// stackJSON is the machine-readable shape, keyed by product short key so consumers do
// not need the product id table.
type stackJSON struct {
	Releases map[string]string `json:"releases"`
	Pinned   []string          `json:"pinned"`
	Inferred []string          `json:"inferred"`
	// Omitted names the optional products this stack does not contain, for the same
	// reason recommendationJSON carries it: absence in `releases` must not be read as a
	// claim that nothing works.
	Omitted []string `json:"omitted"`
}

func stacksToJSON(stacks []graph.Stack) []stackJSON {
	out := make([]stackJSON, 0, len(stacks))
	for _, s := range stacks {
		j := stackJSON{Releases: map[string]string{}}
		for _, p := range model.Products {
			if r := s.Releases[p.ID]; r != nil {
				j.Releases[p.Key] = r.Raw
			} else if p.Optional {
				j.Omitted = append(j.Omitted, p.Key)
			}
			if s.Pinned[p.ID] {
				j.Pinned = append(j.Pinned, p.Key)
			}
		}
		for _, v := range s.Inferred() {
			j.Inferred = append(j.Inferred, v.A.Key+"×"+v.B.Key)
		}
		out = append(out, j)
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// The JSON shapes below are the agent-facing contract. They are written by hand rather
// than reflected off internal structs so that renaming a Go field cannot silently break
// a consumer, and so every field can be given a name that means something on its own.

func productsJSON(gr *graph.Graph) []map[string]any {
	out := make([]map[string]any, 0, len(model.Products))
	for _, p := range model.Products {
		floor := p.MinVersion
		entry := map[string]any{
			"key":          p.Key,
			"name":         p.Name,
			"label":        p.Label,
			"upstreamId":   p.ID,
			"releases":     len(gr.ReleasesOf(p.ID)),
			"upgradeOrder": p.UpgradeOrder,
			"versionFloor": floor,
			// Optional products are absent from a solved stack unless pinned or passed
			// to `--with`. This is how a caller discovers what `--with` accepts.
			"optional": p.Optional,
		}
		if floor == "" {
			// No hardcoded floor: these products are filtered by reachability, so
			// anything that only ever worked with an out-of-scope base drops out.
			entry["versionFloor"] = nil
			entry["floorBy"] = "reachability"
		}
		if len(p.Trains) > 0 {
			entry["trains"] = p.Trains
			entry["trainNote"] = p.TrainNote
		}
		out = append(out, entry)
	}
	return out
}

func pairsJSON(gr *graph.Graph) []map[string]any {
	out := make([]map[string]any, 0, len(model.Pairs()))
	for _, pr := range model.Pairs() {
		first, _ := model.ByID(pr[0])
		second, _ := model.ByID(pr[1])
		a, b := model.OrderPair(first, second)
		out = append(out, map[string]any{
			"a":          a.Key,
			"b":          b.Key,
			"published":  gr.Published(pr[0], pr[1]),
			"dependency": model.Constrains(pr[0], pr[1]),
		})
	}
	return out
}

// checkJSON renders a verdict so a caller can act on it without re-deriving anything: the
// overall answer, then the pairs grouped by what kind of answer they are.
func checkJSON(res graph.CheckResult) map[string]any {
	pair := func(v graph.PairVerdict) map[string]any {
		return map[string]any{
			"a":          v.A.Key,
			"b":          v.B.Key,
			"aVersion":   v.ARelease.Raw,
			"bVersion":   v.BRelease.Raw,
			"dependency": v.Constrains,
			"published":  v.Published,
			"evaluated":  v.HasEdge,
			"status":     v.Status,
			"statusText": statusWord(v.Status),
			"verdict":    verdictWord(v),
			"footnotes":  v.Footnotes,
		}
	}
	all := make([]map[string]any, 0, len(res.Pairs))
	for _, v := range res.Pairs {
		all = append(all, pair(v))
	}
	names := func(vs []graph.PairVerdict) []string {
		out := make([]string, 0, len(vs))
		for _, v := range vs {
			out = append(out, v.A.Label+" × "+v.B.Label)
		}
		return out
	}
	return map[string]any{
		"ok":            res.OK(),
		"pairs":         all,
		"incompatible":  names(res.Incompatible()),
		"unverified":    names(res.Unverified()),
		"informational": names(res.Informational()),
	}
}

// shortDate renders an epoch-ms date, or "-" when upstream published none.
func shortDate(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}
