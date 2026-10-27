package capability

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMetaCarriedCopiedAndEmptyIsNil(t *testing.T) {
	if MetaFrom(context.Background()) != nil {
		t.Fatal("no meta must read as nil")
	}
	in := Meta{"trace_id": "t1"}
	ctx := WithMeta(context.Background(), in)
	in["trace_id"] = "changed-after"
	got := MetaFrom(ctx)
	if got["trace_id"] != "t1" {
		t.Fatalf("WithMeta must copy: %v", got)
	}
	got["trace_id"] = "changed-after-read"
	if MetaFrom(ctx)["trace_id"] != "t1" {
		t.Fatal("MetaFrom must return a copy")
	}
	if WithMeta(ctx, nil) != ctx {
		t.Fatal("empty meta must leave the context unchanged")
	}
}

func provide(t *testing.T, r *Router, provider, capName string, fn JSONHandler) {
	t.Helper()
	if err := r.ProvideJSON(provider, Name(capName), fn); err != nil {
		t.Fatal(err)
	}
}

func okJSON(s string) JSONHandler {
	return func(context.Context, []byte) ([]byte, error) { return []byte(s), nil }
}

func downJSON() JSONHandler {
	return func(context.Context, []byte) ([]byte, error) { return nil, ErrUnavailable }
}

func TestModeSingleNeverFallsThrough(t *testing.T) {
	r := NewRouter()
	provide(t, r, "a", "x.v1.y", downJSON())
	provide(t, r, "b", "x.v1.y", okJSON(`"b"`))
	r.Bind("x.v1.y", "a", "b")

	// fallback: a is down, b answers
	r.SetPolicy("x.v1.y", Policy{Mode: ModeFallback})
	if out, err := r.CallJSON(context.Background(), "x.v1.y", []byte(`{}`)); err != nil || string(out) != `"b"` {
		t.Fatalf("fallback: %s %v", out, err)
	}
	// single: only a is used, and it is down
	r.SetPolicy("x.v1.y", Policy{Mode: ModeSingle})
	if out, err := r.CallJSON(context.Background(), "x.v1.y", []byte(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("single must not fall through to b: %s %v", out, err)
	}
}

func TestModeMismatchIsACodedError(t *testing.T) {
	r := NewRouter()
	provide(t, r, "a", "x.v1.y", okJSON(`"a"`))
	provide(t, r, "b", "x.v1.y", okJSON(`"b"`))
	ctx := context.Background()

	r.SetPolicy("x.v1.y", Policy{Mode: ModeFanout})
	if _, err := r.CallJSON(ctx, "x.v1.y", []byte(`{}`)); !errors.Is(err, ErrFanoutOnly) {
		t.Fatalf("single-result call on a fanout capability: %v", err)
	}
	if res, err := r.CallAllJSON(ctx, "x.v1.y", []byte(`{}`)); err != nil || len(res) != 2 {
		t.Fatalf("fan-out call on a fanout capability: %v %v", res, err)
	}

	for _, m := range []Mode{ModeSingle, ModeFallback} {
		r.SetPolicy("x.v1.y", Policy{Mode: m})
		if _, err := r.CallAllJSON(ctx, "x.v1.y", []byte(`{}`)); !errors.Is(err, ErrNotFanout) {
			t.Fatalf("fan-out call on mode %d: %v", m, err)
		}
	}

	// lenient default: both are allowed
	r.SetPolicy("x.v1.y", Policy{})
	if _, err := r.CallJSON(ctx, "x.v1.y", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CallAllJSON(ctx, "x.v1.y", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestFanoutOnErrorSkipVsFail(t *testing.T) {
	r := NewRouter()
	provide(t, r, "good", "x.v1.y", okJSON(`"g"`))
	provide(t, r, "bad", "x.v1.y", downJSON())
	ctx := context.Background()

	r.SetPolicy("x.v1.y", Policy{Mode: ModeFanout, OnError: OnErrorSkip})
	res, err := r.CallAllJSON(ctx, "x.v1.y", []byte(`{}`))
	if err != nil || len(res) != 2 || res[0].Err != nil || res[1].Err == nil {
		t.Fatalf("skip keeps the good answer and marks the bad source: %+v %v", res, err)
	}

	r.SetPolicy("x.v1.y", Policy{Mode: ModeFanout, OnError: OnErrorFail})
	res, err = r.CallAllJSON(ctx, "x.v1.y", []byte(`{}`))
	if res != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fail must fail the whole call with the source's error: %+v %v", res, err)
	}
}

func TestPolicyTimeoutOverridesRouterTimeout(t *testing.T) {
	slow := func(ctx context.Context, _ []byte) ([]byte, error) {
		select {
		case <-time.After(300 * time.Millisecond):
			return []byte(`"late"`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r := NewRouter()
	r.Timeout = 5 * time.Second // generous default
	provide(t, r, "a", "x.v1.y", slow)

	r.SetPolicy("x.v1.y", Policy{Timeout: 30 * time.Millisecond})
	start := time.Now()
	if _, err := r.CallJSON(context.Background(), "x.v1.y", []byte(`{}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the policy deadline, got %v", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("policy timeout was not applied")
	}
	if p := r.PolicyOf("x.v1.y"); p.Timeout != 30*time.Millisecond {
		t.Fatalf("PolicyOf: %+v", p)
	}
}
