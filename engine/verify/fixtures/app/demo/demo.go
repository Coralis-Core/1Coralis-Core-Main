// Package demo (TEST FIXTURE) assembles a platform from a config using sample
// cores (one of them a "complement": a core that consumes and provides), to prove that swapping a core is a config edit.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
	complement "example.com/project/engine/verify/fixtures/app/product"
	fixtures "example.com/project/engine/verify/fixtures/contracts"
	"example.com/project/engine/verify/fixtures/contracts/identity"
	"example.com/project/engine/verify/fixtures/cores/memory"
)

// Factories registers the in-process core kinds the demo configs can mount.
func Factories() map[string]boot.Factory {
	return map[string]boot.Factory{
		"directory": func(id string, opts json.RawMessage) (kernel.Core, error) {
			var o struct {
				Users map[string]string `json:"users"` // id -> name
			}
			if len(opts) > 0 {
				if err := json.Unmarshal(opts, &o); err != nil {
					return nil, err
				}
			}
			d := memory.NewDirectory(id)
			for k, name := range o.Users {
				var n int64
				if _, err := fmt.Sscan(k, &n); err != nil {
					return nil, err
				}
				d.Users[n] = identity.User{ID: n, Name: name}
			}
			return d, nil
		},
		"counter": func(id string, _ json.RawMessage) (kernel.Core, error) { return memory.NewCounter(id), nil },
		"profile": func(id string, _ json.RawMessage) (kernel.Core, error) { return complement.New(id), nil },
	}
}

// Build assembles and starts a platform from a config document.
func Build(configJSON string) (*boot.Platform, error) {
	cfg, err := config.Parse(strings.NewReader(configJSON))
	if err != nil {
		return nil, err
	}
	set, err := contract.LoadAll(contracts.FS, fixtures.FS)
	if err != nil {
		return nil, err
	}
	p, err := boot.Build(cfg, boot.Options{Contracts: set, Factories: Factories()})
	if err != nil {
		return nil, err
	}
	return p, p.Start(context.Background())
}
