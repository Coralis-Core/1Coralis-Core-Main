// Package gateway is the platform's outbound boundary: a core that provides
// gateway.v1.request. It is a ROLE, not a locked core: any core that provides
// the same capability can play it (swap it in config). Cores themselves are
// kept off the internet (network isolation in deployment) and reach it only
// through the platform.
//
// Safe defaults (strict mode): only listed domains, no redirects, private and
// internal addresses blocked (checked at connect time, so DNS tricks do not
// help), size and time limits, keys injected from environment variables (the
// config only holds the variable NAMES), and every outbound call logged
// WITHOUT its query string or any secret.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/kernel"
)

// Capability is the contract this core provides.
const Capability = "gateway.v1.request"

// Modes.
const (
	ModeStrict = "strict" // default: allow-list enforced
	ModeOpen   = "open"   // any domain (everything else in force); you accept the risk
)

// Defaults.
const (
	DefaultMaxBytes = 1 << 20
	DefaultTimeout  = 10 * time.Second
)

// Key says: calls to Domain get header Header set to Prefix + $Env.
type Key struct {
	Domain string `json:"domain"`
	Header string `json:"header"`
	Env    string `json:"env"`
	Prefix string `json:"prefix,omitempty"`
}

// Options is the config of a gateway core (config: cores[].options).
type Options struct {
	Mode            string   `json:"mode,omitempty"`             // strict (default) | open
	AllowedDomains  []string `json:"allowed_domains,omitempty"`  // "api.x.com" or "*.x.com"
	FollowRedirects bool     `json:"follow_redirects,omitempty"` // default false
	AllowPrivate    bool     `json:"allow_private,omitempty"`    // default false: private/internal addresses blocked
	MaxBytes        int64    `json:"max_response_bytes,omitempty"`
	TimeoutMS       int      `json:"timeout_ms,omitempty"`
	Keys            []Key    `json:"keys,omitempty"`
}

type request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// Core is the gateway core.
type Core struct {
	id     string
	opt    Options
	getenv func(string) string
	client *http.Client
	log    *slog.Logger
}

// New builds a gateway core from its config options.
func New(id string, raw json.RawMessage, getenv func(string) string, log *slog.Logger) (*Core, error) {
	var o Options
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&o); err != nil {
			return nil, fmt.Errorf("gateway options: %w", err)
		}
	}
	switch o.Mode {
	case "":
		o.Mode = ModeStrict
	case ModeStrict, ModeOpen:
	default:
		return nil, fmt.Errorf("gateway options: mode must be strict or open (got %q)", o.Mode)
	}
	if o.MaxBytes < 0 || o.TimeoutMS < 0 {
		return nil, errors.New("gateway options: max_response_bytes and timeout_ms must not be negative")
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	timeout := DefaultTimeout
	if o.TimeoutMS > 0 {
		timeout = time.Duration(o.TimeoutMS) * time.Millisecond
	}
	for _, k := range o.Keys {
		if k.Domain == "" || k.Header == "" || k.Env == "" {
			return nil, errors.New("gateway options: every key needs domain, header and env")
		}
		if getenv(k.Env) == "" {
			return nil, fmt.Errorf("gateway options: key for %q: environment variable %q is empty (refusing to start without credentials)", k.Domain, k.Env)
		}
	}
	if log == nil {
		log = slog.Default()
	}
	g := &Core{id: id, opt: o, getenv: getenv, log: log}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !o.AllowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || isInternal(ip) {
				return errBlocked
			}
			return nil
		}
	}
	g.client = &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil, DisableKeepAlives: true},
	}
	if !o.FollowRedirects {
		g.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return g, nil
}

var errBlocked = errors.New("blocked address")

// isInternal reports addresses a gateway must never connect to by default:
// loopback, private, link-local (cloud metadata), unspecified, multicast and
// the carrier-grade NAT range.
func isInternal(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return cgnat.Contains(ip)
}

func (g *Core) Name() string { return g.id }

func (g *Core) Stop(context.Context) error { return nil }

func (g *Core) Start(_ context.Context, h *kernel.Host) error {
	return h.Router.ProvideJSON(g.id, capability.Name(Capability), g.handle)
}

func domainMatches(pattern, host string) bool {
	pattern, host = strings.ToLower(pattern), strings.ToLower(host)
	if rest, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+rest)
	}
	return pattern == host
}

func (g *Core) allowed(host string) bool {
	if g.opt.Mode == ModeOpen {
		return true
	}
	for _, d := range g.opt.AllowedDomains {
		if domainMatches(d, host) {
			return true
		}
	}
	return false
}

func (g *Core) handle(ctx context.Context, in []byte) ([]byte, error) {
	var rq request
	if err := json.Unmarshal(in, &rq); err != nil {
		return nil, fmt.Errorf("%w: %v", capability.ErrInvalidInput, err)
	}
	u, err := url.Parse(rq.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, capability.ErrInvalidInput
	}
	host := u.Hostname()
	trace := capability.MetaFrom(ctx)[capability.MetaTrace]
	start := time.Now()
	logCall := func(outcome string, status int) {
		// host and path only: the query string may carry secrets
		g.log.Info("gateway: outbound call", "trace_id", trace, "method", orGet(rq.Method), "host", host, "path", u.Path,
			"outcome", outcome, "status", status, "ms", time.Since(start).Milliseconds())
	}
	if !g.allowed(host) {
		logCall("domain_not_allowed", 0)
		return nil, capability.NewError("domain_not_allowed", "")
	}
	var body io.Reader
	if rq.Body != "" {
		if int64(len(rq.Body)) > g.opt.MaxBytes {
			logCall("too_large", 0)
			return nil, capability.NewError("too_large", "request body")
		}
		body = strings.NewReader(rq.Body)
	}
	req, err := http.NewRequestWithContext(ctx, orGet(rq.Method), u.String(), body)
	if err != nil {
		return nil, capability.ErrInvalidInput
	}
	for k, v := range rq.Headers {
		req.Header.Set(k, v)
	}
	for _, k := range g.opt.Keys {
		if domainMatches(k.Domain, host) {
			req.Header.Set(k.Header, k.Prefix+g.getenv(k.Env)) // secret never logged, never returned
		}
	}
	resp, err := g.client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, errBlocked):
			logCall("blocked_address", 0)
			return nil, capability.NewError("blocked_address", "")
		case isDial(err):
			logCall("unreachable", 0)
			return nil, fmt.Errorf("%w: %s unreachable", capability.ErrUnavailable, host) // nothing was sent
		default:
			logCall("upstream_error", 0)
			return nil, capability.NewError("upstream_error", "")
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, g.opt.MaxBytes+1))
	if err != nil {
		logCall("upstream_error", resp.StatusCode)
		return nil, capability.NewError("upstream_error", "")
	}
	if int64(len(raw)) > g.opt.MaxBytes {
		logCall("too_large", resp.StatusCode)
		return nil, capability.NewError("too_large", "response")
	}
	hdr := map[string]string{}
	for k := range resp.Header {
		if strings.EqualFold(k, "Set-Cookie") {
			continue // never hand cookies to cores
		}
		hdr[strings.ToLower(k)] = resp.Header.Get(k)
	}
	logCall("ok", resp.StatusCode)
	return json.Marshal(response{Status: resp.StatusCode, Headers: hdr, Body: string(raw)})
}

func orGet(m string) string {
	if m == "" {
		return http.MethodGet
	}
	return m
}

func isDial(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}
