// custom-host: a minimal Go host program that adds three things the bundled platform
// binary does not have: an in-process core kind, a wrapper kind, and its own contracts.
//
//	go run ./examples/custom-host                 serves on 127.0.0.1:8081
//	curl -s -X POST 127.0.0.1:8081/v1/call -d '{"capability":"greet.v1.hello","input":{"name":"Sara"}}'
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
)

// ---- 1. an in-process core, written with the typed API -----------------------------------------

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

// ---- 2. a wrapper kind: an around-decorator that logs every call ------------------------------

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

// ---- 3. wiring ----------------------------------------------------------------------------------

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

func main() {
	listen := flag.String("listen", "127.0.0.1:8081", "address to serve on")
	flag.Parse()
	if err := run(*listen); err != nil {
		fmt.Fprintln(os.Stderr, "custom-host:", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	log := slog.Default()
	p, set, err := build(platformJSON, os.DirFS("examples/custom-host/contracts"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := p.Start(ctx); err != nil {
		return err
	}
	defer p.Stop(context.Background())
	srv := &http.Server{Addr: addr, Handler: p.Handler(set, "", log), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("custom host serving", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sd); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
