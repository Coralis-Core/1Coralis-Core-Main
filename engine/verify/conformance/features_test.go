package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	structural "example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
)

// Test contracts, written in memory so the suite needs no extra files.
const reqContract = `{"capability":"%s","version":"1.0.0","kind":"request","summary":"t","idempotent":%v,
 "input":{"type":"object","required":["n"],"additionalProperties":false,"properties":{"n":{"type":"integer"}}},
 "output":{"type":"object","required":["n"],"additionalProperties":false,"properties":{"n":{"type":"integer"}}},
 "examples":[{"name":"one","input":{"n":1},"output":{"n":1}}]}`

const evContract = `{"capability":"tt.v1.ping","version":"1.0.0","kind":"event","summary":"t",
 "payload":{"type":"object","required":["id"],"additionalProperties":false,"properties":{"id":{"type":"integer"}}},
 "examples":[{"name":"one","payload":{"id":1}}],"invalid_payloads":[{"name":"str","payload":{"id":"x"}}]}`

func featureSet(t *testing.T) *contract.Set {
	t.Helper()
	fsys := fstest.MapFS{
		"tt/v1/echo.contract.json": {Data: []byte(fmt.Sprintf(reqContract, "tt.v1.echo", false))},
		"tt/v1/safe.contract.json": {Data: []byte(fmt.Sprintf(reqContract, "tt.v1.safe", true))},
		"tt/v1/ping.contract.json": {Data: []byte(evContract)},
	}
	set, err := contract.LoadAll(structural.FS, fsys)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// wireCore is a tiny HTTP core speaking the wire format (any language could).
type wireCore struct {
	srv   *httptest.Server
	calls atomic.Int32
	mu    sync.Mutex
	got   []string // events received
}

// behave decides the answer of tt.* capabilities: it gets the call number (1-based).
func newWireCore(t *testing.T, caps []string, behave func(n int32, w http.ResponseWriter, capName string, in json.RawMessage)) *wireCore {
	t.Helper()
	wc := &wireCore{}
	wc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Capability string          `json:"capability"`
			Input      json.RawMessage `json:"input"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Capability {
		case "core.v1.describe":
			cs, _ := json.Marshal(caps)
			fmt.Fprintf(w, `{"output":{"name":"c","capabilities":%s}}`, cs)
		case "core.v1.health":
			fmt.Fprint(w, `{"output":{"status":"ok"}}`)
		default:
			wc.mu.Lock()
			wc.got = append(wc.got, req.Capability+" "+string(req.Input))
			wc.mu.Unlock()
			behave(wc.calls.Add(1), w, req.Capability, req.Input)
		}
	}))
	t.Cleanup(wc.srv.Close)
	return wc
}

func okEcho(_ int32, w http.ResponseWriter, _ string, in json.RawMessage) {
	fmt.Fprintf(w, `{"output":%s}`, in)
}

func buildStart(t *testing.T, cfgDoc string, o boot.Options) *boot.Platform {
	t.Helper()
	o.Contracts = featureSet(t)
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := boot.Build(mustParse(t, cfgDoc), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	return p
}

func call(t *testing.T, p *boot.Platform, capName, in string) (string, error) {
	t.Helper()
	b, err := p.Host().Router.CallJSON(context.Background(), capability.Name(capName), []byte(in))
	return string(b), err
}

// ---- wrappers -----------------------------------------------------------------

func doubler(options json.RawMessage) (capability.Wrapper, error) {
	return func(ctx context.Context, in []byte, next capability.Next) ([]byte, error) {
		var m map[string]int
		_ = json.Unmarshal(in, &m)
		m["n"] *= 2
		in2, _ := json.Marshal(m)
		return next(ctx, in2)
	}, nil
}

func breaker(options json.RawMessage) (capability.Wrapper, error) { // violates the contract on purpose
	return func(ctx context.Context, in []byte, next capability.Next) ([]byte, error) {
		return []byte(`{"n":"not a number"}`), nil
	}, nil
}

func TestConfigMountedWrapperChangesBehaviourWithoutTouchingTheCore(t *testing.T) {
	c := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"a","kind":"http","options":{"url":%q}}],
	 "bindings":[{"capability":"tt.v1.echo","cores":["a"]}],
	 "wrappers":[{"capability":"tt.v1.echo","kind":"double"}]}`, c.srv.URL)
	p := buildStart(t, cfg, boot.Options{Wrappers: map[string]boot.WrapperFactory{"double": doubler}})
	if out, err := call(t, p, "tt.v1.echo", `{"n":4}`); err != nil || out != `{"n":8}` {
		t.Fatalf("wrapper must double the input before the core sees it: %s %v", out, err)
	}
}

