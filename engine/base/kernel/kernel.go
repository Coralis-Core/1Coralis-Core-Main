// Package kernel hosts cores: it starts them in order, gives each the shared
// Host (router, bus, log), checks bindings and contracts, and stops them in
// reverse order. It knows nothing about chats, commands or any customer-facing
// shape: whatever sits on top talks to the platform through capabilities
// (Router) or the wire contract. A "complement" is just a core that consumes
// other capabilities and provides new ones.
package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"example.com/project/engine/base/bus"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
)

// Host is what every core receives at Start.
type Host struct {
	Router *capability.Router
	Bus    *bus.Bus
	Log    *slog.Logger
}

// Core is anything that provides capabilities and/or reacts to events.
type Core interface {
	Name() string
	Start(ctx context.Context, h *Host) error
	Stop(ctx context.Context) error
}

type Platform struct {
	host      *Host
	cores     []Core
	started   []Core // in start order
	contracts *contract.Set
}

func New(cfg config.Config, log *slog.Logger) *Platform {
	if log == nil {
		log = slog.Default()
	}
	r := capability.NewRouter()
	r.Timeout = time.Duration(cfg.DefaultTimeoutMS) * time.Millisecond
	for _, b := range cfg.Bindings {
		n := capability.Name(b.Capability)
		r.Bind(n, cfg.Expand(b)...)
		if pol, ok := policyOf(b); ok {
			r.SetPolicy(n, pol)
		}
	}
	b := bus.New()
	b.OnPanic = func(topic string, rec any) { log.Error("bus subscriber panic", "topic", topic, "panic", rec) }
	return &Platform{host: &Host{Router: r, Bus: b, Log: log}}
}

func (p *Platform) Host() *Host { return p.host }

// RequireContracts makes the contract files the source of truth: at Start, any
// capability a core registers without a contract is a startup error.
func (p *Platform) RequireContracts(s *contract.Set) { p.contracts = s }

func (p *Platform) AddCore(c Core) error {
	if len(p.started) > 0 {
		return errors.New("kernel: add before Start")
	}
	for _, e := range p.cores {
		if e.Name() == c.Name() {
			return fmt.Errorf("kernel: core %q already added", c.Name())
		}
	}
	p.cores = append(p.cores, c)
	return nil
}

// Start starts every core in order, then validates bindings and contracts.
// Any failure stops the already started cores in reverse order.
func (p *Platform) Start(ctx context.Context) error {
	fail := func(err error) error {
		_ = p.Stop(ctx)
		return err
	}
	for _, c := range p.cores {
		if err := c.Start(ctx, p.host); err != nil {
			return fail(fmt.Errorf("core %q: start: %w", c.Name(), err))
		}
		p.started = append(p.started, c)
	}
	if err := p.host.Router.Validate(); err != nil {
		return fail(fmt.Errorf("config: %w", err))
	}
	if p.contracts != nil {
		var missing []string
		for _, n := range p.host.Router.Capabilities() {
			if c, ok := p.contracts.Get(string(n)); !ok || c.Kind != contract.KindRequest {
				missing = append(missing, string(n))
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fail(fmt.Errorf("capabilities without a request contract: %s", strings.Join(missing, ", ")))
		}
	}
	return nil
}

func (p *Platform) Stop(ctx context.Context) error {
	var errs []error
	for i := len(p.started) - 1; i >= 0; i-- {
		if err := p.started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%q: stop: %w", p.started[i].Name(), err))
		}
	}
	p.started = nil
	return errors.Join(errs...)
}

// policyOf maps a binding's mode, on_error and timeout_ms onto the router's
// per-capability policy. ok is false when the binding sets none of them, so a
// plain binding keeps the lenient default.
func policyOf(b config.Binding) (capability.Policy, bool) {
	if b.Mode == "" && b.OnError == "" && b.TimeoutMS == 0 && b.Retries == 0 {
		return capability.Policy{}, false
	}
	p := capability.Policy{Timeout: time.Duration(b.TimeoutMS) * time.Millisecond, Retries: b.Retries}
	switch b.Mode {
	case config.ModeSingle:
		p.Mode = capability.ModeSingle
	case config.ModeFallback:
		p.Mode = capability.ModeFallback
	case config.ModeFanout:
		p.Mode = capability.ModeFanout
	}
	if b.OnError == config.OnErrorFail {
		p.OnError = capability.OnErrorFail
	}
	return p, true
}
