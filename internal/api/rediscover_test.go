package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureSite serves the trimmed SPA shell and bundle from testdata, standing in for
// interopmatrix.broadcom.com.
func fixtureSite(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Interoperability", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/Interoperability.html")
	})
	mux.HandleFunc("/main.7d3f9a1c.js", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "testdata/main.7d3f9a1c.js")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRediscoverFromBundle is the guard against silent regex rot. Discovery is the only
// way this tool gets a key, so if the bundle's shape changes, this should fail in CI
// rather than in the field.
func TestRediscoverFromBundle(t *testing.T) {
	rd, err := rediscoverFrom(context.Background(), nil, fixtureSite(t))
	if err != nil {
		t.Fatalf("rediscoverFrom: %v", err)
	}
	if want := "FAKEKEY-not-the-real-one-0000000000000000000000000000000000000"; rd.AuthKey != want {
		t.Errorf("AuthKey = %q, want %q", rd.AuthKey, want)
	}
	// The bundle names a staging service before the production one; only production is
	// a valid target.
	if want := "https://interop.esp.spespg1.vmw.saas.broadcom.com/external"; rd.Base != want {
		t.Errorf("Base = %q, want %q", rd.Base, want)
	}
	if rd.Bundle != "main.7d3f9a1c.js" {
		t.Errorf("Bundle = %q, want main.7d3f9a1c.js", rd.Bundle)
	}
}

func TestRediscoverReportsMissingBundle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>no bundle here</body></html>")) //nolint:errcheck // test server
	}))
	defer srv.Close()

	_, err := rediscoverFrom(context.Background(), nil, srv.URL)
	if err == nil {
		t.Fatal("want an error when the page names no bundle")
	}
	if !strings.Contains(err.Error(), "main.<hash>.js") {
		t.Errorf("error should name what it looked for, got: %v", err)
	}
}
