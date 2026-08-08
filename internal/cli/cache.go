package cli

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/interop-visualizer/internal/model"
	"github.com/warroyo/interop-visualizer/internal/store"
)

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect or clear the local cache",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newCacheInfoCmd(), newCachePathCmd(), newCacheClearCmd())
	return cmd
}

func newCacheInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show cache location, age, contents and pair coverage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openCache()
			if err != nil {
				return err
			}
			defer db.Close()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "path:    %s\n", db.Path())

			at, err := db.FetchedAt()
			if err != nil {
				return err
			}
			if at.IsZero() {
				fmt.Fprintln(out, "state:   empty — run `interop refresh`")
				return nil
			}
			fmt.Fprintf(out, "fetched: %s (%s ago)\n",
				at.Format(time.RFC3339), time.Since(at).Round(time.Minute))

			counts, err := db.Counts()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "rows:    %d products, %d releases, %d compat edges\n\n",
				counts["products"], counts["releases"], counts["compat"])

			snap, err := db.Load()
			if err != nil {
				return err
			}
			printReleaseCounts(out, snap)
			fmt.Fprintln(out)
			printPairCoverage(out, snap)
			return nil
		},
	}
}

func printReleaseCounts(out interface{ Write([]byte) (int, error) }, snap *store.Snapshot) {
	byProduct := map[int]int{}
	for _, r := range snap.Releases {
		byProduct[r.ProductID]++
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRODUCT\tCACHED RELEASES\tFLOOR")
	for _, p := range model.Products {
		floor := p.MinVersion
		if floor == "" {
			floor = "(by reachability)"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\n", p.Label, byProduct[p.ID], floor)
	}
	tw.Flush()
}

func printPairCoverage(out interface{ Write([]byte) (int, error) }, snap *store.Snapshot) {
	counts := map[[2]int]int{}
	for _, pc := range snap.Coverage {
		counts[normalisePair(pc.AProduct, pc.BProduct)] = pc.EdgeCount
	}

	type row struct {
		pair  string
		count int
	}
	var rows []row
	for _, pair := range model.Pairs() {
		a, _ := model.ByID(pair[0])
		b, _ := model.ByID(pair[1])
		rows = append(rows, row{a.Label + " × " + b.Label, counts[normalisePair(pair[0], pair[1])]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PRODUCT PAIR\tEDGES\tPUBLISHED")
	for _, r := range rows {
		state := "yes"
		if r.count == 0 {
			state = "NO — not published upstream"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\n", r.pair, r.count, state)
	}
	tw.Flush()
}

func newCachePathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the cache file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := g.cachePath
			if path == "" {
				var err error
				if path, err = store.DefaultPath(); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

func newCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Delete the local cache",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := g.cachePath
			if path == "" {
				var err error
				if path, err = store.DefaultPath(); err != nil {
					return err
				}
			}
			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(cmd.OutOrStdout(), "no cache to remove")
					return nil
				}
				return fmt.Errorf("removing cache: %w", err)
			}
			// WAL mode leaves sidecar files; remove them so the next open starts clean.
			for _, suffix := range []string{"-wal", "-shm"} {
				os.Remove(path + suffix)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", path)
			return nil
		},
	}
}
