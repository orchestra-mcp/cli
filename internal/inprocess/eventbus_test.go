package inprocess

import (
	"testing"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestEventBus_SubscribeAndPublish(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	id, ch := eb.Subscribe("features")
	if id == "" {
		t.Fatal("expected non-empty subscription ID")
	}

	payload, _ := structpb.NewStruct(map[string]any{"name": "test-feature"})
	eb.Publish("features", "create_feature", payload, "router")

	select {
	case ev := <-ch:
		if ev.Topic != "features" {
			t.Errorf("expected topic 'features', got %q", ev.Topic)
		}
		if ev.EventType != "create_feature" {
			t.Errorf("expected event_type 'create_feature', got %q", ev.EventType)
		}
		if ev.SourcePlugin != "router" {
			t.Errorf("expected source_plugin 'router', got %q", ev.SourcePlugin)
		}
		if ev.SubscriptionId != id {
			t.Errorf("expected subscription_id %q, got %q", id, ev.SubscriptionId)
		}
		if ev.Timestamp == nil {
			t.Error("expected non-nil timestamp")
		}
		if ev.Payload == nil {
			t.Error("expected non-nil payload")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_TopicFiltering(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	_, featuresCh := eb.Subscribe("features")
	_, notesCh := eb.Subscribe("notes")

	eb.Publish("features", "create_feature", nil, "router")

	select {
	case <-featuresCh:
		// expected
	case <-time.After(time.Second):
		t.Fatal("features subscriber should have received the event")
	}

	select {
	case <-notesCh:
		t.Fatal("notes subscriber should NOT have received a features event")
	case <-time.After(50 * time.Millisecond):
		// expected — no event for notes
	}
}

func TestEventBus_SubscribeAll(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	_, ch := eb.SubscribeAll()

	eb.Publish("features", "create_feature", nil, "router")
	eb.Publish("notes", "create_note", nil, "router")
	eb.Publish("projects", "create_project", nil, "router")

	for i := 0; i < 3; i++ {
		select {
		case <-ch:
			// expected
		case <-time.After(time.Second):
			t.Fatalf("wildcard subscriber should have received event %d", i+1)
		}
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	id, ch := eb.Subscribe("features")
	eb.Unsubscribe(id)

	eb.Publish("features", "create_feature", nil, "router")

	// Channel should be closed, so reading returns zero value immediately.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("expected closed channel to be readable immediately")
	}
}

func TestEventBus_UnsubscribeUnknownID(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	// Should not panic.
	eb.Unsubscribe("nonexistent-id")
}

func TestEventBus_MultipleSubscribersSameTopic(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	_, ch1 := eb.Subscribe("features")
	_, ch2 := eb.Subscribe("features")

	eb.Publish("features", "update_feature", nil, "router")

	for _, ch := range []<-chan *pluginv1.EventDelivery{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.EventType != "update_feature" {
				t.Errorf("expected 'update_feature', got %q", ev.EventType)
			}
		case <-time.After(time.Second):
			t.Fatal("both subscribers should receive the event")
		}
	}
}

func TestEventBus_DropsWhenChannelFull(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	_, ch := eb.Subscribe("features")

	// Fill the buffer (capacity 64).
	for i := 0; i < 64; i++ {
		eb.Publish("features", "fill", nil, "router")
	}

	// This publish should be dropped (channel full), not panic.
	eb.Publish("features", "dropped", nil, "router")

	// Drain and verify we got 64 events.
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != 64 {
		t.Errorf("expected 64 buffered events, got %d", count)
	}
}

func TestEventBus_Close(t *testing.T) {
	eb := NewEventBus()

	_, ch1 := eb.Subscribe("a")
	_, ch2 := eb.SubscribeAll()

	eb.Close()

	// Both channels should be closed.
	if _, ok := <-ch1; ok {
		t.Error("expected ch1 to be closed")
	}
	if _, ok := <-ch2; ok {
		t.Error("expected ch2 to be closed")
	}
}

func TestEventBus_UniqueSubscriptionIDs(t *testing.T) {
	eb := NewEventBus()
	defer eb.Close()

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, _ := eb.Subscribe("test")
		if ids[id] {
			t.Fatalf("duplicate subscription ID: %s", id)
		}
		ids[id] = true
	}
}
