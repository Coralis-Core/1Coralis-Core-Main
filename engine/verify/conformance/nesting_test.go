package conformance

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/base/transports/httpjson"
	"example.com/project/engine/verify/fixtures/contracts/identity"
)

// chain builds depth nested platforms: level 0 owns the Go cores; every higher
// level has NO data of its own and only forwards (through HTTP) to the level
// below. Returns the top router and a stop function for each level.
func chain(t *testing.T, set *contract.Set, depth int) (top *capability.Router, kill []func()) {
	t.Helper()
	cur := goPlatform(t, set).Host().Router
	for i := 0; i < depth; i++ {
		srv := httptest.NewServer(httpjson.NewHandler(set, cur, httpjson.Options{}))
		t.Cleanup(srv.Close)
		kill = append(kill, srv.Close)
		up := kernel.New(config.Config{}, nil)
		rm := httpjson.Remote{BaseURL: srv.URL}
		if err := rm.Bind(up.Host().Router, fmt.Sprintf("below-%d", i), set, requestCaps...); err != nil {
			t.Fatal(err)
		}
		cur = up.Host().Router
	}
	return cur, kill
}

// A platform can be a core of another platform, repeatedly: the contracts are
// the only interface, so a 4-level nest answers the same contract examples,
// propagates domain errors, and reports a dead level as "unavailable".
func TestPlatformsNestAsCoresOfEachOther(t *testing.T) {
	set := loadSet(t)
	top, kill := chain(t, set, 4)
	for _, name := range requestCaps {
		c, _ := set.Get(name)
		prov := func(ctx context.Context, in []byte) ([]byte, string, error) {
			out, err := top.CallJSON(ctx, capability.Name(name), in)
			if err != nil {
				if code := capability.CodeOf(err); code != "" {
					return nil, code, nil
				}
				return nil, "", err
			}
			return out, "", nil
		}
		if problems := contract.Check(context.Background(), c, contract.Guard(c, prov)); len(problems) > 0 {
			t.Errorf("%s through 4 nested levels: %v", name, problems)
		}
	}
	// kill the middle level: the top must say "unavailable", not hang or lie.
	kill[1]()
	_, err := identity.GetUser.Call(context.Background(), top, identity.UserQuery{ID: 7})
	if capability.CodeOf(err) != "unavailable" {
		t.Fatalf("want unavailable with a dead level, got %v", err)
	}
}

// Measurement, not an assertion: what does each extra level cost?
func TestNestingLatencyReport(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	set := loadSet(t)
	for _, depth := range []int{0, 1, 2, 4, 8} {
		top, _ := chain(t, set, depth)
		const n = 300
		start := time.Now()
		for i := 0; i < n; i++ {
			if _, err := identity.GetUser.Call(context.Background(), top, identity.UserQuery{ID: 7}); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("depth %d: %v per call", depth, time.Since(start)/n)
	}
}
