// Package config is the platform's mounting surface: one JSON file that says
// which cores are mounted, how to reach them, and which core serves which
// capability. Mounting or swapping something = editing this file.
//
// The language-neutral schema is contracts/config/v1/platform.config.schema.json;
// Parse enforces the same shape plus the cross-reference rules a schema cannot
// express (unique ids, bindings only point at declared cores).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Version is the only config version this engine understands.
const Version = 1

type Core struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // "http" is built in; other kinds come from the host program
	// Capabilities this core serves. Empty = discover them from the core itself
	// (core.v1.describe).
	Capabilities []string        `json:"capabilities,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"` // meaning depends on Kind
}

// RolePrefix marks an entry of a binding's cores as a role reference
// ("role:main") instead of a core id.
const RolePrefix = "role:"

// Modes of a binding: how the capability may be consumed.
const (
	ModeSingle   = "single"   // exactly one core, no fallback
	ModeFallback = "fallback" // first healthy core, in order
	ModeFanout   = "fanout"   // every core at once
)

// What a fan-out does when some source fails.
const (
	OnErrorSkip = "skip" // failed sources are set aside, the others answer
	OnErrorFail = "fail" // any failed source fails the whole call
)

type Binding struct {
	Capability string `json:"capability"`
	// Cores are core ids or "role:<name>", in preference order.
	Cores []string `json:"cores"`
	// Mode is single, fallback or fanout. Empty = lenient default (both the
	// single-result and the fan-out calls are allowed).
	Mode string `json:"mode,omitempty"`
	// OnError is only valid with mode fanout. Empty = skip.
	OnError string `json:"on_error,omitempty"`
	// TimeoutMS bounds the call for this capability (0 = default_timeout_ms).
	// In a fan-out every source runs at once, so it is each source's budget.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// Retries is how many extra attempts a retriable failure gets for this
	// capability (0 = the platform default, resilience.retries). A failure after
	// the request was sent is only retried when the contract says idempotent.
	Retries int `json:"retries,omitempty"`
}

// Placement of a wrapper.
const (
	PlaceOuter = "outer" // around the whole call (for a fan-out: gates it)
	PlaceInner = "inner" // around each source
)

// Wrapper mounts an around-decorator on a capability without touching any
// core. Kind selects a WrapperFactory registered by the host program; the
// order of the list is the order of wrapping (first = outermost).
type Wrapper struct {
	Capability string          `json:"capability"`
	Kind       string          `json:"kind"`
	Placement  string          `json:"placement,omitempty"` // outer (default) | inner
	Options    json.RawMessage `json:"options,omitempty"`
	// CheckContract: the wrapper's output is checked against the capability's
	// contract. nil = true (default); set false to accept the risk.
	CheckContract *bool `json:"check_contract,omitempty"`
}

