package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Ledger records what happened the last few times the server tried to refresh.
//
// "6h old" cannot tell one missed tick from two days of failures, and a hosted instance
// deliberately keeps serving stale data when upstream is unreachable — stale beats
// nothing. That is the right behaviour and an invisible one: until now the only person
// who could see refreshes were failing was whoever could read the logs on the box.
type Ledger struct {
	mu       sync.Mutex
	attempts []Attempt
	// failures counts consecutive failures, reset by any success. This is the number
	// that separates "one blip" from "the data stopped moving".
	failures int
}

// Attempt is one refresh, successful or not.
type Attempt struct {
	At    time.Time `json:"-"`
	AtStr string    `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	Took  string    `json:"took,omitempty"`
}

// ledgerDepth is how much history is kept. Enough to show a pattern, small enough that
// the meta response stays a glance rather than a log file.
const ledgerDepth = 8

// Record adds an attempt, newest first.
func (l *Ledger) Record(at time.Time, took time.Duration, err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	a := Attempt{At: at, AtStr: at.Format(time.RFC3339), OK: err == nil}
	if err != nil {
		a.Error = err.Error()
		l.failures++
	} else {
		a.Took = took.Round(time.Second).String()
		l.failures = 0
	}
	l.attempts = append([]Attempt{a}, l.attempts...)
	if len(l.attempts) > ledgerDepth {
		l.attempts = l.attempts[:ledgerDepth]
	}
}

// Snapshot returns the ledger for the API, or nil when nothing has been attempted.
func (l *Ledger) Snapshot() map[string]any {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.attempts) == 0 {
		return nil
	}
	out := map[string]any{
		"attempts":            append([]Attempt(nil), l.attempts...),
		"consecutiveFailures": l.failures,
	}
	if l.failures > 0 {
		// The age of the data is the age of the last *successful* pull, which is not
		// the same as the time of the last attempt once attempts start failing.
		for _, a := range l.attempts {
			if a.OK {
				out["lastSuccess"] = a.AtStr
				break
			}
		}
	}
	return out
}

// handleHealth is the probe for a hosted instance. It reports 200 only when the cache
// actually has data, so a rollout does not go green on an instance that has nothing to
// answer with yet.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// The probe asks whether there is data at all, which is a question about the cache
	// rather than about any one generation.
	g, err := s.cfg.Load(0)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": err.Error(),
		})
		return
	}
	if g.FetchedAt == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "cache is empty",
		})
		return
	}
	fetched := time.UnixMilli(g.FetchedAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"fetchedAt": fetched.Format(time.RFC3339),
		"ageHours":  int(time.Since(fetched).Hours()),
	})
}

// handleRefresh streams refresh progress as server-sent events, so the UI can show what
// is happening during the minute or so a full pull takes.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	// A read-only instance must reject this before doing any work, so a visitor cannot
	// make the server call upstream.
	if s.cfg.ReadOnly || s.cfg.Refresh == nil {
		writeErr(w, http.StatusForbidden,
			fmt.Errorf("this instance is read-only; data refreshes on a schedule"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	send := func(event string, payload any) {
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		flusher.Flush()
	}

	err := s.cfg.Refresh(func(step, total int, message string) {
		send("progress", map[string]any{"step": step, "total": total, "message": message})
	})
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	send("done", map[string]bool{"ok": true})
}
