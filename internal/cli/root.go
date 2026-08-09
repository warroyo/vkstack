// Package cli wires the cobra command tree.
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/store"
)

// Global flags shared by every command.
type globals struct {
	cachePath   string
	apiBase     string
	authKey     string
	humanOut    bool
	jsonOut     bool
	csvOut      bool
	allVersions bool
	minVersions []string
	maxAge      time.Duration
	// mode is the resolved output mode, decided once in PersistentPreRun.
	mode OutputMode
}

var g globals

// NewRoot builds the command tree.
func NewRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "vkstack",
		Short: "Compatibility across vCenter, ESX, Supervisor, VKS and VKr",
		Long: strings.TrimSpace(`
Compatibility across vCenter, ESX, Supervisor, VKS and VKr.

Data comes from the Broadcom Product Interoperability Matrix and is cached locally.
Run ` + "`vkstack refresh`" + ` once to populate the cache, then everything else works offline.

Start with ` + "`vkstack explain`" + ` for how the pieces fit together, or
` + "`vkstack stack vcenter 8.0U3k`" + ` to get a whole valid stack from one pinned version.`),
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Output mode is resolved once, before any command runs, so every command and
		// the error reporter agree on who is reading.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			g.mode = resolveMode()
			// Existing commands branch on jsonOut; keep that flag meaning "render for a
			// program" now that it is the default.
			g.jsonOut = g.mode == OutputJSON
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.cachePath, "cache", "", "path to the local cache (default ~/.cache/vkstack/vkstack.db)")
	pf.StringVar(&g.apiBase, "api-base", "", "override the interop API base URL")
	pf.StringVar(&g.authKey, "auth-key", "", "override the interop API auth key")
	pf.BoolVar(&g.humanOut, "human", false, "render tables and prose for a person instead of JSON")
	pf.BoolVar(&g.jsonOut, "json", false, "emit JSON (the default; kept so scripts can be explicit)")
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
		newServeCmd(),
		newStaticCmd(),
		newCacheCmd(),
		newDescribeCmd(),
		newMCPCmd(),
	)
	return root
}

// resolveMode decides who the output is for.
//
// JSON is the default because this CLI is for programs: people have the web UI, which
// answers the same questions better than any table can. --human brings the tables back,
// and VKSTACK_OUTPUT sets a default for a shell where a person is doing the typing.
func resolveMode() OutputMode {
	switch {
	case g.humanOut:
		return OutputHuman
	case g.csvOut:
		return OutputCSV
	case g.jsonOut:
		return OutputJSON
	}
	switch strings.ToLower(os.Getenv("VKSTACK_OUTPUT")) {
	case "human", "text", "table":
		return OutputHuman
	case "csv":
		return OutputCSV
	}
	return OutputJSON
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
		return fmt.Errorf("no cached data yet — run `vkstack refresh` first")
	}
	if age := time.Since(at); age > g.maxAge {
		fmt.Fprintf(os.Stderr, "warning: cache is %s old (last refreshed %s) — run `vkstack refresh` to update\n",
			age.Round(time.Hour), at.Format(time.RFC3339))
	}
	return nil
}
