package thumbnail

import (
	"testing"
	"time"
)

func TestHubReplayAndLivePublish(t *testing.T) {
	hub := NewHub()
	hub.Publish("image", "a.jpg", "ready")

	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	first := recvEvent(t, events)
	if first.Kind != "image" || first.Path != "a.jpg" || first.Status != "ready" {
		t.Fatalf("replay got %+v", first)
	}

	hub.Publish("cover", "album", "error")
	second := recvEvent(t, events)
	if second.Kind != "cover" || second.Path != "album" || second.Status != "error" {
		t.Fatalf("live got %+v", second)
	}
	if second.ID <= first.ID {
		t.Fatalf("expected increasing ids, got %d then %d", first.ID, second.ID)
	}
}

func recvEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hub event")
	}
	return Event{}
}
