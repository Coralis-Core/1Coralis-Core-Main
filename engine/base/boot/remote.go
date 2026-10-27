package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/base/transports/httpjson"
)

type httpOptions struct {
	URL       string `json:"url"`
	TokenEnv  string `json:"token_env"`
	TimeoutMS int    `json:"timeout_ms"`
}

// remoteCore mounts any HTTP+JSON core (any language, or another platform).
type remoteCore struct {
	id   string
	caps []string
	rm   httpjson.Remote
	set  *contract.Set
}

func newRemote(c config.Core, o Options) (*remoteCore, error) {
	var opt httpOptions
	dec := json.NewDecoder(bytes.NewReader(c.Options))
	dec.DisallowUnknownFields()
	if len(c.Options) == 0 {
		return nil, errors.New(`options.url is required for kind "http"`)
	}
	if err := dec.Decode(&opt); err != nil {
		return nil, fmt.Errorf("options: %w", err)
	}
	if opt.URL == "" {
		return nil, errors.New("options.url is required")
	}
	if opt.TimeoutMS < 0 {
		return nil, errors.New("options.timeout_ms must not be negative")
	}
	rm := httpjson.Remote{BaseURL: opt.URL}
	if opt.TokenEnv != "" {
		rm.Token = o.Getenv(opt.TokenEnv)
		if rm.Token == "" {
			return nil, fmt.Errorf("options.token_env %q is set but the environment variable is empty (refusing to mount without credentials)", opt.TokenEnv)
		}
	}
	switch {
	case o.HTTPClient != nil:
		rm.Client = o.HTTPClient
	case opt.TimeoutMS > 0:
		rm.Client = &http.Client{Timeout: time.Duration(opt.TimeoutMS) * time.Millisecond}
	}
	return &remoteCore{id: c.ID, caps: c.Capabilities, rm: rm, set: o.Contracts}, nil
}

func (r *remoteCore) Name() string               { return r.id }
func (r *remoteCore) Stop(context.Context) error { return nil }

func (r *remoteCore) call(ctx context.Context, capName string) ([]byte, error) {
	c, _ := r.set.Get(capName)
	out, code, err := contract.Guard(c, r.rm.Provider(c))(ctx, []byte(`{}`))
	switch {
	case err != nil:
		return nil, err
	case code != "":
		return nil, fmt.Errorf("%s: %s", capName, code)
	}
	return out, nil
}

func (r *remoteCore) described(ctx context.Context) ([]string, error) {
	out, err := r.call(ctx, CapDescribe)
	if err != nil {
		return nil, err
	}
	var d struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, err
	}
	return d.Capabilities, nil
}

// Start verifies the config against reality, then mounts the capabilities.
func (r *remoteCore) Start(ctx context.Context, h *kernel.Host) error {
	described, derr := r.described(ctx)
	caps := r.caps
	switch {
	case derr == nil && len(caps) == 0:
		for _, name := range described {
			if c, ok := r.set.Get(name); ok && c.Kind == contract.KindRequest {
				caps = append(caps, name)
			} else {
				h.Log.Warn("boot: core offers a capability that has no contract here; not mounted", "core", r.id, "capability", name)
			}
		}
	case derr == nil:
		have := map[string]bool{}
		for _, n := range described {
			have[n] = true
		}
		var missing []string
		for _, n := range caps {
			if !have[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("config claims capabilities the core does not provide: %s", strings.Join(missing, ", "))
		}
	case len(caps) == 0:
		return fmt.Errorf("cannot discover capabilities (%v); list them in the config", derr)
	default:
		h.Log.Warn("boot: core could not be described; trusting the config", "core", r.id, "err", derr)
	}
	return r.rm.Bind(h.Router, r.id, r.set, caps...)
}

// Health implements Healther through core.v1.health.
func (r *remoteCore) Health(ctx context.Context) (string, string) {
	out, err := r.call(ctx, CapHealth)
	if err != nil {
		return "down", err.Error()
	}
	var h struct{ Status, Detail string }
	if json.Unmarshal(out, &struct {
		Status *string `json:"status"`
		Detail *string `json:"detail"`
	}{&h.Status, &h.Detail}) != nil {
		return "down", "unreadable health response"
	}
	return h.Status, h.Detail
}

// Deliver implements EventReceiver: the event goes to the remote core over the
// wire (POST /v1/call with the event name as the capability and the payload as
// the input); the core answers {"output":{}}. Best effort: an error is only
// logged by the platform.
func (r *remoteCore) Deliver(ctx context.Context, event string, payload []byte) error {
	c, ok := r.set.Get(event)
	if !ok || c.Kind != contract.KindEvent {
		return fmt.Errorf("no event contract for %q", event)
	}
	_, code, err := r.rm.Provider(c)(ctx, payload)
	switch {
	case err != nil:
		return err
	case code != "":
		return fmt.Errorf("%s: %s", event, code)
	}
	return nil
}
