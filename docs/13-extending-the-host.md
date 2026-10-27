# 13 · Extending the host (Go)

The bundled `engine/cmd/platform` binary is a thin **host program** with two built-in core kinds, `http` and `gateway`. Anything else (in-process cores, wrapper kinds, other transports such as gRPC)
is added by writing a small Go `main` that calls `boot.Build` with options. Everything below is exercised by `examples/custom-host`, which is tested (`go test ./examples/custom-host`) and runnable:

```bash
go run ./examples/custom-host          # serves on 127.0.0.1:8081
curl -s -X POST 127.0.0.1:8081/v1/call -d '{"capability":"greet.v1.hello","input":{"name":"Sara"}}'
# {"output":{"message":"Hello, Sara!"}}
```

## The pieces

| You write | Interface | Registered in |
|---|---|---|
| an in-process core kind | `boot.Factory` → `kernel.Core` | `boot.Options.Factories["kind"]` |
| a remote core kind (a transport) | `boot.RemoteFactory` → `kernel.Core` | `boot.Options.RemoteFactories["kind"]` |
| a wrapper kind | `boot.WrapperFactory` → `capability.Wrapper` | `boot.Options.Wrappers["kind"]` |
| contracts for your capabilities | JSON files in any `fs.FS` | `contract.LoadAll(contracts.FS, yourFS)` |

```go
type Core interface {                       // package kernel
    Name() string
    Start(ctx context.Context, h *Host) error   // register providers on h.Router; h also has h.Bus and h.Log
    Stop(ctx context.Context) error
}
// Optional, detected automatically:
type Healther interface      { Health(ctx context.Context) (status, detail string) }              // "ok" | "degraded" | "down"
type EventReceiver interface { Deliver(ctx context.Context, event string, payload []byte) error }  // makes a core subscribable

type Factory        func(id string, options json.RawMessage) (kernel.Core, error)
type RemoteFactory  func(c config.Core, o boot.Options) (kernel.Core, error)        // also sees the claimed capabilities
type WrapperFactory func(options json.RawMessage) (capability.Wrapper, error)
```

A factory must return a core whose `Name()` equals the `id` in the config (a mismatch fails the build). Unknown kinds fail the build.

## The typed Go API

Inside the process, capabilities are provided and called with generics:

```go
var Hello = capability.Define[helloIn, helloOut]("greet.v1.hello")

Hello.Provide(h.Router, "greeter", func(ctx context.Context, in helloIn) (helloOut, error) { ... })   // in Start
out, err := Hello.Call(ctx, h.Router, helloIn{Name: "Sara"})                                           // from any core: calls the chain
answers, err := Hello.CallAll(ctx, h.Router, helloIn{Name: "Sara"})                                    // fan-out
```

Return a coded error with `capability.NewError("limit_exceeded", "...")` (and read it with `capability.CodeOf(err)`); an ordinary error is `internal`. A call from Go to an in-process typed provider goes **straight to it, without serialization and without contract validation**: the guard sits at the
edges (transports and mounted remote cores), not inside the router, because both sides share types and a process. If an *outer* wrapper is mounted on the capability, the call is routed
through JSON so the wrapper can see the bytes. Everything that crosses a transport is validated as usual.

## An in-process core

```go
type helloIn struct {
	Name string `json:"name"`
}
type helloOut struct {
	Message string `json:"message"`
}

// Hello is the typed handle of the capability. Callers in Go use Hello.Call(ctx, router, in).
var Hello = capability.Define[helloIn, helloOut]("greet.v1.hello")

type greeter struct{ id string }

func (g *greeter) Name() string { return g.id }
func (g *greeter) Start(_ context.Context, h *kernel.Host) error {
	return Hello.Provide(h.Router, g.id, func(_ context.Context, in helloIn) (helloOut, error) {
		return helloOut{Message: "Hello, " + in.Name + "!"}, nil
	})
}
func (g *greeter) Stop(context.Context) error { return nil }

// newGreeter is a boot.Factory: it builds the core of kind "greeter" from its config entry.
func newGreeter(id string, _ json.RawMessage) (kernel.Core, error) { return &greeter{id: id}, nil }
```

