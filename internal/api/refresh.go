package api

import (
	"context"
	"fmt"

	"github.com/warroyo/vkstack/internal/model"
	"github.com/warroyo/vkstack/internal/store"
)

// Progress reports refresh steps to the caller (a CLI line, or an SSE frame).
type Progress func(step, total int, message string)

// Refresh replaces the cache contents with a fresh pull from upstream.
//
// The whole refresh runs in one transaction and stamps fetched_at last, so an interrupted
// run never leaves a cache that looks complete. It probes all 28 product pairs rather
// than the 17 currently known to be populated: upstream coverage can change, and
// recording which pairs are empty is itself useful data.
func Refresh(ctx context.Context, c *Client, db *store.DB, progress Progress) error {
	if progress == nil {
		progress = func(int, int, string) {}
	}
	pairs := model.Pairs()
	total := 2 + len(pairs)

	// This first call is also what discovers the auth key: the client has none until a
	// request needs one, and a rotation is handled inside the client, not here.
	progress(1, total, "fetching product catalogue")
	products, err := c.Products(ctx)
	if err != nil {
		return err
	}

	wanted := map[int]bool{}
	for _, id := range model.IDs() {
		wanted[id] = true
	}

	w, err := db.BeginWrite()
	if err != nil {
		return err
	}
	defer w.Rollback() //nolint:errcheck // no-op once Commit has succeeded

	found := map[int]bool{}
	for _, p := range products {
		if !wanted[p.ID] {
			continue
		}
		found[p.ID] = true
		mp, _ := model.ByID(p.ID)
		if err := w.PutProduct(store.Product{
			ID: p.ID, Name: p.Name, Aliases: p.Aliases, ShortKey: mp.Key,
		}); err != nil {
			return err
		}
		releases := p.Releases
		if len(releases) == 0 {
			// Fall back to the per-product endpoint if the catalogue omitted them.
			releases, err = c.ProductReleases(ctx, p.ID)
			if err != nil {
				return err
			}
		}
		for _, r := range releases {
			if err := w.PutRelease(toStoreRelease(r, p.ID)); err != nil {
				return err
			}
		}
	}
	for id := range wanted {
		if !found[id] {
			return fmt.Errorf("product %d is in scope but was not present in the upstream catalogue", id)
		}
	}

	for i, pair := range pairs {
		a, _ := model.ByID(pair[0])
		b, _ := model.ByID(pair[1])
		progress(i+2, total, fmt.Sprintf("fetching %s × %s", a.Label, b.Label))

		res, err := c.Matrix(ctx, pair[0], pair[1])
		if err != nil {
			return fmt.Errorf("fetching %s × %s: %w", a.Label, b.Label, err)
		}

		// The matrix returns releases the products call does not (ESX 111 vs 102,
		// vCenter 137 vs 123, Supervisor 158 vs 139). Upsert them or the edges below
		// reference rows that do not exist.
		for _, r := range res.Releases {
			if !wanted[r.ProductID] {
				continue
			}
			if err := w.PutRelease(toStoreRelease(r, r.ProductID)); err != nil {
				return err
			}
		}
		for _, e := range res.Edges {
			if err := w.PutCompat(e.ColumnRelease, e.RowRelease, e.Status, e.Footnotes); err != nil {
				return err
			}
		}
		if err := w.PutPairCoverage(pair[0], pair[1], len(res.Edges)); err != nil {
			return err
		}
	}

	progress(total-1, total, "fetching published support dates")
	if err := refreshLifecycle(ctx, c.lifecycleClient(), w, progress, total); err != nil {
		// A lifecycle failure must not cost us a good matrix refresh. The previous rows
		// stay in place and every release falls back to the matrix flags.
		progress(total-1, total, fmt.Sprintf("skipping support dates: %v", err))
	}

	if err := w.Commit(c.Base); err != nil {
		return err
	}
	progress(total, total, "done")
	return nil
}

func (c *Client) lifecycleClient() *LifecycleClient {
	if c.Lifecycle != nil {
		return c.Lifecycle
	}
	return NewLifecycle("")
}

// refreshLifecycle mirrors published support dates for every product the portal covers.
//
// Rows are stored unfiltered by version, keeping this package's habit of mirroring
// upstream verbatim and leaving the supported-version floor to internal/graph, so moving
// the floor never requires a refetch.
func refreshLifecycle(ctx context.Context, lc *LifecycleClient, w *store.Writer, progress Progress, total int) error {
	for _, p := range model.Products {
		if !p.Lifecycle.Sourced() {
			continue
		}
		rows, err := lc.Rows(ctx, p.Lifecycle.ProductName, p.Lifecycle.Description)
		if err != nil {
			return fmt.Errorf("fetching %s support dates: %w", p.Label, err)
		}

		kept, conflicts := dedupeLifecycle(rows, p)
		if err := w.ClearLifecycle(p.Key); err != nil {
			return err
		}
		for _, l := range kept {
			if err := w.PutLifecycle(l); err != nil {
				return err
			}
		}
		if conflicts > 0 {
			// The portal repeats a release once per portfolio bucket it ships in, and
			// those copies have always agreed. A disagreement means the source changed
			// shape and the join needs another look.
			progress(total-1, total, fmt.Sprintf(
				"%s: %d releases published with conflicting dates, kept the first", p.Label, conflicts))
		}
	}
	return nil
}

// dedupeLifecycle collapses the portal's repeated rows to one per release, dropping rows
// belonging to other products and rows carrying no dates at all.
func dedupeLifecycle(rows []LifecycleRow, p model.Product) ([]store.Lifecycle, int) {
	byRelease := map[string]store.Lifecycle{}
	var order []string
	conflicts := 0

	for _, r := range rows {
		if r.Release == "" || !p.Lifecycle.Keeps(r.Description) {
			continue
		}
		l := store.Lifecycle{
			ProductKey: p.Key,
			ReleaseKey: r.Release,
			GADate:     ParseLifecycleDate(r.GADate),
			EOGSDate:   ParseLifecycleDate(r.DropSupport),
			EOTGDate:   ParseLifecycleDate(r.EOLDate),
			Source:     r.ProductName + "|" + r.Description,
		}
		if l.GADate == 0 && l.EOGSDate == 0 && l.EOTGDate == 0 {
			continue // a listing with no published dates says nothing
		}
		prev, seen := byRelease[r.Release]
		if !seen {
			byRelease[r.Release] = l
			order = append(order, r.Release)
			continue
		}
		if prev.GADate != l.GADate || prev.EOGSDate != l.EOGSDate || prev.EOTGDate != l.EOTGDate {
			conflicts++
		}
	}

	out := make([]store.Lifecycle, 0, len(order))
	for _, rel := range order {
		out = append(out, byRelease[rel])
	}
	return out, conflicts
}

func toStoreRelease(r Release, productID int) store.Release {
	if r.ProductID != 0 {
		productID = r.ProductID
	}
	hybrid := r.HybridVersion
	if hybrid == "" {
		hybrid = r.Version
	}
	return store.Release{
		ID:            r.ID,
		ProductID:     productID,
		Version:       r.Version,
		HybridVersion: hybrid,
		ReleaseType:   r.ReleaseType,
		GADate:        r.GADate,
		TechGuided:    r.TechGuided,
		GenGuided:     r.GenGuided,
	}
}
