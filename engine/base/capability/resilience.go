package capability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrAmbiguous means the request may already have reached the provider (a
// timeout or a broken connection after sending). Repeating it is only safe for
// a capability whose contract declares it idempotent; otherwise the error goes
// to the caller. ErrUnavailable, by contrast, means "nothing was processed"
// (provider down, circuit open) and is always safe to retry or fall back from.
var ErrAmbiguous = NewError("maybe_delivered", "capability: request may have reached the provider")

// Resilience is the platform-wide default for retries and the circuit breaker.
// Every field has a default, so the zero value works.
type Resilience struct {
	// Retries is how many EXTRA attempts a retriable failure gets (default 0).
	Retries int
	// RetryDelay is the pause between attempts (default 50ms).
	RetryDelay time.Duration
	// BreakerFailures is how many consecutive failures put a provider aside
	// (0 = default 5, negative = breaker off).
	BreakerFailures int
	// BreakerCooldown is how long a tripped provider stays aside (default 30s).
	BreakerCooldown time.Duration
}

// Defaults for the zero Resilience.
const (
	DefaultRetryDelay      = 50 * time.Millisecond
	DefaultBreakerFailures = 5
	DefaultBreakerCooldown = 30 * time.Second
)

// FailureEvent is passed to Router.OnFailure for every failed attempt.
type FailureEvent struct {
	Capability Name
	Provider   string
	Attempt    int
	Err        error
}

type bstate struct {
	fails     int
	openUntil time.Time
}

type resilience struct {
	mu    sync.Mutex
	cfg   Resilience
	idem  map[Name]bool
	state map[string]*bstate
	now   func() time.Time
}

func newResilience() resilience {
	return resilience{idem: map[Name]bool{}, state: map[string]*bstate{}, now: time.Now}
}

// SetResilience sets the platform-wide retry / breaker defaults.
func (r *Router) SetResilience(c Resilience) {
	r.res.mu.Lock()
	r.res.cfg = c
	r.res.mu.Unlock()
}

// SetIdempotent records that repeating n after it was sent is safe (comes from
// the capability's contract; default is not safe).
func (r *Router) SetIdempotent(n Name, safe bool) {
	r.res.mu.Lock()
	r.res.idem[n] = safe
	r.res.mu.Unlock()
}

// SetFailureLogger sets the callback for failed attempts.
func (r *Router) SetFailureLogger(f func(FailureEvent)) { r.onFailure = f }

func (rs *resilience) breakerParams() (int, time.Duration) {
	n, d := rs.cfg.BreakerFailures, rs.cfg.BreakerCooldown
	if n == 0 {
		n = DefaultBreakerFailures
	}
	if d <= 0 {
		d = DefaultBreakerCooldown
	}
	return n, d
}

func (r *Router) breakerAllow(provider string) bool {
	rs := &r.res
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n, _ := rs.breakerParams()
	if n < 0 {
		return true
	}
	st := rs.state[provider]
	return st == nil || !rs.now().Before(st.openUntil)
}

func (r *Router) breakerResult(provider string, failed bool) {
	rs := &r.res
	rs.mu.Lock()
	defer rs.mu.Unlock()
	n, cool := rs.breakerParams()
	if n < 0 {
		return
	}
	st := rs.state[provider]
	if st == nil {
		st = &bstate{}
		rs.state[provider] = st
	}
	if !failed {
		st.fails = 0
		st.openUntil = time.Time{}
		return
	}
	st.fails++
	if st.fails >= n {
		st.openUntil = rs.now().Add(cool)
		st.fails = 0
	}
}

// retriable says whether a failed attempt may be repeated or handed to the
// next provider.
func (r *Router) retriable(n Name, err error) bool {
	switch {
	case errors.Is(err, ErrUnavailable):
		return true
	case errors.Is(err, ErrAmbiguous):
		r.res.mu.Lock()
		defer r.res.mu.Unlock()
		return r.res.idem[n]
	}
	return false
}

func isInfraFailure(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrAmbiguous)
}

// attempt calls one provider with the circuit breaker and retries applied.
func (r *Router) attempt(ctx context.Context, n Name, provider string, retries int, try func() (any, error)) (any, error) {
	if !r.breakerAllow(provider) {
		return nil, fmt.Errorf("%w: circuit open for %q", ErrUnavailable, provider)
	}
	r.res.mu.Lock()
	if retries == 0 {
		retries = r.res.cfg.Retries
	}
	delay := r.res.cfg.RetryDelay
	r.res.mu.Unlock()
	if delay <= 0 {
		delay = DefaultRetryDelay
	}
	for a := 1; ; a++ {
		out, err := safe(try)
		if err == nil {
			r.breakerResult(provider, false)
			return out, nil
		}
		if isInfraFailure(err) {
			r.breakerResult(provider, true)
			if r.onFailure != nil {
				r.onFailure(FailureEvent{Capability: n, Provider: provider, Attempt: a, Err: err})
			}
		}
		if a > retries || !r.retriable(n, err) || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(delay):
		}
	}
}
