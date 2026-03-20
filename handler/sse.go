package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// SSE writes Server-Sent Events to an HTTP response.
// Supports event types, auto-incrementing IDs, and reconnect detection.
//
//	func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
//	    sse := handler.NewSSE(w, r)
//	    sse.Send("progress", map[string]any{"step": 1})
//	    sse.Send("progress", map[string]any{"step": 2})
//	    sse.Send("done", map[string]any{"url": "..."})
//	}
type SSE struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	lastEventID int
	nextID      int
}

// NewSSE initializes an SSE stream. Sets headers and reads Last-Event-ID
// from a reconnecting client.
func NewSSE(w http.ResponseWriter, r *http.Request) *SSE {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	var lastID int
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		lastID, _ = strconv.Atoi(v)
	}

	flusher, _ := w.(http.Flusher)

	return &SSE{
		w:           w,
		flusher:     flusher,
		lastEventID: lastID,
		nextID:      lastID + 1,
	}
}

// IsReconnect returns true if the client is reconnecting after a disconnect.
// Check this to resume from where the client left off.
//
//	if sse.IsReconnect() {
//	    state := service.GetJobState(ctx, jobID)
//	    sse.Send("progress", state)
//	    return
//	}
func (s *SSE) IsReconnect() bool {
	return s.lastEventID > 0
}

// LastEventID returns the Last-Event-ID sent by a reconnecting client.
// Returns 0 on first connection.
func (s *SSE) LastEventID() int {
	return s.lastEventID
}

// Send writes an event to the stream.
// eventType is the SSE event name (client listens with addEventListener(eventType, ...)).
// data is JSON-serialized.
//
//	sse.Send("progress", map[string]any{"step": 1, "status": "analyzing"})
func (s *SSE) Send(eventType string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	id := s.nextID
	s.nextID++

	io.WriteString(s.w, "id: ")           //nolint:errcheck
	io.WriteString(s.w, strconv.Itoa(id)) //nolint:errcheck
	io.WriteString(s.w, "\nevent: ")      //nolint:errcheck
	io.WriteString(s.w, eventType)        //nolint:errcheck
	io.WriteString(s.w, "\ndata: ")       //nolint:errcheck
	s.w.Write(jsonData)                   //nolint:errcheck
	io.WriteString(s.w, "\n\n")           //nolint:errcheck

	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

// SendError writes an error event and is typically the last event before closing.
//
//	sse.SendError("transcode failed: unsupported format")
func (s *SSE) SendError(message string) error {
	return s.Send("error", map[string]string{"error": message})
}
