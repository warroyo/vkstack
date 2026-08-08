package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleHealth is the probe for a hosted instance. It reports 200 only when the cache
// actually has data, so a rollout does not go green on an instance that can serve
// nothing but the model view.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	g, err := s.cfg.Load()
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
