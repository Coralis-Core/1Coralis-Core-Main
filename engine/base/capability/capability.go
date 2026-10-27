// Package capability is the heart of the decoupling. A capability is a named,
// versioned operation ("identity.v1.user.get"). Cores PROVIDE capabilities;
// complements CALL them. Neither knows the other exists. Which core serves a
// capability is configuration (Bind), not code.
//
// A provider can be a Go function (typed, in-process, no serialization) or a
// JSON handler (remote core behind any transport). Callers cannot tell.
package capability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Name string

// CodedError is an error with a stable, language-neutral code. It is what
// crosses process boundaries; errors.Is compares by code.
type CodedError struct {
	Code    string
	Message string
}

func NewError(code, message string) *CodedError { return &CodedError{Code: code, Message: message} }

func (e *CodedError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *CodedError) Is(target error) bool {
	t, ok := target.(*CodedError)
	return ok && t.Code == e.Code
}

// CodeOf returns the code of the first CodedError in err's chain ("" if none).
func CodeOf(err error) string {
	var ce *CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// Generic codes every provider/transport may use (contracts declare only
// their domain codes).
var (
	ErrNoProvider   = NewError("no_provider", "capability: no provider")
	ErrUnavailable  = NewError("unavailable", "capability: provider unavailable") // triggers fallback to the next bound provider
	ErrInvalidInput = NewError("invalid_input", "capability: invalid input")
	ErrInternal     = NewError("internal", "capability: internal error")
	ErrBadType      = errors.New("capability: wrong input/output type")
)

// Handler is the in-process, untyped form stored in the router.
type Handler func(ctx context.Context, in any) (any, error)

// JSONHandler is the language-neutral form: JSON in, JSON out.
type JSONHandler func(ctx context.Context, in []byte) ([]byte, error)

type impl struct {
	typed Handler     // nil for remote providers
	json  JSONHandler // always set
}

type Router struct {
	// Timeout, if > 0, bounds every call.
	Timeout time.Duration

	mu    sync.RWMutex
	impl  map[Name]map[string]impl // capability -> provider -> implementation
	order map[Name][]string        // registration order
	bind  map[Name][]string        // explicit preference (restricts the chain)
	pol   map[Name]Policy          // per-capability behaviour from configuration
	wraps map[Name]*wrapSet        // wrappers per capability (see wrap.go)
	res   resilience               // retries, circuit breaker, idempotency (see resilience.go)

	onFailure func(FailureEvent)
}

func NewRouter() *Router {
	return &Router{impl: map[Name]map[string]impl{}, order: map[Name][]string{}, bind: map[Name][]string{}, pol: map[Name]Policy{}, res: newResilience()}
}

func (r *Router) register(provider string, n Name, im impl) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.impl[n] == nil {
		r.impl[n] = map[string]impl{}
	}
	if _, dup := r.impl[n][provider]; dup {
		return fmt.Errorf("capability: %q already provided by %q", n, provider)
	}
	r.impl[n][provider] = im
	r.order[n] = append(r.order[n], provider)
	return nil
}

// ProvideJSON registers a JSON-level implementation (typically a remote core
// reached through a transport adapter).
func (r *Router) ProvideJSON(provider string, n Name, h JSONHandler) error {
	return r.register(provider, n, impl{json: h})
}

// Bind restricts and orders the providers used for a capability. First wins;
// later ones are used only when the earlier return ErrUnavailable.
func (r *Router) Bind(n Name, providers ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bind[n] = append([]string(nil), providers...)
}

// Capabilities lists every capability name that at least one provider serves.
func (r *Router) Capabilities() []Name {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Name, 0, len(r.impl))
	for n := range r.impl {
		out = append(out, n)
	}
	return out
}

// Chain returns the effective provider order for n.
func (r *Router) Chain(n Name) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.chainLocked(n)
}

func (r *Router) chainLocked(n Name) []string {
	var out []string
	if b, ok := r.bind[n]; ok {
		for _, p := range b {
			if _, has := r.impl[n][p]; has {
				out = append(out, p)
			}
		}
		return out
	}
	return append(out, r.order[n]...)
}

// Validate reports bindings that point at providers which do not implement
// the capability (a config typo must fail at startup, not at 3am).
func (r *Router) Validate() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var errs []error
	for n, ps := range r.bind {
		for _, p := range ps {
			if _, ok := r.impl[n][p]; !ok {
				errs = append(errs, fmt.Errorf("binding %q -> %q: provider does not implement it", n, p))
			}
		}
	}
	return errors.Join(errs...)
}

