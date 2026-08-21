package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/api"
)

func newRefreshCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Pull the latest compatibility data from the interop API into the local cache",
		Long: `Pull the latest compatibility data into the local cache.

Refresh is never automatic — the cache is only updated when you ask. All 28 product
pairs are probed, including the seven upstream does not publish, so coverage gaps are
recorded rather than inferred from missing data.

Expect this to take a minute or two and move roughly 40 MB.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openCache()
			if err != nil {
				return err
			}
			defer db.Close()

			if !force {
				if at, err := db.FetchedAt(); err == nil && !at.IsZero() && time.Since(at) < time.Hour {
					if g.jsonOut {
						return emit(cmd, "refresh", 1, map[string]any{
							"refreshed": false,
							"reason":    "cache is recent",
							"fetchedAt": at.Format(time.RFC3339),
							"hint":      "pass --force to refresh anyway",
						})
					}
					fmt.Fprintf(cmd.OutOrStdout(),
						"cache was refreshed %s ago; use --force to refresh anyway\n",
						time.Since(at).Round(time.Minute))
					return nil
				}
			}

			client := api.New(g.apiBase, g.authKey)
			start := time.Now()
			err = api.Refresh(cmd.Context(), client, db, func(step, total int, msg string) {
				fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", step, total, msg)
			})
			if err != nil {
				return err
			}

			counts, err := db.Counts()
			if err != nil {
				return err
			}
			if g.jsonOut {
				return emit(cmd, "refresh", 1, map[string]any{
					"refreshed": true,
					"took":      time.Since(start).Round(time.Second).String(),
					"counts":    counts,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"refreshed in %s: %d products, %d releases, %d compatibility edges\n",
				time.Since(start).Round(time.Second),
				counts["products"], counts["releases"], counts["compat"])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "refresh even if the cache is recent")
	return cmd
}
