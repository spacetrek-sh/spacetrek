package httputil

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// PrepareSSE configures an http.ResponseWriter for Server-Sent Events.
// It sets the required headers and performs an initial flush.
// Callers should clear the write deadline with
// ResponseController.SetWriteDeadline(time.Time{}) after calling this.
func PrepareSSE(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).Flush()
}

// WriteSSEEvent writes a single SSE event to the response writer.
// Returns an error if JSON marshaling or writing fails, allowing callers
// to detect client disconnections.
func WriteSSEEvent(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sse payload: %w", err)
	}
	if _, err := w.Write([]byte("event: " + event + "\n")); err != nil {
		return fmt.Errorf("write sse event line: %w", err)
	}
	if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
		return fmt.Errorf("write sse data line: %w", err)
	}
	return nil
}

// WriteSSEHeartbeat writes an SSE comment line used as a keepalive.
// SSE spec says lines starting with ':' are comments and ignored by clients.
func WriteSSEHeartbeat(w http.ResponseWriter) error {
	_, err := w.Write([]byte(": heartbeat\n\n"))
	return err
}
