// Package store is the durable local cache: a SQLite mirror of what the upstream interop
// API returned. It deliberately does no filtering and no query logic — the supported
// version floor and every compatibility question are applied in memory by internal/graph,
// so changing them never requires a refetch.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary stays static
)

const schemaVersion = "1"

// Status codes as published by the interop API.
const (
	StatusCompatible   = 1
	StatusIncompatible = 2
	StatusCompatibleNT = 3 // compatible, not tested
	StatusNotSupported = 4
)

// Product is one cached product row.
type Product struct {
	ID       int
	Name     string
	Aliases  string
	ShortKey string
}

// Release is one cached release row. HybridVersion is the version to display and parse;
// Version carries an upstream " - <product name>" suffix and is kept only for reference.
type Release struct {
	ID            int
	ProductID     int
	Version       string
	HybridVersion string
	ReleaseType   string
	GADate        int64 // epoch ms
	TechGuided    bool
	GenGuided     bool
}

// Compat is one cached compatibility edge, stored canonically with ARelease < BRelease.
type Compat struct {
	ARelease  int
	BRelease  int
	Status    int
	Footnotes string
}

// PairCoverage records that a product pair was probed and what came back. Empty pairs get
// a row too, so "upstream publishes nothing here" is a stored fact rather than an
// inference from missing data.
type PairCoverage struct {
	AProduct  int
	BProduct  int
	EdgeCount int
	FetchedAt int64
}

// DB is an open cache.
type DB struct {
	sql  *sql.DB
	path string
}

// DefaultPath returns the cache location, honouring XDG_CACHE_HOME.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating home directory: %w", err)
		}
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "interop", "interop.db"), nil
}

// Open opens (creating if needed) the cache at path and applies the schema.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}
	// busy_timeout keeps a concurrent `serve` and `refresh` from failing outright.
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("opening cache %s: %w", path, err)
	}
	db := &DB{sql: sqlDB, path: path}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Path returns the on-disk location of the cache.
func (db *DB) Path() string { return db.path }

// Close releases the underlying handle.
func (db *DB) Close() error { return db.sql.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS products(
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  aliases   TEXT,
  short_key TEXT);

CREATE TABLE IF NOT EXISTS releases(
  id             INTEGER PRIMARY KEY,
  product_id     INTEGER NOT NULL,
  version        TEXT,
  hybrid_version TEXT,
  release_type   TEXT,
  ga_date        INTEGER,
  tech_guided    INTEGER,
  gen_guided     INTEGER);

CREATE TABLE IF NOT EXISTS compat(
  a_release INTEGER NOT NULL,
  b_release INTEGER NOT NULL,
  status    INTEGER NOT NULL,
  footnotes TEXT,
  PRIMARY KEY(a_release, b_release));

CREATE TABLE IF NOT EXISTS pair_coverage(
  a_product  INTEGER NOT NULL,
  b_product  INTEGER NOT NULL,
  edge_count INTEGER NOT NULL,
  fetched_at INTEGER NOT NULL,
  PRIMARY KEY(a_product, b_product));

CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);

CREATE INDEX IF NOT EXISTS idx_releases_product ON releases(product_id);
CREATE INDEX IF NOT EXISTS idx_compat_b ON compat(b_release);
`

func (db *DB) migrate() error {
	if _, err := db.sql.Exec(schema); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}
	got, err := db.Meta("schema_version")
	if err != nil {
		return err
	}
	if got == "" {
		return db.SetMeta("schema_version", schemaVersion)
	}
	if got != schemaVersion {
		return fmt.Errorf("cache at %s uses schema version %s, this build expects %s — run `interop cache clear`",
			db.path, got, schemaVersion)
	}
	return nil
}

// Meta reads a metadata value, returning "" if absent.
func (db *DB) Meta(key string) (string, error) {
	var v string
	err := db.sql.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading meta %q: %w", key, err)
	}
	return v, nil
}

// SetMeta writes a metadata value.
func (db *DB) SetMeta(key, value string) error {
	_, err := db.sql.Exec(
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("writing meta %q: %w", key, err)
	}
	return nil
}

// FetchedAt returns when the cache was last completely refreshed. A zero time means the
// cache has never been populated — refresh writes this key last, so a partial fetch never
// looks complete.
func (db *DB) FetchedAt() (time.Time, error) {
	v, err := db.Meta("fetched_at")
	if err != nil || v == "" {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing fetched_at %q: %w", v, err)
	}
	return time.UnixMilli(ms), nil
}

// Counts returns the number of rows in each populated table, for `cache info`.
func (db *DB) Counts() (map[string]int, error) {
	out := map[string]int{}
	for _, t := range []string{"products", "releases", "compat", "pair_coverage"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM ` + t).Scan(&n); err != nil {
			return nil, fmt.Errorf("counting %s: %w", t, err)
		}
		out[t] = n
	}
	return out, nil
}
