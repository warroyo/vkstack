// Package cli wires the cobra command tree.
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/interop-visualizer/internal/store"
)

// Global flags shared by every command.
type globals struct {
	cachePath   string
	apiBase     string
	authKey     string
	jsonOut     bool
	csvOut      bool
	allVersions bool
	minVersions []string
	maxAge      time.Duration
}

var g globals

// NewRoot builds the command tree.
func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "interop",
		Short: "Compatibility and upgrade planning across vCenter, ESX, Supervisor, VKS and VKr",
		Long: strings.TrimSpace(`
Compatibility and upgrade planning across vCenter, ESX, Supervisor, VKS and VKr.

Data comes from the Broadcom Product Interoperability Matrix and is cached locally.
Run ` + "`interop refresh`" + ` once to populate the cache, then everything else works offline.

Start with ` + "`interop explain`" + ` for how the pieces fit together, or
` + "`interop stack vcenter 8.0U3k`" + ` to get a whole valid stack from one pinned version.`),
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.cachePath, "cache", "", "path to the local cache (default ~/.cache/interop/interop.db)")
	pf.StringVar(&g.apiBase, "api-base", "", "override the interop API base URL")
	pf.StringVar(&g.authKey, "auth-key", "", "override the interop API auth key")
	pf.BoolVar(&g.jsonOut, "json", false, "emit JSON")
	pf.BoolVar(&g.csvOut, "csv", false, "emit CSV")
	pf.BoolVar(&g.allVersions, "all-versions", false, "ignore the supported-version floor")
	pf.StringArrayVar(&g.minVersions, "min-version", nil,
		"override a product's version floor, e.g. --min-version vcenter=9.0.0.0 (repeatable)")
	pf.DurationVar(&g.maxAge, "max-age", 7*24*time.Hour, "warn when the cache is older than this")

	root.AddCommand(
		newExplainCmd(),
		newRefreshCmd(),
		newProductsCmd(),
		newReleasesCmd(),
		newStackCmd(),
		newCompatCmd(),
		newCheckCmd(),
		newCacheCmd(),
	)
	return root
}

// openCache opens the local cache, creating it if absent.
func openCache() (*store.DB, error) {
	path := g.cachePath
	if path == "" {
		var err error
		path, err = store.DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	return store.Open(path)
}

// warnIfStale prints a one-line staleness note to stderr. Refresh is never automatic.
func warnIfStale(db *store.DB) error {
	at, err := db.FetchedAt()
	if err != nil {
		return err
	}
	if at.IsZero() {
		return fmt.Errorf("no cached data yet — run `interop refresh` first")
	}
	if age := time.Since(at); age > g.maxAge {
		fmt.Fprintf(os.Stderr, "warning: cache is %s old (last refreshed %s) — run `interop refresh` to update\n",
			age.Round(time.Hour), at.Format(time.RFC3339))
	}
	return nil
}
