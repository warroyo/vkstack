package store

import (
	"fmt"
)

// Snapshot is everything the cache holds, loaded in one pass. The dataset is small
// (a few hundred releases, tens of thousands of edges), so every command reads it whole
// and answers from memory rather than issuing SQL per query.
type Snapshot struct {
	Products []Product
	Releases []Release
	Compat   []Compat
	Coverage []PairCoverage
	// FetchedAt is when the cache was last completely refreshed, in epoch ms.
	FetchedAt int64
}

// Load reads the entire cache.
func (db *DB) Load() (*Snapshot, error) {
	s := &Snapshot{}

	rows, err := db.sql.Query(`SELECT id, name, COALESCE(aliases,''), COALESCE(short_key,'') FROM products`)
	if err != nil {
		return nil, fmt.Errorf("loading products: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Aliases, &p.ShortKey); err != nil {
			return nil, fmt.Errorf("scanning product: %w", err)
		}
		s.Products = append(s.Products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("loading products: %w", err)
	}

	relRows, err := db.sql.Query(`SELECT id, product_id, COALESCE(version,''), COALESCE(hybrid_version,''),
		COALESCE(release_type,''), COALESCE(ga_date,0), COALESCE(tech_guided,0), COALESCE(gen_guided,0) FROM releases`)
	if err != nil {
		return nil, fmt.Errorf("loading releases: %w", err)
	}
	defer relRows.Close()
	for relRows.Next() {
		var r Release
		var tech, gen int
		if err := relRows.Scan(&r.ID, &r.ProductID, &r.Version, &r.HybridVersion,
			&r.ReleaseType, &r.GADate, &tech, &gen); err != nil {
			return nil, fmt.Errorf("scanning release: %w", err)
		}
		r.TechGuided, r.GenGuided = tech != 0, gen != 0
		s.Releases = append(s.Releases, r)
	}
	if err := relRows.Err(); err != nil {
		return nil, fmt.Errorf("loading releases: %w", err)
	}

	cRows, err := db.sql.Query(`SELECT a_release, b_release, status, COALESCE(footnotes,'') FROM compat`)
	if err != nil {
		return nil, fmt.Errorf("loading compat: %w", err)
	}
	defer cRows.Close()
	for cRows.Next() {
		var c Compat
		if err := cRows.Scan(&c.ARelease, &c.BRelease, &c.Status, &c.Footnotes); err != nil {
			return nil, fmt.Errorf("scanning compat: %w", err)
		}
		s.Compat = append(s.Compat, c)
	}
	if err := cRows.Err(); err != nil {
		return nil, fmt.Errorf("loading compat: %w", err)
	}

	covRows, err := db.sql.Query(`SELECT a_product, b_product, edge_count, fetched_at FROM pair_coverage`)
	if err != nil {
		return nil, fmt.Errorf("loading pair coverage: %w", err)
	}
	defer covRows.Close()
	for covRows.Next() {
		var pc PairCoverage
		if err := covRows.Scan(&pc.AProduct, &pc.BProduct, &pc.EdgeCount, &pc.FetchedAt); err != nil {
			return nil, fmt.Errorf("scanning pair coverage: %w", err)
		}
		s.Coverage = append(s.Coverage, pc)
	}
	if err := covRows.Err(); err != nil {
		return nil, fmt.Errorf("loading pair coverage: %w", err)
	}

	at, err := db.FetchedAt()
	if err != nil {
		return nil, err
	}
	s.FetchedAt = at.UnixMilli()

	return s, nil
}
