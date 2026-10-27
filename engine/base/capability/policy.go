package capability

import "time"

// Mode says how one capability may be consumed. It comes from configuration
// (a binding's "mode"), never from the code of a core.
type Mode int

const (
	// ModeAny keeps the lenient default: both the single-result calls and the
	// fan-out calls are allowed.
	ModeAny Mode = iota
	// ModeSingle uses exactly the first bound provider and never falls through.
	ModeSingle
	// ModeFallback uses the first healthy provider in order.
	ModeFallback
	// ModeFanout asks every bound provider at once; only the fan-out calls
	// (CallAll / CallAllJSON) are allowed.
	ModeFanout
)

// OnError is what a fan-out does when some source fails.
type OnError int

const (
	// OnErrorSkip sets failed sources aside: each failed source carries its own
	// error and the others still answer.
	OnErrorSkip OnError = iota
	// OnErrorFail fails the whole call if any source failed.
	OnErrorFail
)

// Policy is the per-capability behaviour chosen in configuration.
type Policy struct {
	Mode    Mode
	OnError OnError
	// Timeout, if > 0, replaces Router.Timeout for this capability. In a
	// fan-out every source runs at the same time, so it is each source's budget.
	Timeout time.Duration
	// Retries overrides the platform default of extra attempts (0 = default).
	Retries int
}

// Coded errors for a call that does not match the configured mode.
var (
	ErrFanoutOnly = NewError("fanout_only", "capability: bound as fan-out; use the fan-out call")
	ErrNotFanout  = NewError("not_fanout", "capability: not bound as fan-out; use the single-result call")
)

// SetPolicy sets how n may be consumed. Without a policy a capability behaves
// as ModeAny.
func (r *Router) SetPolicy(n Name, p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pol[n] = p
}

// PolicyOf returns the policy of n (the zero Policy if none was set).
func (r *Router) PolicyOf(n Name) Policy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pol[n]
}