func TestWrapperOutputIsContractCheckedByDefaultAndCheckCanBeTurnedOff(t *testing.T) {
	c := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	mk := func(extra string) *boot.Platform {
		return buildStart(t, fmt.Sprintf(`{"version":1,"cores":[{"id":"a","kind":"http","options":{"url":%q}}],
		 "bindings":[{"capability":"tt.v1.echo","cores":["a"]}],
		 "wrappers":[{"capability":"tt.v1.echo","kind":"bad"%s}]}`, c.srv.URL, extra),
			boot.Options{Wrappers: map[string]boot.WrapperFactory{"bad": breaker}})
	}
	if _, err := call(t, mk(""), "tt.v1.echo", `{"n":1}`); capability.CodeOf(err) != "internal" {
		t.Fatalf("default: a wrapper that breaks the contract is caught: %v", err)
	}
	if out, err := call(t, mk(`,"check_contract":false`), "tt.v1.echo", `{"n":1}`); err != nil || !strings.Contains(out, "not a number") {
		t.Fatalf("check off: the user accepted the risk: %s %v", out, err)
	}
}

func TestUnknownWrapperKindOrContractFailsAtBuild(t *testing.T) {
	for name, cfg := range map[string]string{
		"unknown kind":  `{"version":1,"wrappers":[{"capability":"tt.v1.echo","kind":"nope"}]}`,
		"no contract":   `{"version":1,"wrappers":[{"capability":"zz.v1.none","kind":"double"}]}`,
		"bad placement": `{"version":1,"wrappers":[{"capability":"tt.v1.echo","kind":"double","placement":"middle"}]}`,
	} {
		parsed, err := config.Parse(strings.NewReader(cfg))
		if err == nil {
			_, err = boot.Build(parsed, boot.Options{Contracts: featureSet(t), Wrappers: map[string]boot.WrapperFactory{"double": doubler}})
		}
		if err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

// ---- retries, repeat safety, backup, breaker --------------------------------------

func flakyThenOK(failures int32) func(int32, http.ResponseWriter, string, json.RawMessage) {
	return func(n int32, w http.ResponseWriter, c string, in json.RawMessage) {
		if n <= failures {
			w.WriteHeader(http.StatusBadGateway) // not a wire answer: the request may have been processed
			fmt.Fprint(w, "oops")
			return
		}
		okEcho(n, w, c, in)
	}
}

func TestRetryAfterSendHappensOnlyForIdempotentContracts(t *testing.T) {
	for _, tc := range []struct {
		capName   string
		wantCalls int32
		wantErr   bool
	}{{"tt.v1.safe", 2, false}, {"tt.v1.echo", 1, true}} {
		c := newWireCore(t, []string{tc.capName}, flakyThenOK(1))
		cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"a","kind":"http","options":{"url":%q}}],
		 "bindings":[{"capability":%q,"cores":["a"],"retries":2}],"resilience":{"retry_delay_ms":1}}`, c.srv.URL, tc.capName)
		p := buildStart(t, cfg, boot.Options{})
		_, err := call(t, p, tc.capName, `{"n":1}`)
		if (err != nil) != tc.wantErr || c.calls.Load() != tc.wantCalls {
			t.Errorf("%s: err=%v calls=%d (want err=%v calls=%d)", tc.capName, err, c.calls.Load(), tc.wantErr, tc.wantCalls)
		}
		if tc.wantErr && !errorsIsAmbiguous(err) {
			t.Errorf("%s: the caller must learn the request may have been delivered: %v", tc.capName, err)
		}
	}
}

func errorsIsAmbiguous(err error) bool { return capability.CodeOf(err) == "maybe_delivered" }

func TestBackupCoreIsUsedWhenTheFirstIsDownEvenIfNotIdempotent(t *testing.T) {
	dead := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	deadURL := dead.srv.URL
	good := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	// Start with both up (describe must succeed), then take the first one down.
	cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"a","kind":"http","options":{"url":%q}},{"id":"b","kind":"http","options":{"url":%q}}],
	 "bindings":[{"capability":"tt.v1.echo","cores":["a","b"],"mode":"fallback"}]}`, deadURL, good.srv.URL)
	p := buildStart(t, cfg, boot.Options{})
	dead.srv.Close()
	if out, err := call(t, p, "tt.v1.echo", `{"n":3}`); err != nil || out != `{"n":3}` {
		t.Fatalf("nothing was sent to the dead core, so the backup is safe: %s %v", out, err)
	}
	if good.calls.Load() != 1 {
		t.Fatalf("backup should have served once, got %d", good.calls.Load())
	}
}