## A wrapper kind

A wrapper is `func(ctx, in []byte, next capability.Next) ([]byte, error)`: do something, call `next`, do something else, return.

```go
func newAudit(options json.RawMessage) (capability.Wrapper, error) {
	var o struct {
		Prefix string `json:"prefix"`
	}
	if len(options) > 0 {
		if err := json.Unmarshal(options, &o); err != nil {
			return nil, fmt.Errorf("audit: %w", err)
		}
	}
	return func(ctx context.Context, in []byte, next capability.Next) ([]byte, error) {
		start := time.Now()
		out, err := next(ctx, in) // "before" code is above this line, "after" code below it
		slog.Info(o.Prefix+"call", "trace", capability.MetaFrom(ctx)[capability.MetaTrace],
			"took", time.Since(start).Round(time.Microsecond), "ok", err == nil)
		return out, err
	}, nil
}
```

## Wiring it together

```go
const platformJSON = `{
  "version": 1,
  "cores":    [ { "id": "greeter", "kind": "greeter" } ],
  "wrappers": [ { "capability": "greet.v1.hello", "kind": "audit", "options": { "prefix": "audit: " } } ],
  "serve":    { "expose": ["greet.v1.hello"] }
}`

// build assembles the platform from a config and your own contract folder.
func build(cfgJSON string, mine fs.FS) (*boot.Platform, *contract.Set, error) {
	cfg, err := config.Parse(strings.NewReader(cfgJSON))
	if err != nil {
		return nil, nil, err
	}
	set, err := contract.LoadAll(contracts.FS, mine) // structural contracts + yours
	if err != nil {
		return nil, nil, err
	}
	o := boot.Options{
		Contracts: set,
		Factories: map[string]boot.Factory{"greeter": newGreeter},
		Wrappers:  map[string]boot.WrapperFactory{"audit": newAudit},
	}
	p, err := boot.Build(cfg, o)
	return p, set, err
}
```

Then serve it (`run` in the example): `p.Start(ctx)`, `http.Server{Handler: p.Handler(set, token, log)}`, `srv.Shutdown(ctx)` then `p.Stop(ctx)` on exit.
`Handler` is the same handler the bundled binary uses (`/v1/call`, `/v1/event`, `/livez`, `/healthz`); pass your bearer token (or `""` for none).

## Adding the gRPC transport

If you only need gRPC and nothing else custom, use the ready-made `platform-grpc` binary ([08](08-wire-protocols.md#the-ready-made-binary-platform-grpc)). To add it to your own host:

```go
o := boot.Options{Contracts: set}
grpcjson.Register(&o)                         // registers kind "grpc" under RemoteFactories
p, err := boot.Build(cfg, o)
// to SERVE gRPC as well:
srv := grpcjson.NewServer(set, p.Host().Router, grpcjson.Options{Token: tok, Allow: p.Exposed, MaxDepth: 8})
g := grpcjson.NewGRPCServer(); srv.Register(g); go g.Serve(listener)
```

`engine/contrib/grpc` is a separate module: require it from your host's `go.mod` (it brings `google.golang.org/grpc`).

## Where host code may live

A host is just a `main` package. In this repository `examples/custom-host` sits in the main module. For a real project, keep your host, your cores and your contracts in your own repository
and import `engine/base/...` and `contracts`; the dependency rules ([03](03-architecture.md#dependency-rules)) apply to code *inside this repository's* folders. A Go core that
lives in `cores/<name>/` may import only `engine/base`, `contracts` and itself.

## Publishing events from Go

`p.Publish(ctx, "orders.v1.placed", payloadJSON)` validates the payload against the event contract and delivers it to the subscribers; it returns an error for an unknown event or an invalid payload.
