// Package api is a client for the private JSON API behind the Broadcom Product
// Interoperability Matrix SPA.
//
// There is no login: the SPA ships a static X-Auth-Key in its public JavaScript bundle.
// Both the key and the service URL can be re-derived from that bundle if they rotate —
// see Rediscover.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultBase is the production interop service, as published in the SPA bundle.
	DefaultBase = "https://interop.esp.spespg1.vmw.saas.broadcom.com/external"
	// DefaultAuthKey is public: it is a literal in the SPA's main.<hash>.js.
	DefaultAuthKey = `N31mVcQkL?Q]GSe[Tve0Wl8b[i2_vU:ClohDvU7Ex;GCu4=hxa=q>3B<aMEZRwmT`
	// SiteURL is the SPA itself, used only to re-derive the key and base URL.
	SiteURL = "https://interopmatrix.broadcom.com"
)

// Client talks to the interop API.
type Client struct {
	Base    string
	AuthKey string
	HTTP    *http.Client
}

// New returns a client with the compiled-in defaults. Empty base or key fall back to
// those defaults so callers can pass through flag values unconditionally.
func New(base, authKey string) *Client {
	if base == "" {
		base = DefaultBase
	}
	if authKey == "" {
		authKey = DefaultAuthKey
	}
	return &Client{
		Base:    base,
		AuthKey: authKey,
		// Matrix payloads reach ~8.5 MB, so the timeout has to cover a slow large body.
		HTTP: &http.Client{Timeout: 3 * time.Minute},
	}
}

// Release is a release record as returned by the API. The same shape appears inside the
// products call and, more thinly populated, inside matrix responses.
type Release struct {
	ID            int    `json:"id"`
	ProductID     int    `json:"productId"`
	Version       string `json:"version"`
	HybridVersion string `json:"hybridVersion"`
	ReleaseType   string `json:"releaseType"`
	GADate        int64  `json:"gaDate"`
	TechGuided    bool   `json:"techGuided"`
	GenGuided     bool   `json:"genGuided"`
}

// Product is a product record with its embedded releases.
type Product struct {
	ID       int       `json:"id"`
	Name     string    `json:"name"`
	Aliases  string    `json:"aliases"`
	Releases []Release `json:"releases"`
}

// matrixEntry is one cell block in a matrix response: a column release plus the row
// releases it was evaluated against.
type matrixEntry struct {
	ProductID int    `json:"productId"`
	ID        int    `json:"id"`
	Version   string `json:"version"`
	GADate    int64  `json:"gaDate"`
	Type      string `json:"releaseType"`
	Tech      bool   `json:"techGuided"`
	Gen       bool   `json:"genGuided"`
	Status    int    `json:"status"`
	Footnotes string `json:"footnotes"`
	// RowProdReleaseMap is keyed by row index. One product pair per request means the
	// only key is ever "0".
	RowProdReleaseMap map[string][]matrixEntry `json:"rowProdReleaseMap"`
}

// CompatEdge is one compatibility result between two releases.
type CompatEdge struct {
	ColumnRelease int
	RowRelease    int
	Status        int
	Footnotes     string
}

// MatrixResult is one product pair's worth of compatibility data.
type MatrixResult struct {
	// Published is false when upstream returned a bare {} for this pair, meaning no
	// data exists rather than "nothing is compatible". Three of the ten pairs in scope
	// are unpublished.
	Published bool
	Edges     []CompatEdge
	// Releases are every release seen in the response. The matrix returns releases the
	// products call does not, so these must be upserted or edges dangle.
	Releases []Release
}

func (c *Client) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("X-Auth-Key", c.AuthKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		resp.Body.Close()
		return nil, &HTTPError{Status: resp.StatusCode, URL: url, Body: string(snippet)}
	}
	return resp, nil
}

// HTTPError is a non-200 response. Status is checked by refresh to decide whether to
// attempt key rediscovery.
type HTTPError struct {
	Status int
	URL    string
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("interop API returned %d for %s: %s", e.Status, e.URL, e.Body)
}