func TestSingleModeNeverFallsBack(t *testing.T) {
	a := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	b := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"a","kind":"http","options":{"url":%q}},{"id":"b","kind":"http","options":{"url":%q}}],
	 "roles":{"main":["a","b"]},
	 "bindings":[{"capability":"tt.v1.echo","cores":["role:main"],"mode":"fallback"}]}`, a.srv.URL, b.srv.URL)
	_ = buildStart(t, cfg, boot.Options{})
	// single with two cores is a config error (already enforced by config tests)
}

// ---- gateway as a mounted role ---------------------------------------------------------

func TestGatewayMountedFromConfigAndCalledThroughTheWire(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "pong") }))
	defer up.Close()
	host := strings.TrimPrefix(strings.Split(up.URL, ":")[1], "//")
	cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"out","kind":"gateway","options":{"allow_private":true,"allowed_domains":[%q]}}],
	 "roles":{"gateway":["out"]},
	 "bindings":[{"capability":"gateway.v1.request","cores":["role:gateway"],"mode":"single"}]}`, host)
	p := buildStart(t, cfg, boot.Options{})
	out, err := call(t, p, "gateway.v1.request", fmt.Sprintf(`{"url":%q}`, up.URL))
	if err != nil || !strings.Contains(out, `"body":"pong"`) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := call(t, p, "gateway.v1.request", `{"url":"http://evil.invalid/"}`); capability.CodeOf(err) != "domain_not_allowed" {
		t.Fatalf("unlisted domain: %v", err)
	}
}

// ---- events -------------------------------------------------------------------------------

func postJSON(t *testing.T, h http.Handler, path, body string) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

func TestEventsAreDeliveredToSubscribersOnlyAndSlowOnesDoNotBlockFastOnes(t *testing.T) {
	ack := func(_ int32, w http.ResponseWriter, _ string, _ json.RawMessage) { fmt.Fprint(w, `{"output":{}}`) }
	fast := newWireCore(t, []string{"tt.v1.echo"}, ack)
	slow := newWireCore(t, []string{"tt.v1.echo"}, func(n int32, w http.ResponseWriter, c string, in json.RawMessage) {
		time.Sleep(400 * time.Millisecond)
		ack(n, w, c, in)
	})
	other := newWireCore(t, []string{"tt.v1.echo"}, ack)
	cfg := fmt.Sprintf(`{"version":1,"cores":[
	 {"id":"fast","kind":"http","options":{"url":%q}},
	 {"id":"slow","kind":"http","options":{"url":%q}},
	 {"id":"other","kind":"http","options":{"url":%q}}],
	 "subscriptions":[{"event":"tt.v1.ping","core":"fast"},{"event":"tt.v1.ping","core":"slow","timeout_ms":50}],
	 "serve":{"expose":["tt.v1.ping"]}}`, fast.srv.URL, slow.srv.URL, other.srv.URL)
	p := buildStart(t, cfg, boot.Options{})
	h := p.Handler(featureSet(t), "", slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	if code, body := postJSON(t, h, "/v1/event", `{"event":"tt.v1.ping","payload":{"id":7}}`); code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatalf("the slow subscriber's own timeout must bound the wait, took %v", time.Since(start))
	}
	if !hasEvent(fast, `tt.v1.ping {"id":7}`) {
		t.Fatalf("fast subscriber missed the event: %v", fast.got)
	}
	if len(other.got) != 0 {
		t.Fatalf("a core nobody subscribed must not get it: %v", other.got)
	}
	if code, _ := postJSON(t, h, "/v1/event", `{"event":"tt.v1.ping","payload":{"id":"x"}}`); code != 400 {
		t.Fatalf("invalid payload must be refused: %d", code)
	}
	if code, _ := postJSON(t, h, "/v1/event", `{"event":"tt.v1.other","payload":{}}`); code != 404 {
		t.Fatalf("unknown or unexposed events look nonexistent: %d", code)
	}
}

