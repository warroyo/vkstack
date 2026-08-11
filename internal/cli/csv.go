package cli

import (
	"encoding/csv"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/model"
)

// CSV is for spreadsheets, which is a human errand — an agent wants the JSON. It applies
// only to the commands whose answer is genuinely a table of rows; a solved stack and the
// dependency model are not, and asking for them as CSV gets a clear no rather than a
// flattened shape that quietly loses the parts that matter.

func writeCSV(cmd *cobra.Command, header []string, rows [][]string) error {
	w := csv.NewWriter(cmd.OutOrStdout())
	if err := w.Write(header); err != nil {
		return err
	}
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func csvUnavailable(command string) error {
	return codedErr("csv_unavailable", ExitUsage,
		"%s has no CSV form: its answer is not a table of rows — use the default JSON",
		command)
}

func releasesCSV(cmd *cobra.Command, product string, releases []*graph.Release) error {
	rows := make([][]string, 0, len(releases))
	for _, r := range releases {
		rows = append(rows, []string{
			product, r.Raw, r.ReleaseType, shortDate(r.GADate), shortDate(r.EOGSDate), shortDate(r.EOTGDate),
			string(r.EffectivePhase()), r.EffectivePhase().Label(), string(r.EffectivePhaseSource()),
		})
	}
	return writeCSV(cmd,
		[]string{"product", "version", "type", "ga", "eogs", "eotg", "phase", "phaseLabel", "phaseSource"},
		rows)
}

func compatCSV(cmd *cobra.Command, self model.Product, version string, groups []graph.CompatGroup) error {
	var rows [][]string
	for _, grp := range groups {
		if !grp.Published {
			// A pair with no upstream data is a row of its own, so a spreadsheet cannot
			// read the absence as "nothing is compatible".
			rows = append(rows, []string{
				self.Key, version, grp.Product.Key, "", "", "no data published",
			})
			continue
		}
		for _, hit := range grp.Releases {
			rows = append(rows, []string{
				self.Key, version, grp.Product.Key, hit.Release.Raw,
				statusWord(hit.Status), hit.Footnotes,
			})
		}
	}
	return writeCSV(cmd,
		[]string{"product", "version", "peerProduct", "peerVersion", "status", "notes"}, rows)
}

func productsCSV(cmd *cobra.Command, gr *graph.Graph) error {
	rows := make([][]string, 0, len(model.Products))
	for _, p := range model.Products {
		floor := p.MinVersion
		if floor == "" {
			floor = "(by reachability)"
		}
		rows = append(rows, []string{
			p.Key, p.Name, itoa(p.ID), itoa(len(gr.ReleasesOf(p.ID))), floor, itoa(p.UpgradeOrder),
		})
	}
	return writeCSV(cmd,
		[]string{"key", "name", "upstreamId", "releases", "versionFloor", "upgradeOrder"}, rows)
}

func itoa(n int) string { return strconv.Itoa(n) }
