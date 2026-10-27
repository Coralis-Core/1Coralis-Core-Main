package grpcjson_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	grpcjson "example.com/project/engine/contrib/grpc"
	"example.com/project/engine/verify/fixtures/app/demo"
	fixtures "example.com/project/engine/verify/fixtures/contracts"
)

const platformA = `{"version":1,"cores":[
  {"id":"dir","kind":"directory","options":{"users":{"7":"Sara"}}}],
 "bindings":[{"capability":"identity.v1.user.get","cores":["dir"]}],
 "serve":{"expose":["identity.v1.user.get"]}}`

func contractSet(t *testing.T) *contract.Set {
	t.Helper()
	set, err := contract.LoadAll(contracts.FS, fixtures.FS)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// serveA starts platform A and a real gRPC server on a free port; returns its address.
func serveA(t *testing.T, opts grpcjson.Options) string {
	t.Helper()
	a, err := demo.Build(platformA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Stop(context.Background()) })
	opts.Allow = func(c string) bool { return a.Exposed(c) }
	srv := grpcjson.NewServer(contractSet(t), a.Host().Router, opts)
	g := grpc.NewServer()
	srv.Register(g)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go g.Serve(l)
	t.Cleanup(g.Stop)
	return l.Addr().String()
}

// buildB mounts A over gRPC and returns B's router.
func buildB(t *testing.T, addr, extraOpts string, getenv func(string) string) *boot.Platform {
	t.Helper()
	cfg, err := config.Parse(strings.NewReader(`{"version":1,"cores":[
	  {"id":"a","kind":"grpc","capabilities":["identity.v1.user.get"],"options":{"target":"` + addr + `"` + extraOpts + `}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	o := boot.Options{Contracts: contractSet(t), Getenv: getenv}
	grpcjson.Register(&o)
	b, err := boot.Build(cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Stop(context.Background()) })
	return b
}

func call(b *boot.Platform, in string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := b.Host().Router.CallJSON(ctx, "identity.v1.user.get", []byte(in))
	return string(out), err
}

func TestEndToEndOverRealGRPC(t *testing.T) {
	b := buildB(t, serveA(t, grpcjson.Options{}), "", nil)

	out, err := call(b, `{"id":7}`)
	if err != nil || !strings.Contains(out, "Sara") {
		t.Fatalf("success path: %q %v", out, err)
	}
	if _, err := call(b, `{"id":99}`); capability.CodeOf(err) != "not_found" {
		t.Fatalf("domain error must pass through as not_found, got %v", err)
	}
	if _, err := call(b, `{"id":"x"}`); err == nil {
		t.Fatal("invalid input must be rejected")
	}
}

func TestTokenIsEnforced(t *testing.T) {
	addr := serveA(t, grpcjson.Options{Token: "s3cret"})
	good := buildB(t, addr, `,"token_env":"T"`, func(string) string { return "s3cret" })
	if _, err := call(good, `{"id":7}`); err != nil {
		t.Fatalf("right token: %v", err)
	}
	bad := buildB(t, addr, `,"token_env":"T"`, func(string) string { return "wrong" })
	// Same as the HTTP transport: "unauthorized" is not a code the contract declares, so the
	// caller sees an internal error (nothing leaks) and A never executed the call.
	if out, err := call(bad, `{"id":7}`); err == nil || capability.CodeOf(err) != "internal" || out != "" {
		t.Fatalf("wrong token must fail as internal with no output, got %q %v", out, err)
	}
	r, _ := grpcjson.Dial(addr)
	set := contractSet(t)
	c, _ := set.Get("identity.v1.user.get")
	if _, code, _ := r.Provider(c)(context.Background(), []byte(`{"id":7}`)); code != "unauthorized" {
		t.Fatalf("no token at the raw wire must be unauthorized, got %q", code)
	}
}

func TestOnlyExposedCapabilitiesAreServed(t *testing.T) {
	addr := serveA(t, grpcjson.Options{})
	r, err := grpcjson.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	set := contractSet(t)
	c, _ := set.Get("identity.v1.chat.get") // exists as a contract but A does not expose it
	_, code, _ := r.Provider(c)(context.Background(), []byte(`{"id":1}`))
	if code != "unknown_capability" {
		t.Fatalf("unexposed capability must be unknown_capability, got %q", code)
	}
}

func TestServerDownIsUnavailable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here
	r, _ := grpcjson.Dial(addr)
	r.Timeout = 2 * time.Second
	set := contractSet(t)
	c, _ := set.Get("identity.v1.user.get")
	_, code, _ := r.Provider(c)(context.Background(), []byte(`{"id":7}`))
	if code != "unavailable" {
		t.Fatalf("got %q", code)
	}
}

func TestLoopGuard(t *testing.T) {
	addr := serveA(t, grpcjson.Options{MaxDepth: 1})
	r, _ := grpcjson.Dial(addr)
	set := contractSet(t)
	c, _ := set.Get("identity.v1.user.get")
	ctx := capability.WithMeta(context.Background(), capability.Meta{"depth": "1"})
	_, code, _ := r.Provider(c)(ctx, []byte(`{"id":7}`))
	if code != "depth_exceeded" {
		t.Fatalf("a call already at max depth must be refused, got %q", code)
	}
}

func TestBodyCannotForgeAuthorization(t *testing.T) {
	srv := grpcjson.NewServer(contractSet(t), nil, grpcjson.Options{Token: "s3cret"})
	resp, _ := srv.Handle(context.Background(), &grpcjson.CallRequest{
		Capability: "identity.v1.user.get", Input: []byte(`{"id":7}`),
		Meta: map[string]string{"authorization": "Bearer s3cret"}})
	if resp.ErrorCode != "unauthorized" {
		t.Fatalf("token in the body must not count, got %+v", resp)
	}
}
