// Package boot turns a config into a running platform: it mounts what the
// config declares (kind "http" is built in; other kinds come from Factories
// the host program registers), checks that cores really provide what the
// config claims (core.v1.describe), exposes the platform itself as a core
// (so platforms can be nested), and reports health (core.v1.health).
package boot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/gateway"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/base/transports/httpjson"
)

const (
	CapDescribe = "core.v1.describe"
	CapHealth   = "core.v1.health"
)

// Factory builds an in-process core of a custom kind from its config options.
type Factory func(id string, options json.RawMessage) (kernel.Core, error)

// RemoteFactory builds a core of a custom kind that lives OUTSIDE this process
// (another transport, e.g. gRPC). Unlike Factory it receives the whole core
// config entry (including the claimed capabilities) and the boot options.
type RemoteFactory func(c config.Core, o Options) (kernel.Core, error)

// WrapperFactory builds the around-decorator of a wrapper kind from its
// config options (wrappers[].kind). The host program registers them.
type WrapperFactory func(options json.RawMessage) (capability.Wrapper, error)

// EventReceiver is implemented by a core that can be handed an event
// (best effort) when the config subscribes it.
type EventReceiver interface {
	Deliver(ctx context.Context, event string, payload []byte) error
}

type Options struct {
	Contracts *contract.Set // must include the structural core.v1.* contracts
	Factories map[string]Factory
	// RemoteFactories register extra transport kinds (e.g. "grpc" from contrib/grpc).
	RemoteFactories map[string]RemoteFactory
	Wrappers        map[string]WrapperFactory
	Getenv          func(string) string // default os.Getenv
	Log             *slog.Logger
	HTTPClient      *http.Client // optional (tests); default per-core client from timeout_ms
}

// Healther is optional: a core that can report its own health.
type Healther interface {
	Health(ctx context.Context) (status, detail string)
}

type CoreHealth struct {
	ID     string `json:"id"`
	Status string `json:"status"` // ok | degraded | down
	Detail string `json:"detail,omitempty"`
}

type subTarget struct {
	id      string
	rx      EventReceiver
	timeout time.Duration
}

type Platform struct {
	*kernel.Platform
	cores     []kernel.Core
	expose    map[string]bool
	exposeAll bool
	set       *contract.Set
	maxDepth  int
	log       *slog.Logger
	subs      map[string][]subTarget // event -> subscribers (from config)
}

// DefaultSubscriberTimeout bounds one event delivery when the config sets none.
const DefaultSubscriberTimeout = 2 * time.Second

// Publish announces an event: it is validated against its event contract, put
// on the bus for in-process cores, and handed to the cores the config
// subscribes. Delivery is best effort with no ordering guarantee: a subscriber
// that is down or too slow misses the event (logged, never blocks the others).
func (p *Platform) Publish(ctx context.Context, event string, payload []byte) error {
	c, ok := p.set.Get(event)
	if !ok || c.Kind != contract.KindEvent {
		return capability.NewError("unknown_event", event)
	}
	if err := c.ValidatePayload(payload); err != nil {
		return fmt.Errorf("%w: %v", capability.ErrInvalidInput, err)
	}
	p.Host().Bus.Publish(ctx, event, json.RawMessage(payload))
	return nil
}

// deliver hands raw to every subscribed core at once, each with its own timeout.
func (p *Platform) deliver(ctx context.Context, event string, raw []byte) {
	var wg sync.WaitGroup
	for _, t := range p.subs[event] {
		wg.Add(1)
		go func(t subTarget) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, t.timeout)
			defer cancel()
			if err := t.rx.Deliver(cctx, event, raw); err != nil {
				p.log.Warn("event not delivered (no delivery guarantee)", "event", event, "core", t.id, "err", err)
			}
		}(t)
	}
	wg.Wait()
}

// Exposed says whether a capability may be seen by callers of this platform's
// endpoint. The structural core.v1.* capabilities are always visible.
func (p *Platform) Exposed(name string) bool {
	return p.exposeAll || p.expose[name] || name == CapDescribe || name == CapHealth
}

