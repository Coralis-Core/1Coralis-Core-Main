package boot

import (
	"context"
	"fmt"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
)

// mountWrappers mounts wrappers[] in list order (first = outermost within its
// placement). Each wrapper's output is checked against the capability's
// contract unless check_contract is false.
func (p *Platform) mountWrappers(cfg config.Config, o Options) error {
	fanout := map[string]bool{}
	for _, b := range cfg.Bindings {
		fanout[b.Capability] = b.Mode == config.ModeFanout
	}
	for i, w := range cfg.Wrappers {
		f, ok := o.Wrappers[w.Kind]
		if !ok {
			return fmt.Errorf("wrappers[%d]: unknown kind %q (register a WrapperFactory for it)", i, w.Kind)
		}
		c, ok := o.Contracts.Get(w.Capability)
		if !ok || c.Kind != contract.KindRequest {
			return fmt.Errorf("wrappers[%d]: %q has no request contract", i, w.Capability)
		}
		wr, err := f(w.Options)
		if err != nil {
			return fmt.Errorf("wrappers[%d] (%s): %w", i, w.Kind, err)
		}
		place := capability.Outer
		if w.Placement == config.PlaceInner {
			place = capability.Inner
		}
		// an outer wrapper around a fan-out only gates it: there is no single
		// output document to check
		gate := place == capability.Outer && fanout[w.Capability]
		if (w.CheckContract == nil || *w.CheckContract) && !gate {
			wr = checked(c, wr)
		}
		p.Host().Router.AddWrapper(capability.Name(w.Capability), place, wr)
	}
	return nil
}

func checked(c *contract.Contract, w capability.Wrapper) capability.Wrapper {
	return func(ctx context.Context, in []byte, next capability.Next) ([]byte, error) {
		out, err := w(ctx, in, next)
		if err != nil {
			return nil, err
		}
		if verr := c.ValidateOutput(out); verr != nil {
			return nil, fmt.Errorf("%w: wrapper output violates the contract of %s: %v", capability.ErrInternal, c.Capability, verr)
		}
		return out, nil
	}
}

// mountSubscriptions resolves subscriptions[] to the cores that receive them.
func (p *Platform) mountSubscriptions(cfg config.Config) error {
	byID := map[string]kernel.Core{}
	for _, c := range p.cores {
		byID[c.Name()] = c
	}
	for i, s := range cfg.Subscriptions {
		c, ok := p.set.Get(s.Event)
		if !ok || c.Kind != contract.KindEvent {
			return fmt.Errorf("subscriptions[%d]: %q is not an event contract", i, s.Event)
		}
		timeout := DefaultSubscriberTimeout
		if s.TimeoutMS > 0 {
			timeout = time.Duration(s.TimeoutMS) * time.Millisecond
		}
		for _, id := range cfg.Expand(config.Binding{Cores: []string{s.Core}}) {
			rx, ok := byID[id].(EventReceiver)
			if !ok {
				return fmt.Errorf("subscriptions[%d]: core %q cannot receive events (kind does not support delivery)", i, id)
			}
			p.subs[s.Event] = append(p.subs[s.Event], subTarget{id: id, rx: rx, timeout: timeout})
		}
	}
	return nil
}
