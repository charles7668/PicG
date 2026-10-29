package thumbnail

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	hubRecentLimit  = 64
	hubClientBuffer = 64
	hubHeartbeat    = 15 * time.Second
)

// Event tells browsers a thumbnail finished generating (or failed).
type Event struct {
	ID     uint64 `json:"-"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// Hub broadcasts thumbnail events to one SSE stream per browser tab.
type Hub struct {
	mu      sync.Mutex
	seq     uint64
	clients map[chan Event]struct{}
	recent  []Event
}

func NewHub() *Hub {
	return &Hub{clients: make(map[chan Event]struct{})}
}

func (h *Hub) Publish(kind, path, status string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.seq++
	event := Event{ID: h.seq, Kind: kind, Path: path, Status: status}
	h.recent = append(h.recent, event)
	if len(h.recent) > hubRecentLimit {
		h.recent = append([]Event(nil), h.recent[len(h.recent)-hubRecentLimit:]...)
	}
	clients := make([]chan Event, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		select {
		case client <- event:
		default:
		}
	}
}

func (h *Hub) Subscribe() (<-chan Event, func()) {
	client := make(chan Event, hubClientBuffer)
	h.mu.Lock()
	h.clients[client] = struct{}{}
	replay := append([]Event(nil), h.recent...)
	h.mu.Unlock()
	for _, event := range replay {
		select {
		case client <- event:
		default:
		}
	}
	return client, func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
	}
}

// Write holds one text/event-stream connection until the client disconnects.
func (h *Hub) Write(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
		return
	}
	flusher.Flush()

	events, unsubscribe := h.Subscribe()
	defer unsubscribe()
	heartbeat := time.NewTicker(hubHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: thumbnail\ndata: %s\n\n", event.ID, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
