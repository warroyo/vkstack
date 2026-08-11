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

// LifecycleBase is the Broadcom Product Lifecycle portal's JSON endpoint.
//
// The portal's own HTML requires a login; this endpoint does not, and takes no auth key
// of any kind. It is a different host and a different contract from the interop API, so
// it gets its own client rather than sharing Client's key-rotation machinery.
const LifecycleBase = "https://support.broadcom.com/web/ecx/productlifecycle"

const lifecyclePath = "/-/productLifecycle/getproductlifecycledetail"

// lifecyclePageSize is what each request asks for. The largest bucket in scope is a few
// hundred rows, so this is one round trip per product in practice.
const lifecyclePageSize = 1000

// LifecycleClient reads published support dates from the lifecycle portal.
type LifecycleClient struct {
	Base string
	HTTP *http.Client
}

// NewLifecycle returns a client. An empty base uses LifecycleBase.
func NewLifecycle(base string) *LifecycleClient {
	if base == "" {
		base = LifecycleBase
	}
	return &LifecycleClient{Base: base, HTTP: &http.Client{Timeout: time.Minute}}
}

// LifecycleRow is one published lifecycle record.
//
// The date fields are the portal's own strings ("2026-02-28 00:00:00.000") and are often
// empty. Note what they mean, because the names mislead: DropSupport is end of *general*
// support, and EOL is end of technical guidance — the later of the two.
type LifecycleRow struct {
	ProductName string `json:"productName"`
	Release     string `json:"release"`
	Description string `json:"description"`
	GADate      string `json:"gaDate"`
	DropSupport string `json:"dropSupportDate"`
	EOLDate     string `json:"eolDate"`
}

type lifecycleRequest struct {
	DateFilter    string `json:"dateFilter"`
	Description   string `json:"description"`
	DropSupport   string `json:"dropSupportDate"`
	EOLDate       string `json:"eolDate"`
	GADate        string `json:"gaDate"`
	GenLevel      string `json:"genLevel"`
	IsEntitled    string `json:"isEntitled"`
	OS            string `json:"os"`
	Page          string `json:"page"`
	ProductLine   string `json:"productLine"`
	ProductName   string `json:"productName"`
	Release       string `json:"release"`
	Size          string `json:"size"`
	Sort          string `json:"sort"`
	Stabilization string `json:"stabilizationDate"`
}

type lifecycleResponse struct {
	Success bool `json:"success"`
	Data    struct {
		LifecycleList []LifecycleRow `json:"lifecycleList"`
		Page          int            `json:"page"`
		Size          int            `json:"size"`
		TotalCount    int            `json:"totalCount"`
	} `json:"data"`
}

// Rows fetches every lifecycle row matching an exact productName, an exact description,
// or both. An empty string means "do not filter on this field"; both empty returns the
// whole division-less catalogue, which is large.
//
// productLine is always sent empty on purpose. It is a division code, and a product's
// rows can span divisions — NSX appears under both VC and VA — so scoping by it silently
// drops rows. Scoping it wrongly returns zero rows rather than an error, which is worse.
func (c *LifecycleClient) Rows(ctx context.Context, productName, description string) ([]LifecycleRow, error) {
	var out []LifecycleRow
	for page := 0; ; page++ {
		res, err := c.page(ctx, productName, description, page)
		if err != nil {
			return nil, err
		}
		out = append(out, res.Data.LifecycleList...)

		// Page against totalCount, never against the echoed size: the response's size is
		// the number of rows returned, not the number requested, so a full page and a
		// final short page are indistinguishable by it.
		if len(res.Data.LifecycleList) == 0 || len(out) >= res.Data.TotalCount {
			return out, nil
		}
	}
}

func (c *LifecycleClient) page(ctx context.Context, productName, description string, page int) (*lifecycleResponse, error) {
	// Every field is sent, empty where unused: the endpoint rejects a partial body.
	body, err := json.Marshal(lifecycleRequest{
		IsEntitled:  "false",
		Page:        fmt.Sprint(page),
		Size:        fmt.Sprint(lifecyclePageSize),
		Sort:        "productName,asc",
		ProductName: productName,
		Description: description,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding lifecycle request: %w", err)
	}

	url := c.Base + lifecyclePath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, &HTTPError{Status: resp.StatusCode, URL: url, Body: string(snippet)}
	}

	var out lifecycleResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding lifecycle response: %w", err)
	}
	if !out.Success {
		return nil, fmt.Errorf("lifecycle endpoint reported failure for productName=%q description=%q", productName, description)
	}
	return &out, nil
}

// lifecycleDateLayout is how the portal writes its dates.
const lifecycleDateLayout = "2006-01-02 15:04:05.000"

// ParseLifecycleDate converts a portal date to epoch ms, matching the convention the
// cache already uses for gaDate. Empty and unparseable values return 0, which reads as
// "not published" everywhere downstream.
func ParseLifecycleDate(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(lifecycleDateLayout, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
