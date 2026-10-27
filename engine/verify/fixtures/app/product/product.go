// Package product (TEST FIXTURE) is a "complement": nothing special, just a
// core that CONSUMES other capabilities (identity, stats) and PROVIDES a new
// one (profile.v1.get). It degrades gracefully when a source is missing.
package product

import (
	"context"
	"errors"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/verify/fixtures/contracts/identity"
	"example.com/project/engine/verify/fixtures/contracts/profile"
	"example.com/project/engine/verify/fixtures/contracts/stats"
)

type Core struct {
	id   string
	host *kernel.Host
}

func New(id string) *Core { return &Core{id: id} }

func (c *Core) Name() string               { return c.id }
func (c *Core) Stop(context.Context) error { return nil }

func (c *Core) Start(_ context.Context, h *kernel.Host) error {
	c.host = h
	return profile.Get.Provide(h.Router, c.id, c.get)
}

func tolerated(err error) bool {
	return errors.Is(err, identity.ErrNotFound) || errors.Is(err, capability.ErrNoProvider) || errors.Is(err, capability.ErrUnavailable)
}

func (c *Core) get(ctx context.Context, q profile.Query) (profile.Profile, error) {
	out := profile.Profile{UserID: q.UserID}
	if u, err := identity.GetUser.Call(ctx, c.host.Router, identity.UserQuery{ID: q.UserID}); err == nil {
		out.Name = u.Name
	} else if !tolerated(err) {
		return out, err
	}
	if n, err := stats.MessageCount.Call(ctx, c.host.Router, stats.CountQuery{ChatID: q.ChatID}); err == nil {
		m := n.Messages
		out.Messages = &m
	}
	return out, nil
}
