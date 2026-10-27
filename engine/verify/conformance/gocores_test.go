package conformance

import (
	"context"
	"testing"
	"testing/fstest"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/verify/fixtures/contracts"
	"example.com/project/engine/verify/fixtures/contracts/identity"
	"example.com/project/engine/verify/fixtures/contracts/stats"
	"example.com/project/engine/verify/fixtures/cores/memory"
)

func codeOf(err error) string { return capability.CodeOf(err) }

func TestGoCoresConformToContracts(t *testing.T) {
	set, err := contract.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	p := kernel.New(config.Config{}, nil)
	p.RequireContracts(set)
	dir := memory.NewDirectory("dir")
	dir.Users[7] = identity.User{ID: 7, Username: "sara", Name: "Sara", Language: "fa"}
	dir.Users[8] = identity.User{ID: 8}
	dir.Users[5000000000] = identity.User{ID: 5000000000, Username: "big"}
	dir.Chats[1] = identity.Chat{ID: 1, Name: "Team", Language: "fa"}
	dir.Chats[2] = identity.Chat{ID: 2}
	dir.Chats[-1001234567890] = identity.Chat{ID: -1001234567890, Name: "Supergroup"}
	for _, c := range []kernel.Core{dir, memory.NewCounter("counter")} {
		if err := p.AddCore(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	r := p.Host().Router
	ctx := context.Background()

	cases := map[string]contract.Provider{
		"identity.v1.user.get": contract.JSONAdapter(func(ctx context.Context, q identity.UserQuery) (identity.User, error) {
			return identity.GetUser.Call(ctx, r, q)
		}, codeOf),
		"identity.v1.chat.get": contract.JSONAdapter(func(ctx context.Context, q identity.ChatQuery) (identity.Chat, error) {
			return identity.GetChat.Call(ctx, r, q)
		}, codeOf),
		"stats.v1.messages.count": contract.JSONAdapter(func(ctx context.Context, q stats.CountQuery) (stats.Count, error) {
			return stats.MessageCount.Call(ctx, r, q)
		}, codeOf),
	}
	for name, prov := range cases {
		c, _ := set.Get(name)
		if problems := contract.Check(ctx, c, contract.Guard(c, prov)); len(problems) > 0 {
			t.Errorf("%s not conformant: %v", name, problems)
		}
	}
}

// A core that registers a capability with no contract must not start.
func TestUnknownCapabilityRefusedAtStartup(t *testing.T) {
	p := kernel.New(config.Config{}, nil)
	// an empty contract set simulates a core registering a capability nobody specified
	empty, err := contract.Load(fstest.MapFS{})
	if err != nil {
		t.Fatal(err)
	}
	p.RequireContracts(empty)
	p.AddCore(memory.NewCounter("counter"))
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("capability without contract must be rejected")
	}
}

// Go's decoder accepts {"ID":7} for a field tagged "id"; Guard (schema) does not.
func TestGuardIsStricterThanGoDecoder(t *testing.T) {
	set, _ := contract.Load(contracts.FS)
	c, _ := set.Get("identity.v1.user.get")
	lenient := contract.JSONAdapter(func(_ context.Context, q identity.UserQuery) (identity.User, error) {
		return identity.User{ID: q.ID}, nil
	}, codeOf)
	if _, _, err := lenient(context.Background(), []byte(`{"ID":7}`)); err != nil {
		t.Fatalf("premise: Go decoder should accept wrong-case key, got %v", err)
	}
	if _, code, _ := contract.Guard(c, lenient)(context.Background(), []byte(`{"ID":7}`)); code != "invalid_input" {
		t.Fatalf("Guard must reject input that violates the schema, got code %q", code)
	}
	bad := func(context.Context, []byte) ([]byte, string, error) { return []byte(`{"id":"x"}`), "", nil }
	if _, _, err := contract.Guard(c, bad)(context.Background(), []byte(`{"id":7}`)); err == nil {
		t.Fatal("Guard must reject output that violates the schema")
	}
	if _, _, err := contract.Guard(c, func(context.Context, []byte) ([]byte, string, error) { return nil, "weird", nil })(context.Background(), []byte(`{"id":7}`)); err == nil {
		t.Fatal("Guard must reject undeclared error codes")
	}
}
