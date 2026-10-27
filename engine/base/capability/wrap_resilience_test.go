package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func rawCall(t *testing.T, r *Router, n Name, in string) (string, error) {
	t.Helper()
	b, err := r.CallJSON(context.Background(), n, []byte(in))
	return string(b), err
}

func TestWrappersAroundOrderOuterInnerAndError(t *testing.T) {
	r := NewRouter()
	double.Provide(r, "a", ok("a", 2))
	tag := func(name string) Wrapper {
		return func(ctx context.Context, in []byte, next Next) ([]byte, error) {
			var m map[string]any
			_ = json.Unmarshal(in, &m)
			m["trace"] = m["trace"].(string) + name
			in2, _ := json.Marshal(m)
			return next(ctx, in2)
		}
	}
	_ = tag
	// outer wrappers: first added = outermost; they see the call in list order.
	var order []string
	mk := func(name string) Wrapper {
		return func(ctx context.Context, in []byte, next Next) ([]byte, error) {
			order = append(order, name+":before")
			out, err := next(ctx, in)
			order = append(order, name+":after")
			return out, err
		}
	}
	r.AddWrapper(double.Name, Outer, mk("o1"))
	r.AddWrapper(double.Name, Outer, mk("o2"))
	r.AddWrapper(double.Name, Inner, mk("i1"))
	if _, err := rawCall(t, r, double.Name, `{"N":4}`); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "o1:before,o2:before,i1:before,i1:after,o2:after,o1:after" {
		t.Fatalf("order: %s", got)
	}
	// typed calls go through the wrappers too
	order = nil
	if got, err := double.Call(context.Background(), r, in{5}); err != nil || got.N != 10 || len(order) != 6 {
		t.Fatalf("typed call must be wrapped: %v %v %v", got, err, order)
	}
	// a wrapper may replace the output; its error propagates, never swallowed
	r2 := NewRouter()
	double.Provide(r2, "a", ok("a", 2))
	r2.AddWrapper(double.Name, Outer, func(ctx context.Context, in []byte, next Next) ([]byte, error) {
		return nil, NewError("denied", "policy")
	})
	if _, err := rawCall(t, r2, double.Name, `{"N":1}`); CodeOf(err) != "denied" {
		t.Fatalf("wrapper error must propagate: %v", err)
	}
}

func TestInnerWrapperRunsPerSourceInFanout(t *testing.T) {
	r := NewRouter()
	double.Provide(r, "a", ok("a", 1))
	double.Provide(r, "b", ok("b", 1))
	var n atomic.Int32
	r.AddWrapper(double.Name, Inner, func(ctx context.Context, in []byte, next Next) ([]byte, error) {
		n.Add(1)
		return next(ctx, in)
	})
	if _, err := double.CallAll(context.Background(), r, in{1}); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatalf("inner wrapper must run once per source, ran %d", n.Load())
	}
}

func flaky(failTimes int32, err error) (func(context.Context, in) (out, error), *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context, in) (out, error) {
		if calls.Add(1) <= failTimes {
			return out{}, err
		}
		return out{N: 1}, nil
	}, &calls
}

func TestRetryOnlyWhenSafe(t *testing.T) {
	// unavailable (nothing was processed): retried
	r := NewRouter()
	r.SetResilience(Resilience{Retries: 2, RetryDelay: time.Millisecond})
	fn, calls := flaky(2, ErrUnavailable)
	double.Provide(r, "a", fn)
	if _, err := double.Call(context.Background(), r, in{1}); err != nil || calls.Load() != 3 {
		t.Fatalf("unavailable must be retried: err=%v calls=%d", err, calls.Load())
	}
	// maybe_delivered, not idempotent: NOT retried
	r2 := NewRouter()
	r2.SetResilience(Resilience{Retries: 2, RetryDelay: time.Millisecond})
	fn2, calls2 := flaky(1, ErrAmbiguous)
	double.Provide(r2, "a", fn2)
	if _, err := double.Call(context.Background(), r2, in{1}); !errors.Is(err, ErrAmbiguous) || calls2.Load() != 1 {
		t.Fatalf("non-idempotent must not be repeated: err=%v calls=%d", err, calls2.Load())
	}
	// same, but declared idempotent: retried
	r3 := NewRouter()
	r3.SetResilience(Resilience{Retries: 2, RetryDelay: time.Millisecond})
	r3.SetIdempotent(double.Name, true)
	fn3, calls3 := flaky(1, ErrAmbiguous)
	double.Provide(r3, "a", fn3)
	if _, err := double.Call(context.Background(), r3, in{1}); err != nil || calls3.Load() != 2 {
		t.Fatalf("idempotent must be retried: err=%v calls=%d", err, calls3.Load())
	}
}

func TestBackupOnlyWhenSafeAndFailuresAreLogged(t *testing.T) {
	r := NewRouter()
	var logged atomic.Int32
	r.SetFailureLogger(func(FailureEvent) { logged.Add(1) })
	fn, _ := flaky(100, ErrAmbiguous)
	double.Provide(r, "a", fn)
	double.Provide(r, "b", ok("b", 1))
	if _, err := double.Call(context.Background(), r, in{1}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("no backup after a sent request when not idempotent: %v", err)
	}
	if logged.Load() != 1 {
		t.Fatalf("each failure must be logged, got %d", logged.Load())
	}
	// failure before delivery: backup is fine
	r2 := NewRouter()
	fn2, _ := flaky(100, ErrUnavailable)
	double.Provide(r2, "a", fn2)
	double.Provide(r2, "b", ok("b", 3))
	if got, err := double.Call(context.Background(), r2, in{1}); err != nil || got.N != 3 {
		t.Fatalf("backup after unavailable: %v %v", got, err)
	}
}

func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	r := NewRouter()
	now := time.Now()
	r.res.now = func() time.Time { return now }
	r.SetResilience(Resilience{BreakerFailures: 2, BreakerCooldown: time.Minute})
	fn, calls := flaky(2, ErrUnavailable)
	double.Provide(r, "a", fn)
	for i := 0; i < 2; i++ {
		_, _ = double.Call(context.Background(), r, in{1})
	}
	before := calls.Load()
	_, err := double.Call(context.Background(), r, in{1})
	if !errors.Is(err, ErrUnavailable) || calls.Load() != before {
		t.Fatalf("an open circuit must not call the provider: err=%v calls=%d", err, calls.Load())
	}
	now = now.Add(2 * time.Minute)
	if _, err := double.Call(context.Background(), r, in{1}); err != nil {
		t.Fatalf("after the cooldown the provider is tried again: %v", err)
	}
	// breaker can be turned off
	r2 := NewRouter()
	r2.SetResilience(Resilience{BreakerFailures: -1})
	fn2, calls2 := flaky(50, ErrUnavailable)
	double.Provide(r2, "a", fn2)
	for i := 0; i < 10; i++ {
		_, _ = double.Call(context.Background(), r2, in{1})
	}
	if calls2.Load() != 10 {
		t.Fatalf("breaker off: every call reaches the provider, got %d", calls2.Load())
	}
}
