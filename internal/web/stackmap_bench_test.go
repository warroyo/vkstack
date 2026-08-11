package web

// Benchmark for the pinned stackmap path, the most expensive answer the UI asks for and
// the one `vkstack static` pays 152 times over.
//
// It runs against a real cache rather than the fixtures the other tests use, because the
// costs that matter here only appear at production data volumes: this benchmark is what
// caught model.ByKey copying a Product per iteration inside the solver's inner loop,
// worth a 2.5x difference in the answer time. Skipped unless you point it at a cache.
//
//	VKSTACK_BENCH_CACHE=~/.cache/vkstack/vkstack.db go test ./internal/web/ \
//	  -run XXX -bench BenchmarkPinnedStackMap -benchtime 30x -cpuprofile /tmp/cpu.out
//	go tool pprof -top web.test /tmp/cpu.out

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/warroyo/vkstack/internal/graph"
	"github.com/warroyo/vkstack/internal/store"
)

func BenchmarkPinnedStackMap(b *testing.B) {
	path := os.Getenv("VKSTACK_BENCH_CACHE")
	if path == "" {
		b.Skip("set VKSTACK_BENCH_CACHE to a real cache database")
	}
	db, err := store.Open(path)
	if err != nil {
		b.Fatalf("opening cache: %v", err)
	}
	defer db.Close()

	var (
		mu     sync.Mutex
		graphs = map[int]*graph.Graph{}
	)
	load := func(gen int) (*graph.Graph, error) {
		mu.Lock()
		defer mu.Unlock()
		if g, ok := graphs[gen]; ok {
			return g, nil
		}
		snap, err := db.Load()
		if err != nil {
			return nil, err
		}
		g, err := graph.Load(snap, graph.Options{Generation: gen})
		if err != nil {
			return nil, err
		}
		graphs[gen] = g
		return g, nil
	}

	handler, err := NewServer(Config{Load: load, ReadOnly: true, Version: "bench"})
	if err != nil {
		b.Fatalf("building server: %v", err)
	}
	if _, err := load(0); err != nil {
		b.Fatalf("loading graph: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stackmap?product=vcenter&version=9.0.0.0", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}
