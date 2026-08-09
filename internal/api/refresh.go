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
// run never leaves a cache that looks complete. It probes all ten product pairs rather
// than the seven currently known to be populated: upstream coverage can change, and
// recording which pairs are empty is itself useful data.
func Refresh(ctx context.Context, c *Client, db *store.DB, progress Progress) error {
	if progress == nil {
		progress = func(int, int, string) {}
	}
	pairs := model.Pairs()
	total := 1 + len(pairs)

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

	if err := w.Commit(c.Base); err != nil {
		return err
	}
	progress(total, total, "done")
	return nil
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