// Unauthorized reports whether the failure looks like a rotated auth key.
func (e *HTTPError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Products fetches every product with its embedded releases.
func (c *Client) Products(ctx context.Context) ([]Product, error) {
	url := fmt.Sprintf("%s/products?interoptype=product&time=%d", c.Base, time.Now().UnixMilli())
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var products []Product
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		return nil, fmt.Errorf("decoding products: %w", err)
	}
	return products, nil
}

// ProductReleases fetches one product's releases.
//
// filter_vcf_version must be false: with true the endpoint silently drops older releases
// (VCF returns 14 instead of 29) with no error and no indication.
func (c *Client) ProductReleases(ctx context.Context, productID int) ([]Release, error) {
	url := fmt.Sprintf("%s/product-releases?id=%d&filter_vcf_version=false", c.Base, productID)
	resp, err := c.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var products []Product
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		return nil, fmt.Errorf("decoding releases for product %d: %w", productID, err)
	}
	if len(products) == 0 {
		return nil, nil
	}
	return products[0].Releases, nil
}

// matrixRequest is the body the interop matrix endpoint expects.
//
// Every isHide* key is required: omitting any one returns "400 Missing parameters.".
// They are all sent false because they do not actually filter the payload (status 4 rows
// come back regardless) — they only drive rendering in the SPA. Filtering happens at
// query time against the cache instead.
type matrixRequest struct {
	Columns             []matrixSelector `json:"columns"`
	Rows                []matrixSelector `json:"rows"`
	IsCollection        bool             `json:"isCollection"`
	IsHidePatch         bool             `json:"isHidePatch"`
	IsHideGenSupported  bool             `json:"isHideGenSupported"`
	IsHideTechSupported bool             `json:"isHideTechSupported"`
	IsHideCompatible    bool             `json:"isHideCompatible"`
	IsHideIncompatible  bool             `json:"isHideIncompatible"`
	IsHideNTCompatible  bool             `json:"isHideNTCompatible"`
	IsHideNotSupported  bool             `json:"isHideNotSupported"`
}

type matrixSelector struct {
	Product  int   `json:"product"`
	Releases []int `json:"releases"`
}

// Matrix fetches the compatibility grid for one product pair.
func (c *Client) Matrix(ctx context.Context, columnProduct, rowProduct int) (*MatrixResult, error) {
	body := matrixRequest{
		Columns: []matrixSelector{{Product: columnProduct, Releases: []int{}}},
		Rows:    []matrixSelector{{Product: rowProduct, Releases: []int{}}},
	}
	resp, err := c.do(ctx, http.MethodPost, c.Base+"/products/interoperabilityMatrix", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Payloads reach ~8.5 MB; stream rather than buffering the whole body.
	var raw map[string][]matrixEntry
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding matrix for products %d/%d: %w", columnProduct, rowProduct, err)
	}

	out := &MatrixResult{Published: len(raw) > 0}
	seen := map[int]bool{}
	addRelease := func(e matrixEntry, fallbackProduct int) {
		if e.ID == 0 || seen[e.ID] {
			return
		}
		seen[e.ID] = true
		pid := e.ProductID
		if pid == 0 {
			pid = fallbackProduct
		}
		out.Releases = append(out.Releases, Release{
			ID:        e.ID,
			ProductID: pid,
			// Matrix entries carry the bare version, not the " - <product>" suffixed
			// form, so it doubles as the hybrid version here.
			HybridVersion: e.Version,
			ReleaseType:   e.Type,
			GADate:        e.GADate,
			TechGuided:    e.Tech,
			GenGuided:     e.Gen,
		})
	}

	for _, entries := range raw {
		for _, col := range entries {
			addRelease(col, columnProduct)
			for _, rowGroup := range col.RowProdReleaseMap {
				for _, row := range rowGroup {
					addRelease(row, rowProduct)
					out.Edges = append(out.Edges, CompatEdge{
						ColumnRelease: col.ID,
						RowRelease:    row.ID,
						Status:        row.Status,
						Footnotes:     row.Footnotes,
					})
				}
			}
		}
	}
	return out, nil
}
