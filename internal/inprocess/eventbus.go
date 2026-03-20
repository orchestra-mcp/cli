package inprocess

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// subscription holds the state for a single event subscription.
type subscription struct {
	id       string
	topic    string
	ch       chan *pluginv1.EventDelivery
	wildcard bool
}

// EventBus is an in-memory pub/sub event dispatcher. Plugins publish events
// to named topics, and subscribers receive matching events via channels.
// Wildcard subscribers (created via SubscribeAll) receive events on all topics.
type EventBus struct {
	mu     sync.RWMutex
	subs   map[string]*subscription
	nextID atomic.Uint64
}

// NewEventBus creates a new EventBus ready for use.
func NewEventBus() *EventBus {
	return &EventBus{
		subs: make(map[string]*subscription),
	}
}

// Subscribe creates a subscription for the given topic. It returns a unique
// subscription ID and a read-only channel that receives matching events.
// The channel is buffered (capacity 64) and the caller must consume it to
// avoid dropped events.
func (eb *EventBus) Subscribe(topic string) (string, <-chan *pluginv1.EventDelivery) {
	id := fmt.Sprintf("sub-%d", eb.nextID.Add(1))
	ch := make(chan *pluginv1.EventDelivery, 64)

	sub := &subscription{
		id:       id,
		topic:    topic,
		ch:       ch,
		wildcard: false,
	}

	eb.mu.Lock()
	eb.subs[id] = sub
	eb.mu.Unlock()

	slog.Debug("event subscription created",
		"subscription_id", id,
		"topic", topic,
	)

	return id, ch
}

// SubscribeAll creates a wildcard subscription that receives events on all
// topics. It returns a unique subscription ID and a read-only channel.
func (eb *EventBus) SubscribeAll() (string, <-chan *pluginv1.EventDelivery) {
	id := fmt.Sprintf("sub-%d", eb.nextID.Add(1))
	ch := make(chan *pluginv1.EventDelivery, 64)

	sub := &subscription{
		id:       id,
		topic:    "*",
		ch:       ch,
		wildcard: true,
	}

	eb.mu.Lock()
	eb.subs[id] = sub
	eb.mu.Unlock()

	slog.Debug("wildcard event subscription created",
		"subscription_id", id,
	)

	return id, ch
}

// Unsubscribe removes a subscription by ID and closes its channel.
// It is safe to call with an ID that does not exist.
func (eb *EventBus) Unsubscribe(id string) {
	eb.mu.Lock()
	sub, ok := eb.subs[id]
	if ok {
		delete(eb.subs, id)
	}
	eb.mu.Unlock()

	if ok {
		close(sub.ch)
		slog.Debug("event subscription removed",
			"subscription_id", id,
			"topic", sub.topic,
		)
	}
}

// Publish sends an event to all subscriptions whose topic matches or that are
// wildcard subscribers. Delivery is non-blocking: if a subscriber's channel is
// full, the event is dropped and a warning is logged.
func (eb *EventBus) Publish(topic, eventType string, payload *structpb.Struct, sourcePlugin string) {
	now := timestamppb.New(time.Now())

	eb.mu.RLock()
	// Snapshot the matching subscriptions under the read lock.
	matching := make([]*subscription, 0, len(eb.subs))
	for _, sub := range eb.subs {
		if sub.wildcard || sub.topic == topic {
			matching = append(matching, sub)
		}
	}
	eb.mu.RUnlock()

	for _, sub := range matching {
		delivery := &pluginv1.EventDelivery{
			SubscriptionId: sub.id,
			Topic:          topic,
			EventType:      eventType,
			Payload:        payload,
			SourcePlugin:   sourcePlugin,
			Timestamp:      now,
		}

		select {
		case sub.ch <- delivery:
		default:
			slog.Warn("event delivery dropped: subscriber channel full",
				"subscription_id", sub.id,
				"topic", topic,
				"event_type", eventType,
				"source_plugin", sourcePlugin,
			)
		}
	}

	slog.Debug("event published",
		"topic", topic,
		"event_type", eventType,
		"source_plugin", sourcePlugin,
		"subscriber_count", len(matching),
	)
}

// Close removes all subscriptions and closes their channels. After Close is
// called, the EventBus should not be used.
func (eb *EventBus) Close() {
	eb.mu.Lock()
	subs := eb.subs
	eb.subs = make(map[string]*subscription)
	eb.mu.Unlock()

	for _, sub := range subs {
		close(sub.ch)
	}

	slog.Debug("event bus closed",
		"subscriptions_closed", len(subs),
	)
}