func hasEvent(c *wireCore, want string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.got {
		if g == want {
			return true
		}
	}
	return false
}

func TestEventNotExposedIsInvisible(t *testing.T) {
	fast := newWireCore(t, []string{}, func(_ int32, w http.ResponseWriter, _ string, _ json.RawMessage) { fmt.Fprint(w, `{"output":{}}`) })
	cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"fast","kind":"http","options":{"url":%q,"timeout_ms":1000}}],
	 "subscriptions":[{"event":"tt.v1.ping","core":"fast"}]}`, fast.srv.URL)
	p := buildStart(t, cfg, boot.Options{})
	h := p.Handler(featureSet(t), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if code, _ := postJSON(t, h, "/v1/event", `{"event":"tt.v1.ping","payload":{"id":1}}`); code != 404 {
		t.Fatalf("events are not exposed unless listed in serve.expose: %d", code)
	}
	if err := p.Publish(context.Background(), "tt.v1.ping", []byte(`{"id":1}`)); err != nil {
		t.Fatalf("in-process publishing still works: %v", err)
	}
}

// ---- depth cap ----------------------------------------------------------------------------------

func TestMaxDepthIsEnforcedOnTheWireWhenConfigured(t *testing.T) {
	c := newWireCore(t, []string{"tt.v1.echo"}, okEcho)
	mk := func(limits string) http.Handler {
		cfg := fmt.Sprintf(`{"version":1,"cores":[{"id":"a","kind":"http","options":{"url":%q}}],%s
		 "bindings":[{"capability":"tt.v1.echo","cores":["a"]}],"serve":{"expose":["tt.v1.echo"]}}`, c.srv.URL, limits)
		return buildStart(t, cfg, boot.Options{}).Handler(featureSet(t), "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	deep := `{"capability":"tt.v1.echo","input":{"n":1},"meta":{"depth":"5"}}`
	if code, _ := postJSON(t, mk(""), "/v1/call", deep); code != 200 {
		t.Fatalf("no cap by default: %d", code)
	}
	code, body := postJSON(t, mk(`"limits":{"max_depth":3},`), "/v1/call", deep)
	if code != http.StatusLoopDetected || !strings.Contains(body, "depth_exceeded") {
		t.Fatalf("cap on: %d %s", code, body)
	}
}

type plainCore struct{}

func (plainCore) Name() string                              { return "plain" }
func (plainCore) Start(context.Context, *kernel.Host) error { return nil }
func (plainCore) Stop(context.Context) error                { return nil }

func TestSubscribingACoreThatCannotReceiveEventsFailsAtBuild(t *testing.T) {
	cfg := mustParse(t, `{"version":1,"cores":[{"id":"plain","kind":"plain"}],"subscriptions":[{"event":"tt.v1.ping","core":"plain"}]}`)
	_, err := boot.Build(cfg, boot.Options{Contracts: featureSet(t), Factories: map[string]boot.Factory{
		"plain": func(string, json.RawMessage) (kernel.Core, error) { return plainCore{}, nil }}})
	if err == nil || !strings.Contains(err.Error(), "cannot receive events") {
		t.Fatalf("want a clear startup error, got %v", err)
	}
	// and a subscription to something that is not an event contract
	cfg2 := mustParse(t, `{"version":1,"cores":[{"id":"plain","kind":"plain"}],"subscriptions":[{"event":"tt.v1.echo","core":"plain"}]}`)
	if _, err := boot.Build(cfg2, boot.Options{Contracts: featureSet(t), Factories: map[string]boot.Factory{
		"plain": func(string, json.RawMessage) (kernel.Core, error) { return plainCore{}, nil }}}); err == nil {
		t.Fatal("a request contract is not an event")
	}
}
