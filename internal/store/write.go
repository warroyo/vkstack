package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Writer accumulates a full refresh inside one transaction. Nothing is visible until
// Commit, and Commit writes fetched_at last, so an interrupted refresh never leaves a
// cache that looks complete.
type Writer struct {
	tx *sql.Tx

	products     *sql.Stmt
	releases     *sql.Stmt
	compat       *sql.Stmt
	pairCoverage *sql.Stmt
}

// BeginWrite starts a refresh transaction.
func (db *DB) BeginWrite() (*Writer, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("starting refresh transaction: %w", err)
	}
	w := &Writer{tx: tx}
	stmts := []struct {
		dst **sql.Stmt
		sql string
	}{
		{&w.products, `INSERT INTO products(id, name, aliases, short_key) VALUES(?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, aliases=excluded.aliases, short_key=excluded.short_key`},
		{&w.releases, `INSERT INTO releases(id, product_id, version, hybrid_version, release_type, ga_date, tech_guided, gen_guided)
			VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET
				product_id=excluded.product_id,
				-- Matrix responses carry thinner release records than the products call.
				-- Keep whichever value is non-empty so neither source clobbers the other.
				version=COALESCE(NULLIF(excluded.version,''), releases.version),
				hybrid_version=COALESCE(NULLIF(excluded.hybrid_version,''), releases.hybrid_version),
				release_type=COALESCE(NULLIF(excluded.release_type,''), releases.release_type),
				ga_date=CASE WHEN excluded.ga_date > 0 THEN excluded.ga_date ELSE releases.ga_date END,
				tech_guided=excluded.tech_guided,
				gen_guided=excluded.gen_guided`},
		{&w.compat, `INSERT INTO compat(a_release, b_release, status, footnotes) VALUES(?,?,?,?)
			ON CONFLICT(a_release, b_release) DO UPDATE SET status=excluded.status, footnotes=excluded.footnotes`},
		{&w.pairCoverage, `INSERT INTO pair_coverage(a_product, b_product, edge_count, fetched_at) VALUES(?,?,?,?)
			ON CONFLICT(a_product, b_product) DO UPDATE SET edge_count=excluded.edge_count, fetched_at=excluded.fetched_at`},
	}
	for _, s := range stmts {
		st, err := tx.Prepare(s.sql)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("preparing refresh statement: %w", err)
		}
		*s.dst = st
	}
	return w, nil
}

// PutProduct upserts a product.
func (w *Writer) PutProduct(p Product) error {
	if _, err := w.products.Exec(p.ID, p.Name, p.Aliases, p.ShortKey); err != nil {
		return fmt.Errorf("upserting product %d: %w", p.ID, err)
	}
	return nil
}

// PutRelease upserts a release. Safe to call for the same release from both the products
// call and a matrix response; the thinner record will not overwrite good values.
func (w *Writer) PutRelease(r Release) error {
	_, err := w.releases.Exec(r.ID, r.ProductID, r.Version, r.HybridVersion,
		r.ReleaseType, r.GADate, boolToInt(r.TechGuided), boolToInt(r.GenGuided))
	if err != nil {
		return fmt.Errorf("upserting release %d: %w", r.ID, err)
	}
	return nil
}

// PutCompat upserts one compatibility edge. Release ids are normalised so each unordered
// pair is stored exactly once.
func (w *Writer) PutCompat(aRelease, bRelease, status int, footnotes string) error {
	if aRelease == bRelease {
		return nil
	}
	if aRelease > bRelease {
		aRelease, bRelease = bRelease, aRelease
	}
	if _, err := w.compat.Exec(aRelease, bRelease, status, footnotes); err != nil {
		return fmt.Errorf("upserting compat %d/%d: %w", aRelease, bRelease, err)
	}
	return nil
}

// PutPairCoverage records that a product pair was probed, including empty results.
func (w *Writer) PutPairCoverage(aProduct, bProduct, edgeCount int) error {
	if aProduct > bProduct {
		aProduct, bProduct = bProduct, aProduct
	}
	_, err := w.pairCoverage.Exec(aProduct, bProduct, edgeCount, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("recording coverage %d/%d: %w", aProduct, bProduct, err)
	}
	return nil
}

// Commit finalises the refresh and stamps fetched_at.
func (w *Writer) Commit(apiBase string) error {
	now := time.Now().UnixMilli()
	for _, kv := range [][2]string{
		{"api_base", apiBase},
		{"fetched_at", fmt.Sprint(now)},
	} {
		_, err := w.tx.Exec(
			`INSERT INTO meta(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			kv[0], kv[1])
		if err != nil {
			w.tx.Rollback()
			return fmt.Errorf("stamping %s: %w", kv[0], err)
		}
	}
	if err := w.tx.Commit(); err != nil {
		return fmt.Errorf("committing refresh: %w", err)
	}
	return nil
}

// Rollback discards an incomplete refresh.
func (w *Writer) Rollback() error { return w.tx.Rollback() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
