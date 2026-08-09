package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// rotatingSite serves a bundle whose key changes on every fetch, so a test can simulate
// the key rotating out from under a run in progress.
func rotatingSite(t *testing.T, keys ...string) (site string, fetches *int32) {
	t.Helper()
	var n int32
	mux := http.NewServeMux()
	mux.HandleFunc("/Interoperability", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><script src="main.7d3f9a1c.js"></script></body></html>`)
	})
	mux.HandleFunc("/main.7d3f9a1c.js", func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(keys) {
			i = len(keys) - 1
		}
		fmt.Fprintf(w, `x={"X-Auth-Key":%q};simServiceUrl:"https://interop.esp.example/external"`, keys[i])
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// apiServer accepts exactly one key and 401s everything else, like the real service does
// once a key has rotated.
func apiServer(t *testing.T, accept string, calls *int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.Header.Get("X-Auth-Key") != accept {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// The matrix endpoint returns an object; the product endpoints return an array.
		if r.URL.Path == "/products/interoperabilityMatrix" {
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestDiscoversKeyOnFirstRequest covers the normal path: a client is built with no key at
// all, and the first call derives one.
func TestDiscoversKeyOnFirstRequest(t *testing.T) {
	site, bundleFetches := rotatingSite(t, "live-key")
	var calls int32
	c := New(apiServer(t, "live-key", &calls), "")
	c.site = site

	if c.AuthKey != "" {
		t.Fatalf("a new client must start with no key, got %q", c.AuthKey)
	}
	if _, err := c.Products(context.Background()); err != nil {
		t.Fatalf("Products: %v", err)
	}
	if c.AuthKey != "live-key" {
		t.Errorf("AuthKey = %q, want the discovered live-key", c.AuthKey)
	}

	// A second call reuses the key: discovery is memoised per client, so a refresh pays
	// for the bundle once, not once per request.
	if _, err := c.ProductReleases(context.Background(), 2); err != nil {
		t.Fatalf("ProductReleases: %v", err)
	}
	if *bundleFetches != 1 {
		t.Errorf("bundle fetched %d times, want 1", *bundleFetches)
	}
}

// TestRetriesOnceWhenKeyRotatesMidRun is the gap the old refresh-level handling left open:
// only the first call of a refresh could recover. Now any call can.
func TestRetriesOnceWhenKeyRotatesMidRun(t *testing.T) {
	site, bundleFetches := rotatingSite(t, "stale-key", "fresh-key")
	var calls int32
	c := New(apiServer(t, "fresh-key", &calls), "")
	c.site = site

	if _, err := c.Matrix(context.Background(), 1, 2); err != nil {
		t.Fatalf("Matrix should have recovered from the rotation: %v", err)
	}
	if c.AuthKey != "fresh-key" {
		t.Errorf("AuthKey = %q, want fresh-key after the retry", c.AuthKey)
	}
	if *bundleFetches != 2 {
		t.Errorf("bundle fetched %d times, want 2 (initial discovery, then the retry)", *bundleFetches)
	}
	if calls != 2 {
		t.Errorf("API called %d times, want 2 (the rejected call, then the retry)", calls)
	}
}

// TestGivesUpAfterOneRetry: a second rejection is a real failure, not a race. Retrying
// further would hammer both the SPA and the API.
func TestGivesUpAfterOneRetry(t *testing.T) {
	site, _ := rotatingSite(t, "wrong-key")
	var calls int32
	c := New(apiServer(t, "unobtainable", &calls), "")
	c.site = site

	_, err := c.Products(context.Background())
	if err == nil {
		t.Fatal("want an error when discovery keeps yielding a rejected key")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || !httpErr.Unauthorized() {
		t.Fatalf("want an unauthorized HTTPError, got %v", err)
	}
	if calls != 2 {
		t.Errorf("API called %d times, want exactly 2", calls)
	}
}

// TestPinnedKeyIsNeverReplaced: --auth-key is the escape hatch for when discovery itself
// has rotted, so it must not fall back to discovery and bury the real failure.
func TestPinnedKeyIsNeverReplaced(t *testing.T) {
	site, bundleFetches := rotatingSite(t, "discovered-key")
	var calls int32
	c := New(apiServer(t, "discovered-key", &calls), "operator-key")
	c.site = site

	if _, err := c.Products(context.Background()); err == nil {
		t.Fatal("want the pinned key's rejection reported, not papered over")
	}
	if c.AuthKey != "operator-key" {
		t.Errorf("AuthKey = %q, want the pinned operator-key untouched", c.AuthKey)
	}
	if *bundleFetches != 0 {
		t.Errorf("bundle fetched %d times, want 0 for a pinned key", *bundleFetches)
	}
	if calls != 1 {
		t.Errorf("API called %d times, want 1 with no retry", calls)
	}
}

// TestPinnedBaseSurvivesDiscovery: discovery supplies the key but must not move a base URL
// the operator chose with --api-base.
func TestPinnedBaseSurvivesDiscovery(t *testing.T) {
	site, _ := rotatingSite(t, "live-key")
	var calls int32
	base := apiServer(t, "live-key", &calls)
	c := New(base, "")
	c.site = site

	if _, err := c.Products(context.Background()); err != nil {
		t.Fatalf("Products: %v", err)
	}
	if c.Base != base {
		t.Errorf("Base = %q, want the pinned %q", c.Base, base)
	}
}
