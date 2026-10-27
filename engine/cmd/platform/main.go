// Command platform mounts what config/platform.json declares and serves the
// result through the wire contract (POST /v1/call) plus GET /healthz.
// It has no frontend and no backend of its own: anything that can speak the
// wire contract (in any language) is your top layer, and another platform can
// mount this one as a core.
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
	"path/filepath"
	"syscall"
	"time"

	"example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "platform:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "config/platform.json", "configuration file")
	contractsDir := flag.String("contracts", "", "contracts folder to use instead of the embedded structural ones (e.g. ./contracts, to include your own)")
	listen := flag.String("listen", "", "address to serve on (overrides serve.listen; default 127.0.0.1:8080)")
	printCfg := flag.Bool("print-config", false, "print the FINAL config (defaults filled in) and which keys came from defaults, then exit")
	flag.Parse()
	log := slog.Default()

	f, err := os.Open(*cfgPath)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", *cfgPath, err)
	}

	if *printCfg {
		eff, from := cfg.WithDefaults()
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"config": eff, "from_defaults": from})
	}

	// Contracts: the structural ones (or the folder given with -contracts), plus
	// every folder in contracts_dirs (e.g. the contracts/ of a nested project).
	// Relative paths in contracts_dirs are relative to the config file.
	var srcs []fs.FS
	if *contractsDir != "" {
		srcs = append(srcs, os.DirFS(*contractsDir))
	} else {
		srcs = append(srcs, contracts.FS)
	}
	for _, d := range cfg.ContractsDirs {
		if !filepath.IsAbs(d) {
			d = filepath.Join(filepath.Dir(*cfgPath), d)
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

	p, err := boot.Build(cfg, boot.Options{Contracts: set, Log: log})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := p.Start(ctx); err != nil {
		return err
	}
	defer p.Stop(context.Background())

	addr := cfg.Serve.Listen
	if *listen != "" {
		addr = *listen
	}
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv := &http.Server{Addr: addr, Handler: p.Handler(set, token, log), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("platform serving", "addr", addr, "cores", len(cfg.Cores), "bindings", len(cfg.Bindings))
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