// walk runs try over the provider chain with timeout, panic safety, circuit
// breaker, retries and fallback. Fallback to the next provider happens only
// when repeating is safe: the failure happened before delivery (unavailable),
// or the capability is declared idempotent.
func (r *Router) walk(ctx context.Context, n Name, try func(context.Context, impl) (any, error)) (any, error) {
	r.mu.RLock()
	chain := r.chainLocked(n)
	pol := r.pol[n]
	if pol.Mode == ModeSingle && len(chain) > 1 {
		chain = chain[:1]
	}
	_, inner := r.wrapsOf(n)
	ims := make([]impl, len(chain))
	for i, p := range chain {
		ims[i] = withInner(inner, r.impl[n][p])
	}
	timeout := r.Timeout
	if pol.Timeout > 0 {
		timeout = pol.Timeout
	}
	r.mu.RUnlock()

	if pol.Mode == ModeFanout {
		return nil, fmt.Errorf("%w: %s", ErrFanoutOnly, n)
	}
	if len(ims) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoProvider, n)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var last error
	for i, im := range ims {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		im := im
		out, err := r.attempt(ctx, n, chain[i], pol.Retries, func() (any, error) { return try(ctx, im) })
		if err == nil {
			return out, nil
		}
		if r.retriable(n, err) {
			last = fmt.Errorf("%s: %w", chain[i], err)
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("%s: all providers failed: %w", n, last)
}

func safe(fn func() (any, error)) (out any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%w: panic: %v", ErrAmbiguous, rec)
		}
	}()
	return fn()
}

// CallJSON invokes a capability with a JSON payload (used by transport
// servers). Contract validation is the caller's job (see contract.Guard).
func (r *Router) CallJSON(ctx context.Context, n Name, in []byte) ([]byte, error) {
	return r.runOuter(ctx, n, in, func(ctx context.Context, in []byte) ([]byte, error) {
		out, err := r.walk(ctx, n, func(ctx context.Context, im impl) (any, error) { return im.json(ctx, in) })
		if err != nil {
			return nil, err
		}
		return out.([]byte), nil
	})
}

// slot is one provider's outcome in a fan-out call.
type slot struct {
	provider string
	out      any
	err      error
}

// fanout runs try against EVERY provider in the effective chain at the same
// time (panic-safe, bounded by Router.Timeout) and returns the outcomes in
// chain order. It never short-circuits: one slow or dead source does not hide
// the others.
func (r *Router) fanout(ctx context.Context, n Name, try func(context.Context, impl) (any, error)) ([]slot, error) {
	r.mu.RLock()
	chain := r.chainLocked(n)
	pol := r.pol[n]
	_, inner := r.wrapsOf(n)
	ims := make([]impl, len(chain))
	for i, p := range chain {
		ims[i] = withInner(inner, r.impl[n][p])
	}
	timeout := r.Timeout
	if pol.Timeout > 0 {
		timeout = pol.Timeout
	}
	r.mu.RUnlock()

	if pol.Mode == ModeSingle || pol.Mode == ModeFallback {
		return nil, fmt.Errorf("%w: %s", ErrNotFanout, n)
	}
	if len(ims) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoProvider, n)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	slots := make([]slot, len(ims))
	var wg sync.WaitGroup
	for i := range ims {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := r.attempt(ctx, n, chain[i], pol.Retries, func() (any, error) { return try(ctx, ims[i]) })
			slots[i] = slot{provider: chain[i], out: out, err: err}
		}(i)
	}
	wg.Wait()
	if pol.OnError == OnErrorFail {
		for _, sl := range slots {
			if sl.err != nil {
				return nil, fmt.Errorf("%s: source %q failed: %w", n, sl.provider, sl.err)
			}
		}
	}
	return slots, nil
}

// Result is one source's answer to a fan-out call at the JSON level.
type Result struct {
	Provider string
	Output   []byte // JSON; nil when Err != nil
	Err      error
}

