// Package memory has two tiny in-memory cores. They prove
// that any source of data/logic can sit behind the same contracts, and they
// are handy for tests and demos.
package memory

import (
	"context"
	"sync"

	"example.com/project/engine/base/bus"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/verify/fixtures/contracts/events"
	"example.com/project/engine/verify/fixtures/contracts/identity"
	"example.com/project/engine/verify/fixtures/contracts/stats"
)

// Directory is a core that implements identity.v1.
type Directory struct {
	name  string
	Users map[int64]identity.User
	Chats map[int64]identity.Chat
}

func NewDirectory(name string) *Directory {
	return &Directory{name: name, Users: map[int64]identity.User{}, Chats: map[int64]identity.Chat{}}
}

func (d *Directory) Name() string { return d.name }

func (d *Directory) Start(_ context.Context, h *kernel.Host) error {
	if err := identity.GetUser.Provide(h.Router, d.name, func(_ context.Context, q identity.UserQuery) (identity.User, error) {
		if u, ok := d.Users[q.ID]; ok {
			return u, nil
		}
		return identity.User{}, identity.ErrNotFound
	}); err != nil {
		return err
	}
	return identity.GetChat.Provide(h.Router, d.name, func(_ context.Context, q identity.ChatQuery) (identity.Chat, error) {
		if c, ok := d.Chats[q.ID]; ok {
			return c, nil
		}
		return identity.Chat{}, identity.ErrNotFound
	})
}

func (d *Directory) Stop(context.Context) error { return nil }

// Counter is a core that implements stats.v1 by listening to message events.
type Counter struct {
	name   string
	cancel func()
	mu     sync.Mutex
	counts map[int64]int
}

func NewCounter(name string) *Counter { return &Counter{name: name, counts: map[int64]int{}} }

func (c *Counter) Name() string { return c.name }

func (c *Counter) Start(_ context.Context, h *kernel.Host) error {
	c.cancel = bus.On(h.Bus, events.TopicMessageSeen, func(_ context.Context, ev events.MessageSeen) {
		c.mu.Lock()
		c.counts[ev.ChatID]++
		c.mu.Unlock()
	})
	return stats.MessageCount.Provide(h.Router, c.name, func(_ context.Context, q stats.CountQuery) (stats.Count, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return stats.Count{Messages: c.counts[q.ChatID]}, nil
	})
}

func (c *Counter) Stop(context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}
