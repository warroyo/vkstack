package web

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// handleRefresh streams refresh progress as server-sent events, so the UI can show what
// is happening during the minute or so a full pull takes.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
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

	err := s.refresh(func(step, total int, message string) {
		send("progress", map[string]any{"step": step, "total": total, "message": message})
	})
	if err != nil {
		send("error", map[string]string{"error": err.Error()})
		return
	}
	send("done", map[string]bool{"ok": true})
}
