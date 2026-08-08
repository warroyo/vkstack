package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/warroyo/interop-visualizer/internal/graph"
	"github.com/warroyo/interop-visualizer/internal/model"
)

// handleGraph renders a focused neighbourhood of the real compatibility data as mermaid.
//
// Mermaid rather than a graph library: focus mode keeps the subgraph to a few dozen
// nodes, so a layout engine buys little, and reusing one renderer keeps the data graph
// visually consistent with the conceptual whiteboard.
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	g, err := s.load()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	q := r.URL.Query()
	productKey, versionStr := q.Get("product"), q.Get("version")
	if productKey == "" || versionStr == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("product and version are required"))
		return
	}
	focus, err := g.Resolve(productKey, versionStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	perProduct := 6
	if n := q.Get("limit"); n != "" {
		fmt.Sscanf(n, "%d", &perProduct)
	}

	groups := g.CompatibleWith(focus, graph.CompatOptions{IncludePatches: q.Get("patches") == "true"})

	var b strings.Builder
	b.WriteString("flowchart LR\n")

	focusProduct, _ := model.ByID(focus.ProductID)
	focusID := "F"
	fmt.Fprintf(&b, "    %s[\"<b>%s</b><br/>%s\"]\n", focusID, focusProduct.Label, escape(focus.Raw))

	var unpublished []string
	n := 0
	for _, grp := range groups {
		if !grp.Published {
			unpublished = append(unpublished, grp.Product.Label)
			continue
		}
		if len(grp.Releases) == 0 {
			continue
		}
		// One subgraph per product keeps the layers legible left to right.
		fmt.Fprintf(&b, "    subgraph %s[\"%s\"]\n", "S"+grp.Product.Key, grp.Product.Label)
		shown := grp.Releases
		extra := 0
		if len(shown) > perProduct {
			extra = len(shown) - perProduct
			shown = shown[:perProduct]
		}
		var ids []string
		for _, hit := range shown {
			n++
			id := fmt.Sprintf("N%d", n)
			ids = append(ids, id)
			fmt.Fprintf(&b, "        %s[\"%s\"]\n", id, escape(hit.Release.Raw))
		}
		if extra > 0 {
			n++
			id := fmt.Sprintf("N%d", n)
			fmt.Fprintf(&b, "        %s[\"+%d older\"]\n", id, extra)
		}
		b.WriteString("    end\n")
		for i, hit := range shown {
			arrow := "---"
			if hit.Status == 3 {
				arrow = "-.-" // compatible but not tested
			}
			fmt.Fprintf(&b, "    %s %s %s\n", focusID, arrow, ids[i])
		}
	}

	b.WriteString("\n    classDef focus fill:#fde9c8,stroke:#b8791f,stroke-width:4px,color:#3d2708\n")
	fmt.Fprintf(&b, "    class %s focus\n", focusID)

	note := ""
	if len(unpublished) > 0 {
		note = "No published data against " + strings.Join(unpublished, " or ") +
			" — those relationships can only be inferred."
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mermaid": b.String(),
		"note":    note,
		"focus":   map[string]string{"product": focusProduct.Label, "version": focus.Raw},
	})
}

// escape neutralises the characters mermaid treats as syntax inside a label.
func escape(s string) string {
	r := strings.NewReplacer(`"`, "'", "[", "(", "]", ")", "{", "(", "}", ")", "|", "/")
	return r.Replace(s)
}
