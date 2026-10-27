// Package bus is a tiny in-process publish/subscribe bus (sync, ordered,
// panic-isolated). For multi-process setups, swap it behind the same shape.
package bus

import (
	"context"
	"encoding/json"
	"sync"
)

type Handler func(ctx context.Context, payload any)

type sub struct {
	id uint64
	h  Handler
}

type Bus struct {
	mu      sync.RWMutex
	next    uint64
	subs    map[string][]sub
	OnPanic func(topic string, recovered any)
}

func New() *Bus { return &Bus{subs: map[string][]sub{}} }

func (b *Bus) Subscribe(topic string, h Handler) (cancel func()) {
	b.mu.Lock()
	b.next++
	id := b.next
	b.subs[topic] = append(b.subs[topic], sub{id: id, h: h})
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		list := b.subs[topic]
		for i, s := range list {
			if s.id == id {
				b.subs[topic] = append(list[:i:i], list[i+1:]...)
				return
			}
		}
	}
}

func (b *Bus) Publish(ctx context.Context, topic string, payload any) {
	b.mu.RLock()
	list := make([]sub, len(b.subs[topic]))
	copy(list, b.subs[topic])
	b.mu.RUnlock()
	for _, s := range list {
		b.safeCall(topic, s.h, ctx, payload)
	}
}

func (b *Bus) safeCall(topic string, h Handler, ctx context.Context, payload any) {
	defer func() {
		if r := recover(); r != nil && b.OnPanic != nil {
			b.OnPanic(topic, r)
		}
	}()
	h(ctx, payload)
}

// On is a typed Subscribe: payloads of other types are ignored.
func On[T any](b *Bus, topic string, fn func(ctx context.Context, ev T)) (cancel func()) {
	return b.Subscribe(topic, func(ctx context.Context, payload any) {
		if ev, ok := payload.(T); ok {
			fn(ctx, ev)
		}
	})
}

// Tap subscribes with a JSON view of whatever is published on topic. It is how
// language-neutral tooling (conformance checks, bridges) observes events.
func (b *Bus) Tap(topic string, fn func(ctx context.Context, payload []byte)) (cancel func()) {
	return b.Subscribe(topic, func(ctx context.Context, payload any) {
		if raw, err := json.Marshal(payload); err == nil {
			fn(ctx, raw)
		}
	})
}
