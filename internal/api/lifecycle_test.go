package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/warroyo/vkstack/internal/model"
)

// TestLifecyclePagerTrustsTotalCountNotSize is the trap this client exists to avoid. The
// portal echoes back the number of rows it returned as "size", not the number asked for,
// so a full page and the last page look identical by that field. Only totalCount says
// whether there is more.
func TestLifecyclePagerTrustsTotalCountNotSize(t *testing.T) {
	pages := [][]LifecycleRow{
		{{Release: "1.32", DropSupport: "2026-02-28 00:00:00.000"}, {Release: "1.33"}},
		{{Release: "1.34"}},
		{},
	}
	var got []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req lifecycleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		if req.ProductLine != "" {
			t.Errorf("productLine = %q, want it always empty", req.ProductLine)
		}
		var page int
		fmt.Sscanf(req.Page, "%d", &page)
		got = append(got, page)
		if page >= len(pages) {
			page = len(pages) - 1
		}
		rows := pages[page]
		resp := lifecycleResponse{Success: true}
		resp.Data.LifecycleList = rows
		resp.Data.Page = page
		resp.Data.Size = len(rows) // the echo that must not be trusted
		resp.Data.TotalCount = 3
		json.NewEncoder(w).Encode(resp) //nolint:errcheck // test server
	}))
	defer srv.Close()

	c := &LifecycleClient{Base: srv.URL, HTTP: srv.Client()}
	rows, err := c.Rows(context.Background(), "vSphere Kubernetes releases", "")
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 — the pager stopped early", len(rows))
	}
	if len(got) != 2 {
		t.Errorf("requested pages %v, want exactly 0 and 1", got)
	}
}

func TestLifecycleReportsFailureResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(lifecycleResponse{Success: false}) //nolint:errcheck // test server
	}))
	defer srv.Close()

	c := &LifecycleClient{Base: srv.URL, HTTP: srv.Client()}
	if _, err := c.Rows(context.Background(), "VMware NSX", ""); err == nil {
		t.Fatal("want an error when the endpoint reports success=false")
	}
}

func TestParseLifecycleDate(t *testing.T) {
	if got := ParseLifecycleDate("2026-02-28 00:00:00.000"); got != 1772236800000 {
		t.Errorf("parsed = %d, want 1772236800000", got)
	}
	for _, s := range []string{"", "not a date", "2026-02-28"} {
		if got := ParseLifecycleDate(s); got != 0 {
			t.Errorf("ParseLifecycleDate(%q) = %d, want 0", s, got)
		}
	}
}

// TestDedupeLifecycleCollapsesPortfolioCopies covers the reason VKS is queried by
// description: the portal repeats each release once per portfolio bucket that ships it,
// and those copies agree.
func TestDedupeLifecycleCollapsesPortfolioCopies(t *testing.T) {
	vks, _ := model.ByKey("vks")
	rows := []LifecycleRow{
		{ProductName: "VMware vSphere Kubernetes Service", Description: "vSphere Kubernetes Service",
			Release: "3.4.2+v1.33", DropSupport: "2026-06-28 00:00:00.000"},
		{ProductName: "Tanzu Kubernetes Runtime", Description: "vSphere Kubernetes Service",
			Release: "3.4.2+v1.33", DropSupport: "2026-06-28 00:00:00.000"},
		{ProductName: "VMware Cloud Foundation", Description: "vSphere Kubernetes Service",
			Release: "3.4.2+v1.33", DropSupport: "2026-06-28 00:00:00.000"},
		// A listing with no dates at all says nothing and should not be stored.
		{ProductName: "VMware Cloud Foundation", Description: "vSphere Kubernetes Service",
			Release: "3.9.9+v1.99"},
	}

	kept, conflicts := dedupeLifecycle(rows, vks)
	if conflicts != 0 {
		t.Errorf("conflicts = %d, want 0 — the copies agree", conflicts)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d rows, want 1: %+v", len(kept), kept)
	}
	if kept[0].ReleaseKey != "3.4.2+v1.33" || kept[0].EOGSDate == 0 {
		t.Errorf("kept = %+v, want the dated 3.4.2+v1.33 row", kept[0])
	}
	if kept[0].ProductKey != "vks" {
		t.Errorf("product key = %q, want vks", kept[0].ProductKey)
	}
}

func TestDedupeLifecycleCountsDisagreementAndFiltersForeignRows(t *testing.T) {
	nsx, _ := model.ByKey("nsx")
	rows := []LifecycleRow{
		{ProductName: "VMware NSX", Description: "VMware NSX",
			Release: "4.2.4.1", DropSupport: "2027-10-11 00:00:00.000"},
		{ProductName: "VMware NSX", Description: "VMware NSX",
			Release: "4.2.4.1", DropSupport: "2028-01-01 00:00:00.000"},
		// HCX rides the NSX productName and must not become an NSX date.
		{ProductName: "VMware NSX", Description: "VMware HCX",
			Release: "4.11.5", DropSupport: "2027-10-11 00:00:00.000"},
	}

	kept, conflicts := dedupeLifecycle(rows, nsx)
	if conflicts != 1 {
		t.Errorf("conflicts = %d, want 1", conflicts)
	}
	if len(kept) != 1 || kept[0].ReleaseKey != "4.2.4.1" {
		t.Fatalf("kept = %+v, want only 4.2.4.1", kept)
	}
	if kept[0].EOGSDate != ParseLifecycleDate("2027-10-11 00:00:00.000") {
		t.Errorf("kept the second copy's date; the first should win")
	}
}
