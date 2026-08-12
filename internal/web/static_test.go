package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A UI fix that ships but stays invisible behind a stale cache is indistinguishable from a
// UI fix that never shipped, so the validator is worth a test of its own.
func TestStaticAssetsCarryAValidator(t *testing.T) {
	assets, err := AssetsFS()
	if err != nil {
		t.Fatalf("AssetsFS: %v", err)
	}
	h, err := staticHandler(assets)
	if err != nil {
		t.Fatalf("staticHandler: %v", err)
	}

	// "/index.html" is absent deliberately: http.FileServer canonicalises it to "/" with a
	// 301, which TestStaticIndexIsCanonical covers. Browsers ask for "/".
	for _, path := range []string{"/", "/style.css", "/app.js"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", path, rec.Code)
			}
			tag := rec.Header().Get("ETag")
			if tag == "" {
				t.Fatalf("GET %s sent no ETag; a browser cannot tell a changed file from a cached one", path)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("GET %s Cache-Control = %q, want %q", path, got, "no-cache")
			}

			// The whole point of the validator: the same bytes come back as a 304.
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("If-None-Match", tag)
			again := httptest.NewRecorder()
			h.ServeHTTP(again, req)

			if again.Code != http.StatusNotModified {
				t.Errorf("GET %s with If-None-Match = %d, want 304", path, again.Code)
			}
			if n := again.Body.Len(); n != 0 {
				t.Errorf("304 for %s carried %d bytes of body, want none", path, n)
			}
		})
	}
}

// http.FileServer redirects an explicit /index.html to /, so the page has one cache entry
// rather than two that can disagree with each other.
func TestStaticIndexIsCanonical(t *testing.T) {
	assets, err := AssetsFS()
	if err != nil {
		t.Fatalf("AssetsFS: %v", err)
	}
	h, err := staticHandler(assets)
	if err != nil {
		t.Fatalf("staticHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /index.html = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "./" {
		t.Errorf("GET /index.html redirected to %q, want %q", got, "./")
	}
}

// Two different files must not share a tag, or a browser holding one will refuse to fetch
// the other.
func TestStaticETagsAreContentSpecific(t *testing.T) {
	assets, err := AssetsFS()
	if err != nil {
		t.Fatalf("AssetsFS: %v", err)
	}
	h, err := staticHandler(assets)
	if err != nil {
		t.Fatalf("staticHandler: %v", err)
	}

	tag := func(path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Header().Get("ETag")
	}

	if css, js := tag("/style.css"), tag("/app.js"); css == js {
		t.Errorf("style.css and app.js share the ETag %s", css)
	}

	// A stale tag has to miss, or a changed asset would never be re-fetched.
	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	req.Header.Set("If-None-Match", `"0123456789abcdef0123456789abcdef"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /style.css with a stale If-None-Match = %d, want 200", rec.Code)
	}
}
