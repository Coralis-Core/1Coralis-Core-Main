// Command platform-grpc is the bundled platform plus the gRPC transport:
//
//   - core kind "grpc" (mount a remote platform or core over JSON-gRPC), and
//   - an optional gRPC listener (-grpc-listen) that serves the same capabilities as HTTP.
//
// It reads the same configuration as engine/cmd/platform. The bearer token (serve.token_env)
// and the exposure list (serve.expose) apply to gRPC exactly as to HTTP, and limits.max_depth
// is the loop guard for both. It lives in the gRPC module so the main module keeps zero dependencies.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	grpcjson "example.com/project/engine/contrib/grpc"
)

type options struct {
	cfgPath, contractsDir, listen, grpcListen string
	printCfg                                  bool
}

func main() {
	var o options
	flag.StringVar(&o.cfgPath, "config", "config/platform.json", "configuration file")
	flag.StringVar(&o.contractsDir, "contracts", "", "contracts folder to use instead of the embedded structural ones")
	flag.StringVar(&o.listen, "listen", "", "HTTP address (overrides serve.listen; default 127.0.0.1:8080)")
	flag.StringVar(&o.grpcListen, "grpc-listen", "", "also serve gRPC on this address (empty = HTTP only), e.g. 127.0.0.1:9000")
	flag.BoolVar(&o.printCfg, "print-config", false, "print the FINAL config (defaults filled in), then exit")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o, nil); err != nil {
		fmt.Fprintln(os.Stderr, "platform-grpc:", err)
		os.Exit(1)
	}
}

// run builds the platform from the config and serves until ctx ends. ready (optional) is
// called with the real HTTP and gRPC addresses once both are listening (used by tests).
func run(ctx context.Context, o options, ready func(httpAddr, grpcAddr string)) error {
	log := slog.Default()
	f, err := os.Open(o.cfgPath)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", o.cfgPath, err)
	}
	if o.printCfg {
		eff, from := cfg.WithDefaults()
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"config": eff, "from_defaults": from})
	}

	var srcs []fs.FS
	if o.contractsDir != "" {
		srcs = append(srcs, os.DirFS(o.contractsDir))
	} else {
		srcs = append(srcs, contracts.FS)
	}
	for _, d := range cfg.ContractsDirs {
		if !filepath.IsAbs(d) {
			d = filepath.Join(filepath.Dir(o.cfgPath), d)
		}
		srcs = append(srcs, os.DirFS(d))
	}
	set, err := contract.LoadAll(srcs...)
	if err != nil {
		return err
	}

	token := ""
	if cfg.Serve.TokenEnv != "" {
		if token = os.Getenv(cfg.Serve.TokenEnv); token == "" {
			return fmt.Errorf("serve.token_env %q is set but the environment variable is empty", cfg.Serve.TokenEnv)
		}
	}

	bo := boot.Options{Contracts: set, Log: log}
	grpcjson.Register(&bo) // adds core kind "grpc"
	p, err := boot.Build(cfg, bo)
	if err != nil {
		return err
	}
	if err := p.Start(ctx); err != nil {
		return err
	}
	defer p.Stop(context.Background())

	httpAddr := cfg.Serve.Listen
	if o.listen != "" {
		httpAddr = o.listen
	}
	if httpAddr == "" {
		httpAddr = "127.0.0.1:8080"
	}
	hl, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: p.Handler(set, token, log), ReadHeaderTimeout: 5 * time.Second}

	var gl net.Listener
	gs := grpcjson.NewGRPCServer()
	if o.grpcListen != "" {
		if gl, err = net.Listen("tcp", o.grpcListen); err != nil {
			hl.Close()
			return err
		}
		grpcjson.NewServer(set, p.Host().Router, grpcjson.Options{
			Token: token, Allow: p.Exposed, MaxDepth: cfg.Limits.MaxDepth, Log: log,
		}).Register(gs)
	}

	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(hl) }()
	grpcAddr := ""
	if gl != nil {
		grpcAddr = gl.Addr().String()
		go func() { errc <- gs.Serve(gl) }()
	}
	log.Info("platform serving", "http", hl.Addr().String(), "grpc", grpcAddr, "cores", len(cfg.Cores), "bindings", len(cfg.Bindings))
	if ready != nil {
		ready(hl.Addr().String(), grpcAddr)
	}

	select {
	case err := <-errc:
		gs.Stop()
		return err
	case <-ctx.Done():
		sd, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		gs.GracefulStop()
		if err := srv.Shutdown(sd); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