// Subscription delivers an event topic to a core. Delivery is best effort: a
// subscriber that is down at send time misses the event.
type Subscription struct {
	Event     string `json:"event"` // an event contract name
	Core      string `json:"core"`  // core id or "role:<name>"
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// Resilience: platform-wide defaults for retries and the circuit breaker.
type Resilience struct {
	Retries         int `json:"retries,omitempty"`             // extra attempts (default 0)
	RetryDelayMS    int `json:"retry_delay_ms,omitempty"`      // default 50
	BreakerFailures int `json:"breaker_failures,omitempty"`    // default 5; -1 = off
	BreakerCooldown int `json:"breaker_cooldown_ms,omitempty"` // default 30000
}

// Limits: protections against runaway nesting. All default to OFF (unlimited):
// the user turns them on.
type Limits struct {
	MaxDepth int `json:"max_depth,omitempty"` // 0 = unlimited
}

type Serve struct {
	Listen   string `json:"listen,omitempty"`
	TokenEnv string `json:"token_env,omitempty"` // NAME of the env var holding the bearer token (never the secret itself)
	// Expose lists the capabilities this platform shows to callers; "*" shows
	// everything. Empty = only the structural core.v1.* capabilities, so nothing
	// behind the platform leaks unless you say so.
	Expose []string `json:"expose,omitempty"`
}

type Config struct {
	Version          int    `json:"version"`
	DefaultTimeoutMS int    `json:"default_timeout_ms,omitempty"`
	Cores            []Core `json:"cores,omitempty"`
	// Roles groups cores under a name; a binding refers to a group as
	// "role:<name>". A role is only a grouping: swapping a core is editing
	// one line here. How the group is consumed is set on the binding.
	Roles    map[string][]string `json:"roles,omitempty"`
	Bindings []Binding           `json:"bindings,omitempty"`
	// Wrappers decorate capabilities (see Wrapper).
	Wrappers []Wrapper `json:"wrappers,omitempty"`
	// Subscriptions route events to cores (see Subscription).
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	Resilience    Resilience     `json:"resilience,omitempty"`
	Limits        Limits         `json:"limits,omitempty"`
	// ContractsDirs lists extra contract folders (e.g. the contracts/ of a
	// nested project) merged with the structural contracts.
	ContractsDirs []string `json:"contracts_dirs,omitempty"`
	Serve         Serve    `json:"serve,omitempty"`
	// Extensions is free space for whatever you mount on top. The engine never reads it.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// Expand turns a binding's cores into plain core ids: every "role:<name>" is
// replaced by that role's cores, in order, and a core that appears twice is
// kept once (first position wins).
func (c Config) Expand(b Binding) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, e := range b.Cores {
		if name, ok := strings.CutPrefix(e, RolePrefix); ok {
			for _, id := range c.Roles[name] {
				add(id)
			}
			continue
		}
		add(e)
	}
	return out
}

// Parse reads and validates a config. Unknown fields are errors.
func Parse(r io.Reader) (Config, error) {
	var c Config
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("config: trailing data after the JSON document")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate checks everything a JSON Schema cannot express.
func (c Config) Validate() error {
	var errs []error
	if c.Version != Version {
		errs = append(errs, fmt.Errorf("version must be %d (got %d)", Version, c.Version))
	}
	if c.DefaultTimeoutMS < 0 {
		errs = append(errs, errors.New("default_timeout_ms must not be negative"))
	}
	ids := map[string]bool{}
	for i, k := range c.Cores {
		switch {
		case k.ID == "":
			errs = append(errs, fmt.Errorf("cores[%d]: id is required", i))
		case ids[k.ID]:
			errs = append(errs, fmt.Errorf("cores[%d]: duplicate id %q", i, k.ID))
		}
		ids[k.ID] = true
		if k.Kind == "" {
			errs = append(errs, fmt.Errorf("cores[%d] (%s): kind is required", i, k.ID))
		}
		for _, cn := range k.Capabilities {
			if cn == "" {
				errs = append(errs, fmt.Errorf("cores[%d] (%s): capabilities must not contain empty names", i, k.ID))
			}
		}
	}
	for _, n := range c.Serve.Expose {
		if n == "" {
			errs = append(errs, errors.New("serve.expose must not contain empty names"))
		}
	}
	roleNames := make([]string, 0, len(c.Roles))
	for name := range c.Roles {
		roleNames = append(roleNames, name)
	}
	sort.Strings(roleNames)
	for _, name := range roleNames {
		switch {
		case name == "":
			errs = append(errs, errors.New("roles: a role name must not be empty"))
		case strings.Contains(name, ":"):
			errs = append(errs, fmt.Errorf("roles: name %q must not contain ':'", name))
		}
		members := c.Roles[name]
		if len(members) == 0 {
			errs = append(errs, fmt.Errorf("roles[%s]: needs at least one core (a role nobody plays is an error, not a silent no-op)", name))
		}
		for _, id := range members {
			if !ids[id] {
				errs = append(errs, fmt.Errorf("roles[%s]: core %q is not declared in cores", name, id))
			}
		}
	}
	seen := map[string]bool{}
	for i, b := range c.Bindings {
		switch {
		case b.Capability == "":
			errs = append(errs, fmt.Errorf("bindings[%d]: capability is required", i))
		case seen[b.Capability]:
			errs = append(errs, fmt.Errorf("bindings[%d]: capability %q is bound twice", i, b.Capability))
		}
		seen[b.Capability] = true
		if len(b.Cores) == 0 {
			errs = append(errs, fmt.Errorf("bindings[%d] (%s): needs at least one core", i, b.Capability))
		}
		for _, id := range b.Cores {
			if name, ok := strings.CutPrefix(id, RolePrefix); ok {
				if _, has := c.Roles[name]; !has {
					errs = append(errs, fmt.Errorf("bindings[%d] (%s): role %q is not declared in roles", i, b.Capability, name))
				}
				continue
			}
			if !ids[id] {
				errs = append(errs, fmt.Errorf("bindings[%d] (%s): core %q is not declared in cores", i, b.Capability, id))
			}
		}
		switch b.Mode {
		case "", ModeSingle, ModeFallback, ModeFanout:
		default:
			errs = append(errs, fmt.Errorf("bindings[%d] (%s): mode must be single, fallback or fanout (got %q)", i, b.Capability, b.Mode))
		}
		switch b.OnError {
		case "":
		case OnErrorSkip, OnErrorFail:
			if b.Mode != ModeFanout {
				errs = append(errs, fmt.Errorf("bindings[%d] (%s): on_error only applies to mode fanout", i, b.Capability))
			}
		default:
			errs = append(errs, fmt.Errorf("bindings[%d] (%s): on_error must be skip or fail (got %q)", i, b.Capability, b.OnError))
		}
		if b.TimeoutMS < 0 || b.Retries < 0 {
			errs = append(errs, fmt.Errorf("bindings[%d] (%s): timeout_ms and retries must not be negative", i, b.Capability))
		}
		if b.Mode == ModeSingle {
			if n := len(c.Expand(b)); n != 1 {
				errs = append(errs, fmt.Errorf("bindings[%d] (%s): mode single needs exactly one core (got %d)", i, b.Capability, n))
			}
		}
	}
	for i, w := range c.Wrappers {
		if w.Capability == "" {
			errs = append(errs, fmt.Errorf("wrappers[%d]: capability is required", i))
		}
		if w.Kind == "" {
			errs = append(errs, fmt.Errorf("wrappers[%d]: kind is required", i))
		}
		switch w.Placement {
		case "", PlaceOuter, PlaceInner:
		default:
			errs = append(errs, fmt.Errorf("wrappers[%d]: placement must be outer or inner (got %q)", i, w.Placement))
		}
	}
	for i, sub := range c.Subscriptions {
		if sub.Event == "" {
			errs = append(errs, fmt.Errorf("subscriptions[%d]: event is required", i))
		}
		if sub.TimeoutMS < 0 {
			errs = append(errs, fmt.Errorf("subscriptions[%d]: timeout_ms must not be negative", i))
		}
		if name, ok := strings.CutPrefix(sub.Core, RolePrefix); ok {
			if _, has := c.Roles[name]; !has {
				errs = append(errs, fmt.Errorf("subscriptions[%d]: role %q is not declared in roles", i, name))
			}
		} else if !ids[sub.Core] {
			errs = append(errs, fmt.Errorf("subscriptions[%d]: core %q is not declared in cores", i, sub.Core))
		}
	}
	if c.Resilience.Retries < 0 || c.Resilience.RetryDelayMS < 0 || c.Resilience.BreakerCooldown < 0 || c.Resilience.BreakerFailures < -1 {
		errs = append(errs, errors.New("resilience: values must not be negative (breaker_failures may be -1 = off)"))
	}
	if c.Limits.MaxDepth < 0 {
		errs = append(errs, errors.New("limits.max_depth must not be negative"))
	}
	return errors.Join(errs...)
}