// CallAllJSON asks every provider bound to n at the same time and returns each
// answer with its provenance, in chain order. Partial results are normal: a
// failed source carries its own Err and the others still answer. Merging is
// the caller's job (the platform has no domain logic). The error is non-nil
// only when no provider serves n at all.
func (r *Router) CallAllJSON(ctx context.Context, n Name, in []byte) ([]Result, error) {
	var slots []slot
	_, err := r.runOuter(ctx, n, in, func(ctx context.Context, in []byte) ([]byte, error) {
		var ferr error
		slots, ferr = r.fanout(ctx, n, func(ctx context.Context, im impl) (any, error) {
			b, err := im.json(ctx, in)
			return b, err
		})
		return nil, ferr
	})
	if err != nil {
		return nil, err
	}
	res := make([]Result, len(slots))
	for i, sl := range slots {
		res[i] = Result{Provider: sl.provider, Err: sl.err}
		if sl.err == nil {
			res[i].Output, _ = sl.out.([]byte)
		}
	}
	return res, nil
}

// Cap is a typed handle to a capability. Contracts export these as variables.
type Cap[In, Out any] struct{ Name Name }

func Define[In, Out any](n Name) Cap[In, Out] { return Cap[In, Out]{Name: n} }

// Provide registers fn as the implementation of c on behalf of provider.
func (c Cap[In, Out]) Provide(r *Router, provider string, fn func(context.Context, In) (Out, error)) error {
	typed := func(ctx context.Context, in any) (any, error) {
		v, ok := in.(In)
		if !ok {
			var want In
			return nil, fmt.Errorf("%w: %s wants %T, got %T", ErrBadType, c.Name, want, in)
		}
		return fn(ctx, v)
	}
	asJSON := func(ctx context.Context, raw []byte) ([]byte, error) {
		var v In
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		out, err := fn(ctx, v)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return r.register(provider, c.Name, impl{typed: typed, json: asJSON})
}

// Call invokes the capability through the router. Local typed providers are
// called directly (no serialization); remote ones go through JSON.
func (c Cap[In, Out]) Call(ctx context.Context, r *Router, in In) (Out, error) {
	var zero Out
	if r.hasOuter(c.Name) {
		return callViaJSON[In, Out](ctx, r, c.Name, in)
	}
	out, err := r.walk(ctx, c.Name, func(ctx context.Context, im impl) (any, error) {
		if im.typed != nil {
			return im.typed(ctx, in)
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		b, err := im.json(ctx, raw)
		if err != nil {
			return nil, err
		}
		var o Out
		if err := json.Unmarshal(b, &o); err != nil {
			return nil, fmt.Errorf("%w: remote output for %s: %v", ErrInternal, c.Name, err)
		}
		return o, nil
	})
	if err != nil {
		return zero, err
	}
	v, ok := out.(Out)
	if !ok {
		return zero, fmt.Errorf("%w: %s returned %T", ErrBadType, c.Name, out)
	}
	return v, nil
}

// Answer is one source's typed answer to a fan-out call.
type Answer[Out any] struct {
	Provider string
	Value    Out
	Err      error
}

// CallAll is Call for "everyone who can answer, all at once": local typed
// providers are called directly, remote ones through JSON. Results keep their
// provenance and arrive in chain order; failures are per-source.
func (c Cap[In, Out]) CallAll(ctx context.Context, r *Router, in In) ([]Answer[Out], error) {
	if r.hasOuter(c.Name) {
		raw, merr := json.Marshal(in)
		if merr != nil {
			return nil, merr
		}
		if _, gerr := r.runOuter(ctx, c.Name, raw, func(context.Context, []byte) ([]byte, error) { return nil, nil }); gerr != nil {
			return nil, gerr
		}
	}
	slots, err := r.fanout(ctx, c.Name, func(ctx context.Context, im impl) (any, error) {
		if im.typed != nil {
			return im.typed(ctx, in)
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		b, err := im.json(ctx, raw)
		if err != nil {
			return nil, err
		}
		var o Out
		if err := json.Unmarshal(b, &o); err != nil {
			return nil, fmt.Errorf("%w: remote output for %s: %v", ErrInternal, c.Name, err)
		}
		return o, nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Answer[Out], len(slots))
	for i, sl := range slots {
		out[i] = Answer[Out]{Provider: sl.provider, Err: sl.err}
		if sl.err != nil {
			continue
		}
		v, ok := sl.out.(Out)
		if !ok {
			out[i].Err = fmt.Errorf("%w: %s returned %T", ErrBadType, c.Name, sl.out)
			continue
		}
		out[i].Value = v
	}
	return out, nil
}