// Start starts the cores, then checks that every exposed capability exists
// (a typo in serve.expose must fail at startup, not silently expose nothing).
func (p *Platform) Start(ctx context.Context) error {
	if err := p.Platform.Start(ctx); err != nil {
		return err
	}
	have := map[string]bool{}
	for _, n := range p.Host().Router.Capabilities() {
		have[string(n)] = true
	}
	var unknown []string
	for n := range p.expose {
		if ec, isEvent := p.set.Get(n); !have[n] && !(isEvent && ec.Kind == contract.KindEvent) {
			unknown = append(unknown, n)
		}
	}
	for ev := range p.subs {
		ev := ev
		p.Host().Bus.Tap(ev, func(ctx context.Context, raw []byte) { p.deliver(ctx, ev, raw) })
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		_ = p.Platform.Stop(ctx)
		return fmt.Errorf("serve.expose names capabilities nothing provides: %s", strings.Join(unknown, ", "))
	}
	return nil
}

// Build mounts everything the config declares. It does not start anything.
func Build(cfg config.Config, o Options) (*Platform, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg, _ = cfg.WithDefaults() // every key has a default; see config/defaults.go
	if o.Contracts == nil {
		return nil, errors.New("boot: Options.Contracts is required")
	}
	for _, name := range []string{CapDescribe, CapHealth} {
		if c, ok := o.Contracts.Get(name); !ok || c.Kind != contract.KindRequest {
			return nil, fmt.Errorf("boot: structural contract %q is missing from the contract set", name)
		}
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	kp := kernel.New(cfg, o.Log)
	kp.RequireContracts(o.Contracts)
	bp := &Platform{Platform: kp, expose: map[string]bool{}, set: o.Contracts, maxDepth: cfg.Limits.MaxDepth, log: o.Log, subs: map[string][]subTarget{}}
	router := kp.Host().Router
	router.SetResilience(capability.Resilience{
		Retries:         cfg.Resilience.Retries,
		RetryDelay:      time.Duration(cfg.Resilience.RetryDelayMS) * time.Millisecond,
		BreakerFailures: cfg.Resilience.BreakerFailures,
		BreakerCooldown: time.Duration(cfg.Resilience.BreakerCooldown) * time.Millisecond,
	})
	router.SetFailureLogger(func(e capability.FailureEvent) {
		o.Log.Warn("core call failed", "capability", string(e.Capability), "core", e.Provider, "attempt", e.Attempt, "err", e.Err)
	})
	for _, name := range o.Contracts.Names() {
		if c, _ := o.Contracts.Get(name); c.Kind == contract.KindRequest && c.Idempotent {
			router.SetIdempotent(capability.Name(name), true)
		}
	}
	for _, n := range cfg.Serve.Expose {
		if n == "*" {
			bp.exposeAll = true
		} else {
			bp.expose[n] = true
		}
	}

	for _, c := range cfg.Cores {
		var core kernel.Core
		var err error
		if c.Kind == "http" {
			core, err = newRemote(c, o)
		} else if c.Kind == "gateway" {
			core, err = gateway.New(c.ID, c.Options, o.Getenv, o.Log)
		} else if rf, ok := o.RemoteFactories[c.Kind]; ok {
			core, err = rf(c, o)
			if err == nil && core.Name() != c.ID {
				err = fmt.Errorf("remote factory %q returned a core named %q, config says %q", c.Kind, core.Name(), c.ID)
			}
		} else if f, ok := o.Factories[c.Kind]; ok {
			core, err = f(c.ID, c.Options)
			if err == nil && core.Name() != c.ID {
				err = fmt.Errorf("factory %q returned a core named %q, config says %q", c.Kind, core.Name(), c.ID)
			}
		} else {
			err = fmt.Errorf("unknown kind %q (register a Factory for it, or use \"http\")", c.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("core %q: %w", c.ID, err)
		}
		if err := kp.AddCore(core); err != nil {
			return nil, err
		}
		bp.cores = append(bp.cores, core)
	}
	if err := bp.mountWrappers(cfg, o); err != nil {
		return nil, err
	}
	if err := bp.mountSubscriptions(cfg); err != nil {
		return nil, err
	}
	if err := kp.AddCore(&selfCore{owner: bp}); err != nil {
		return nil, err
	}
	return bp, nil
}

// Health reports every mounted core that can report health.
func (p *Platform) Health(ctx context.Context) []CoreHealth {
	var out []CoreHealth
	for _, c := range p.cores {
		if h, ok := c.(Healther); ok {
			st, d := h.Health(ctx)
			out = append(out, CoreHealth{ID: c.Name(), Status: st, Detail: d})
		}
	}
	return out
}

// Summary folds per-core health into one status.
func Summary(hs []CoreHealth) (status, detail string) {
	var bad []string
	down := 0
	for _, h := range hs {
		if h.Status != "ok" {
			bad = append(bad, h.ID+": "+h.Status)
		}
		if h.Status == "down" {
			down++
		}
	}
	switch {
	case len(bad) == 0:
		return "ok", ""
	case down == len(hs):
		return "down", strings.Join(bad, "; ")
	default:
		return "degraded", strings.Join(bad, "; ")
	}
}

// Handler exposes the platform: POST /v1/call (only the exposed capabilities),
// GET /livez (open, status only) and GET /healthz (per-core detail, so it needs
// the token whenever one is configured).
func (p *Platform) Handler(set *contract.Set, token string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(httpjson.Path, httpjson.NewHandler(set, p.Host().Router, httpjson.Options{Token: token, Log: log, Allow: p.Exposed, MaxDepth: p.maxDepth}))
	mux.HandleFunc("/v1/event", func(w http.ResponseWriter, r *http.Request) {
		p.serveEvent(w, r, token)
	})
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		st, _ := Summary(p.Health(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		if st == "down" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": st})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(r, token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		hs := p.Health(r.Context())
		st, d := Summary(hs)
		w.Header().Set("Content-Type", "application/json")
		if st == "down" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": st, "detail": d, "cores": hs})
	})
	return mux
}

// selfCore makes the platform itself answer core.v1.describe / core.v1.health,
// which is what lets a platform be mounted as a core of another platform.
type selfCore struct{ owner *Platform }

func (*selfCore) Name() string               { return "platform" }
func (*selfCore) Stop(context.Context) error { return nil }

func (s *selfCore) Start(_ context.Context, h *kernel.Host) error {
	if err := h.Router.ProvideJSON("platform", capability.Name(CapDescribe), func(context.Context, []byte) ([]byte, error) {
		var caps []string
		for _, n := range h.Router.Capabilities() {
			if !strings.HasPrefix(string(n), "core.v1.") && s.owner.Exposed(string(n)) {
				caps = append(caps, string(n)) // never reveal what is not exposed
			}
		}
		sort.Strings(caps)
		if caps == nil {
			caps = []string{}
		}
		return json.Marshal(map[string]any{"name": "platform", "version": "1", "capabilities": caps, "publishes": []string{}, "subscribes": []string{}})
	}); err != nil {
		return err
	}
	return h.Router.ProvideJSON("platform", capability.Name(CapHealth), func(ctx context.Context, _ []byte) ([]byte, error) {
		st, _ := Summary(s.owner.Health(ctx)) // status only: detail would name the cores behind us
		return json.Marshal(map[string]any{"status": st})
	})
}

// serveEvent: POST /v1/event {"event":..., "payload":..., "meta":{...}}.
// Only events listed in serve.expose are accepted (others look nonexistent).
// The answer is 202: the platform has accepted the announcement; delivery to
// subscribers is best effort.
func (p *Platform) serveEvent(w http.ResponseWriter, r *http.Request, token string) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, code string) { reply(status, map[string]any{"error": map[string]string{"code": code}}) }
	if r.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, "invalid_input")
		return
	}
	if !tokenOK(r, token) {
		fail(http.StatusUnauthorized, "unauthorized")
		return
	}
	var in struct {
		Event   string            `json:"event"`
		Payload json.RawMessage   `json:"payload"`
		Meta    map[string]string `json:"meta"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || len(in.Payload) == 0 {
		fail(http.StatusBadRequest, "invalid_input")
		return
	}
	if !p.Exposed(in.Event) {
		fail(http.StatusNotFound, "unknown_capability")
		return
	}
	meta, code := httpjson.PrepareMeta(in.Meta, p.maxDepth)
	if code != "" {
		fail(http.StatusLoopDetected, code)
		return
	}
	switch err := p.Publish(capability.WithMeta(r.Context(), meta), in.Event, in.Payload); {
	case err == nil:
		reply(http.StatusAccepted, map[string]bool{"accepted": true})
	case capability.CodeOf(err) == "unknown_event":
		fail(http.StatusNotFound, "unknown_capability")
	default:
		fail(http.StatusBadRequest, "invalid_input")
	}
}

// tokenOK checks the bearer token in constant time (an empty configured token means no auth).
func tokenOK(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
