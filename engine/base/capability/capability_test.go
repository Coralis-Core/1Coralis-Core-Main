package capability

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type in struct{ N int }
type out struct{ N int }

var double = Define[in, out]("test.v1.double")

func ok(provider string, mult int) func(context.Context, in) (out, error) {
	return func(_ context.Context, i in) (out, error) { return out{i.N * mult}, nil }
}

func TestOrderBindAndFallback(t *testing.T) {
	r := NewRouter()
	double.Provide(r, "a", func(context.Context, in) (out, error) { return out{}, ErrUnavailable })
	double.Provide(r, "b", ok("b", 2))

	// default: registration order, a is down -> falls back to b
	got, err := double.Call(context.Background(), r, in{5})
	if err != nil || got.N != 10 {
		t.Fatalf("got %v %v", got, err)
	}
	// bind restricts: only a -> all providers failed
	r.Bind("test.v1.double", "a")
	if _, err := double.Call(context.Background(), r, in{5}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
	// bind order b first
	r.Bind("test.v1.double", "b", "a")
	if got, _ := double.Call(context.Background(), r, in{1}); got.N != 2 {
		t.Fatal("bind order ignored")
	}
}

func TestNonUnavailableErrorStopsChain(t *testing.T) {
	r := NewRouter()
	boom := errors.New("business error")
	double.Provide(r, "a", func(context.Context, in) (out, error) { return out{}, boom })
	double.Provide(r, "b", ok("b", 2))
	if _, err := double.Call(context.Background(), r, in{1}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestNoProviderPanicAndTimeout(t *testing.T) {
	r := NewRouter()
	if _, err := double.Call(context.Background(), r, in{1}); !errors.Is(err, ErrNoProvider) {
		t.Fatal(err)
	}
	double.Provide(r, "p", func(context.Context, in) (out, error) { panic("x") })
	double.Provide(r, "q", ok("q", 3))
	// A panic happens after the request was handed over: repeating it elsewhere
	// is only safe when the capability is declared idempotent (default: not).
	if _, err := double.Call(context.Background(), r, in{2}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("panic must NOT fall back by default: %v", err)
	}
	r.SetIdempotent(double.Name, true)
	if got, err := double.Call(context.Background(), r, in{2}); err != nil || got.N != 6 {
		t.Fatalf("panic should fall back once idempotent: %v %v", got, err)
	}
	r2 := NewRouter()
	r2.Timeout = 20 * time.Millisecond
	double.Provide(r2, "slow", func(ctx context.Context, _ in) (out, error) {
		<-ctx.Done()
		return out{}, ctx.Err()
	})
	if _, err := double.Call(context.Background(), r2, in{1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateCatchesTypoAndDuplicate(t *testing.T) {
	r := NewRouter()
	double.Provide(r, "a", ok("a", 1))
	r.Bind("test.v1.double", "nope")
	if err := r.Validate(); err == nil {
		t.Fatal("typo in binding must fail validation")
	}
	if err := double.Provide(r, "a", ok("a", 1)); err == nil {
		t.Fatal("duplicate provider must be rejected")
	}
}

type user struct {
	ID int64 `json:"id"`
}

var getUser = Define[user, user]("test.v1.user.get")

func TestCodedErrorsCrossBoundaries(t *testing.T) {
	e := NewError("not_found", "x")
	wrapped := fmt.Errorf("ctx: %w", e)
	if !errors.Is(wrapped, NewError("not_found", "other message")) {
		t.Fatal("errors.Is must compare by code")
	}
	if errors.Is(wrapped, ErrUnavailable) || CodeOf(wrapped) != "not_found" || CodeOf(errors.New("plain")) != "" {
		t.Fatal("code semantics wrong")
	}
}

func TestRemoteJSONProviderAndMixedChain(t *testing.T) {
	r := NewRouter()
	// remote (JSON only) first, but unavailable -> typed local provider answers
	r.ProvideJSON("remote", "test.v1.user.get", func(context.Context, []byte) ([]byte, error) {
		return nil, fmt.Errorf("dial: %w", ErrUnavailable)
	})
	getUser.Provide(r, "local", func(_ context.Context, u user) (user, error) { return user{u.ID + 1}, nil })
	got, err := getUser.Call(context.Background(), r, user{1})
	if err != nil || got.ID != 2 {
		t.Fatalf("fallback to local failed: %v %v", got, err)
	}
	// remote healthy: typed Call goes through JSON transparently
	r2 := NewRouter()
	r2.ProvideJSON("remote", "test.v1.user.get", func(_ context.Context, in []byte) ([]byte, error) {
		return []byte(`{"id":42}`), nil
	})
	if got, err := getUser.Call(context.Background(), r2, user{1}); err != nil || got.ID != 42 {
		t.Fatalf("remote typed call: %v %v", got, err)
	}
	// a domain error from a remote provider stops the chain (no fallback)
	r3 := NewRouter()
	r3.ProvideJSON("remote", "test.v1.user.get", func(context.Context, []byte) ([]byte, error) {
		return nil, NewError("not_found", "")
	})
	getUser.Provide(r3, "local", func(_ context.Context, u user) (user, error) { return u, nil })
	if _, err := getUser.Call(context.Background(), r3, user{1}); !errors.Is(err, NewError("not_found", "")) {
		t.Fatalf("got %v", err)
	}
}

func TestCallJSONOnTypedProvider(t *testing.T) {
	r := NewRouter()
	getUser.Provide(r, "local", func(_ context.Context, u user) (user, error) { return user{u.ID * 2}, nil })
	out, err := r.CallJSON(context.Background(), "test.v1.user.get", []byte(`{"id":21}`))
	if err != nil || string(out) != `{"id":42}` {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := r.CallJSON(context.Background(), "test.v1.user.get", []byte(`{"id":"x"}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad JSON must be invalid_input, got %v", err)
	}
	if _, err := r.CallJSON(context.Background(), "nope.v1.x", []byte(`{}`)); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("got %v", err)
	}
}

// CallAll asks every bound provider at once: time is ~max, not sum; each
// answer keeps its provenance; a dead/panicking source does not hide the rest.
func TestCallAllIsParallelPartialAndHonorsBind(t *testing.T) {
	r := NewRouter()
	slow := func(mult int, d time.Duration) func(context.Context, in) (out, error) {
		return func(ctx context.Context, i in) (out, error) {
			select {
			case <-time.After(d):
				return out{i.N * mult}, nil
			case <-ctx.Done():
				return out{}, ctx.Err()
			}
		}
	}
	double.Provide(r, "a", slow(2, 120*time.Millisecond))
	double.Provide(r, "b", slow(3, 120*time.Millisecond))
	double.Provide(r, "c", slow(4, 120*time.Millisecond))
	double.Provide(r, "dead", func(context.Context, in) (out, error) { return out{}, ErrUnavailable })
	double.Provide(r, "boom", func(context.Context, in) (out, error) { panic("x") })

	start := time.Now()
	got, err := double.CallAll(context.Background(), r, in{5})
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took > 300*time.Millisecond {
		t.Fatalf("fan-out looks sequential: took %v for 3x120ms sources", took)
	}
	byName := map[string]Answer[out]{}
	for _, a := range got {
		byName[a.Provider] = a
	}
	if len(got) != 5 || got[0].Provider != "a" || got[4].Provider != "boom" {
		t.Fatalf("results must keep chain order and provenance: %+v", got)
	}
	if byName["a"].Value.N != 10 || byName["b"].Value.N != 15 || byName["c"].Value.N != 20 {
		t.Fatalf("wrong values: %+v", got)
	}
	if !errors.Is(byName["dead"].Err, ErrUnavailable) {
		t.Fatalf("dead should be unavailable, got %v", byName["dead"].Err)
	}
	if !errors.Is(byName["boom"].Err, ErrAmbiguous) {
		t.Fatalf("a panicking source is maybe_delivered, got %v", byName["boom"].Err)
	}

	// Bind restricts which sources are asked.
	r.Bind("test.v1.double", "c", "a")
	got, _ = double.CallAll(context.Background(), r, in{1})
	if len(got) != 2 || got[0].Provider != "c" || got[1].Provider != "a" {
		t.Fatalf("bind order/restriction ignored: %+v", got)
	}

	// no provider at all is the only whole-call error
	if _, err := double.CallAll(context.Background(), NewRouter(), in{1}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("want ErrNoProvider, got %v", err)
	}
	// timeout bounds the whole fan-out and shows up per source
	r2 := NewRouter()
	r2.Timeout = 30 * time.Millisecond
	double.Provide(r2, "slow", slow(2, time.Second))
	double.Provide(r2, "fast", slow(3, time.Millisecond))
	got, _ = double.CallAll(context.Background(), r2, in{1})
	if got[1].Err != nil || got[1].Value.N != 3 || got[0].Err == nil {
		t.Fatalf("timeout should hit only the slow source: %+v", got)
	}
}
