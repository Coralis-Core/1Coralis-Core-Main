package capability

import (
	"context"
	"encoding/json"
	"fmt"
)

// Next continues a call to whatever lies below a wrapper.
type Next func(ctx context.Context, in []byte) ([]byte, error)

// Wrapper is an "around" decorator with full control: it may change the input,
// skip Next, retry it, change the output or the error. Before and after are
// just code before and after the Next call. A wrapper never edits the core it
// wraps. Errors it returns propagate to the caller (never swallowed).
type Wrapper func(ctx context.Context, in []byte, next Next) ([]byte, error)

// Placement says where a wrapper sits.
type Placement int

const (
	// Outer wraps the whole call. For a fan-out it wraps the entire fan-out, but
	// it can only gate it (inspect/change the input, refuse with an error): the
	// per-source results stay untouched because they are a map, not one document.
	Outer Placement = iota
	// Inner wraps each single source (each provider call, also inside a fan-out).
	Inner
)

type wrapSet struct{ outer, inner []Wrapper }

// AddWrapper adds a wrapper to n. Order of calls = order of the config list:
// the first one added is the outermost of its placement.
func (r *Router) AddWrapper(n Name, p Placement, w Wrapper) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wraps == nil {
		r.wraps = map[Name]*wrapSet{}
	}
	ws := r.wraps[n]
	if ws == nil {
		ws = &wrapSet{}
		r.wraps[n] = ws
	}
	if p == Inner {
		ws.inner = append(ws.inner, w)
	} else {
		ws.outer = append(ws.outer, w)
	}
}

func (r *Router) wrapsOf(n Name) (outer, inner []Wrapper) {
	if ws := r.wraps[n]; ws != nil {
		return ws.outer, ws.inner
	}
	return nil, nil
}

func (r *Router) hasOuter(n Name) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, _ := r.wrapsOf(n)
	return len(o) > 0
}

func chain(ws []Wrapper, final Next) Next {
	next := final
	for i := len(ws) - 1; i >= 0; i-- {
		w, nx := ws[i], next
		next = func(ctx context.Context, in []byte) ([]byte, error) { return w(ctx, in, nx) }
	}
	return next
}

// runOuter runs the outer wrappers of n around final.
func (r *Router) runOuter(ctx context.Context, n Name, in []byte, final Next) ([]byte, error) {
	r.mu.RLock()
	o, _ := r.wrapsOf(n)
	r.mu.RUnlock()
	return chain(o, final)(ctx, in)
}

// withInner applies n's inner wrappers to a provider implementation. A wrapped
// provider is always called through JSON (a wrapper works on documents), so the
// typed shortcut is dropped.
func withInner(inner []Wrapper, im impl) impl {
	if len(inner) == 0 {
		return im
	}
	base := im.json
	return impl{json: func(ctx context.Context, in []byte) ([]byte, error) {
		return chain(inner, Next(base))(ctx, in)
	}}
}

// callViaJSON is the typed call path used when outer wrappers exist.
func callViaJSON[In, Out any](ctx context.Context, r *Router, n Name, in In) (Out, error) {
	var zero Out
	raw, err := json.Marshal(in)
	if err != nil {
		return zero, err
	}
	b, err := r.CallJSON(ctx, n, raw)
	if err != nil {
		return zero, err
	}
	var o Out
	if err := json.Unmarshal(b, &o); err != nil {
		return zero, fmt.Errorf("%w: output for %s: %v", ErrInternal, n, err)
	}
	return o, nil
}
