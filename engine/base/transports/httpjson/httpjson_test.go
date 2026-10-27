package httpjson

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/contract"
)

const metaContract = `{
  "capability": "echo.v1.meta", "version": "1.0.0", "kind": "request", "summary": "returns the trace_id it received in meta",
  "input": {"type": "object", "additionalProperties": false},
  "output": {"type": "object", "required": ["trace"], "additionalProperties": false, "properties": {"trace": {"type": "string"}}},
  "examples": [{"name": "ok", "input": {}, "output": {"trace": "t"}, "match": "schema"}]
}`

func setup(t *testing.T) (*contract.Set, *httptest.Server) {
	t.Helper()
	set, err := contract.Load(fstest.MapFS{"echo/v1/meta.contract.json": {Data: []byte(metaContract)}})
	if err != nil {
		t.Fatal(err)
	}
	r := capability.NewRouter()
	err = r.ProvideJSON("p", "echo.v1.meta", func(ctx context.Context, _ []byte) ([]byte, error) {
		return json.Marshal(map[string]string{"trace": capability.MetaFrom(ctx)["trace_id"]})
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(set, r, Options{}))
	t.Cleanup(srv.Close)
	return set, srv
}

func post(t *testing.T, url, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(url+Path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestMetaReachesTheHandlerContext(t *testing.T) {
	_, srv := setup(t)
	code, body := post(t, srv.URL, `{"capability":"echo.v1.meta","input":{},"meta":{"trace_id":"abc"}}`)
	if code != 200 || !strings.Contains(body, `"trace":"abc"`) {
		t.Fatalf("meta did not arrive: %d %s", code, body)
	}
	// no meta is fine: the platform mints a trace id itself (reserved key)
	code, body = post(t, srv.URL, `{"capability":"echo.v1.meta","input":{}}`)
	if code != 200 || strings.Contains(body, `"trace":""`) || !strings.Contains(body, `"trace":"`) {
		t.Fatalf("no meta: the platform must mint a trace id: %d %s", code, body)
	}
}

func TestMetaWithNonStringValueOrUnknownEnvelopeFieldIsRejected(t *testing.T) {
	_, srv := setup(t)
	for name, body := range map[string]string{
		"number value":  `{"capability":"echo.v1.meta","input":{},"meta":{"depth":3}}`,
		"nested value":  `{"capability":"echo.v1.meta","input":{},"meta":{"k":{"a":1}}}`,
		"meta not obj":  `{"capability":"echo.v1.meta","input":{},"meta":"x"}`,
		"unknown field": `{"capability":"echo.v1.meta","input":{},"extra":1}`,
	} {
		if code, out := post(t, srv.URL, body); code != http.StatusBadRequest || !strings.Contains(out, "invalid_input") {
			t.Errorf("%s: want 400 invalid_input, got %d %s", name, code, out)
		}
	}
}

func TestMetaTravelsThroughTheClientToo(t *testing.T) {
	set, srv := setup(t)
	caller := capability.NewRouter()
	if err := (Remote{BaseURL: srv.URL}).Bind(caller, "remote", set, "echo.v1.meta"); err != nil {
		t.Fatal(err)
	}
	ctx := capability.WithMeta(context.Background(), capability.Meta{"trace_id": "via-client"})
	out, err := caller.CallJSON(ctx, "echo.v1.meta", []byte(`{}`))
	if err != nil || !strings.Contains(string(out), `"trace":"via-client"`) {
		t.Fatalf("meta lost between client and server: %s %v", out, err)
	}
}

// ---- fan-out over the wire ---------------------------------------------------

const fanContract = `{
  "capability": "echo.v1.fan", "version": "1.0.0", "kind": "request", "summary": "fan-out test",
  "input": {"type": "object", "additionalProperties": false},
  "output": {"type": "object", "required": ["trace"], "additionalProperties": false, "properties": {"trace": {"type": "string"}}},
  "errors": ["not_found"],
  "examples": [{"name": "ok", "input": {}, "output": {"trace": "t"}, "match": "schema"}]
}`

func fanSetup(t *testing.T, pol capability.Policy, providers map[string]capability.JSONHandler, order []string) *httptest.Server {
	t.Helper()
	set, err := contract.Load(fstest.MapFS{"echo/v1/fan.contract.json": {Data: []byte(fanContract)}})
	if err != nil {
		t.Fatal(err)
	}
	r := capability.NewRouter()
	for _, id := range order {
		if err := r.ProvideJSON(id, "echo.v1.fan", providers[id]); err != nil {
			t.Fatal(err)
		}
	}
	r.SetPolicy("echo.v1.fan", pol)
	srv := httptest.NewServer(NewHandler(set, r, Options{}))
	t.Cleanup(srv.Close)
	return srv
}

func ok(s string) capability.JSONHandler {
	return func(context.Context, []byte) ([]byte, error) { return []byte(s), nil }
}

func failWith(err error) capability.JSONHandler {
	return func(context.Context, []byte) ([]byte, error) { return nil, err }
}

var fanProviders = map[string]capability.JSONHandler{
	"secret-a": ok(`{"trace":"a"}`),
	"secret-b": failWith(capability.ErrUnavailable),
	"secret-c": ok(`{"trace":5}`),                                     // breaks the contract
	"secret-d": failWith(capability.NewError("not_found", "no such")), // declared domain error
	"secret-e": failWith(capability.NewError("weird", "undeclared")),  // undeclared code
}
var fanOrder = []string{"secret-a", "secret-b", "secret-c", "secret-d", "secret-e"}

type fanBody struct {
	Output struct {
		Results []struct {
			Source string                 `json:"source"`
			Output json.RawMessage        `json:"output"`
			Error  *struct{ Code string } `json:"error"`
		} `json:"results"`
	} `json:"output"`
}

func TestFanoutAggregateKeepsTheContractPerSourceAndHidesCoreIds(t *testing.T) {
	srv := fanSetup(t, capability.Policy{Mode: capability.ModeFanout}, fanProviders, fanOrder)
	code, body := post(t, srv.URL, `{"capability":"echo.v1.fan","input":{}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("a core id leaked onto the wire: %s", body)
	}
	var got fanBody
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	res := got.Output.Results
	if len(res) != 5 {
		t.Fatalf("want 5 entries, got %d: %s", len(res), body)
	}
	for i, want := range []string{"1", "2", "3", "4", "5"} {
		if res[i].Source != want {
			t.Fatalf("entry %d has label %q, want the opaque position %q", i, res[i].Source, want)
		}
	}
	if string(res[0].Output) != `{"trace":"a"}` || res[0].Error != nil {
		t.Fatalf("good source: %+v", res[0])
	}
	for i, wantCode := range map[int]string{1: "unavailable", 2: "internal", 3: "not_found", 4: "internal"} {
		if res[i].Error == nil || res[i].Error.Code != wantCode || len(res[i].Output) != 0 {
			t.Errorf("entry %d: want error %q and no output, got %+v", i, wantCode, res[i])
		}
	}
}

func TestFanoutOnErrorFailFailsTheWholeCall(t *testing.T) {
	srv := fanSetup(t, capability.Policy{Mode: capability.ModeFanout, OnError: capability.OnErrorFail}, fanProviders, fanOrder)
	code, body := post(t, srv.URL, `{"capability":"echo.v1.fan","input":{}}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"unavailable"`) || strings.Contains(body, "results") {
		t.Fatalf("on_error fail: want 503 unavailable and no partial results, got %d %s", code, body)
	}
}

func TestFanoutRejectsBadInputAndHandlesNoProvider(t *testing.T) {
	srv := fanSetup(t, capability.Policy{Mode: capability.ModeFanout}, fanProviders, fanOrder)
	if code, body := post(t, srv.URL, `{"capability":"echo.v1.fan","input":{"x":1}}`); code != 400 || !strings.Contains(body, "invalid_input") {
		t.Fatalf("input is still checked against the contract: %d %s", code, body)
	}
	empty := fanSetup(t, capability.Policy{Mode: capability.ModeFanout}, nil, nil)
	if code, body := post(t, empty.URL, `{"capability":"echo.v1.fan","input":{}}`); code != 503 || !strings.Contains(body, "unavailable") {
		t.Fatalf("no provider: %d %s", code, body)
	}
}

func TestFanoutAllHealthyReturnsEveryAnswerInOrder(t *testing.T) {
	srv := fanSetup(t, capability.Policy{Mode: capability.ModeFanout, OnError: capability.OnErrorFail},
		map[string]capability.JSONHandler{"x": ok(`{"trace":"1st"}`), "y": ok(`{"trace":"2nd"}`)}, []string{"x", "y"})
	code, body := post(t, srv.URL, `{"capability":"echo.v1.fan","input":{}}`)
	if code != 200 || !strings.Contains(body, `"trace":"1st"`) || strings.Index(body, "1st") > strings.Index(body, "2nd") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestFanoutOnErrorFailNeverLetsAnUndeclaredCodeEscape(t *testing.T) {
	srv := fanSetup(t, capability.Policy{Mode: capability.ModeFanout, OnError: capability.OnErrorFail},
		map[string]capability.JSONHandler{"x": failWith(capability.NewError("weird", "undeclared"))}, []string{"x"})
	code, body := post(t, srv.URL, `{"capability":"echo.v1.fan","input":{}}`)
	if code != http.StatusInternalServerError || strings.Contains(body, "weird") {
		t.Fatalf("an undeclared code must become internal, got %d %s", code, body)
	}
}

func TestFanoutDeliversMetaToEverySource(t *testing.T) {
	echoTrace := func(ctx context.Context, _ []byte) ([]byte, error) {
		return json.Marshal(map[string]string{"trace": capability.MetaFrom(ctx)["trace_id"]})
	}
	srv := fanSetup(t, capability.Policy{Mode: capability.ModeFanout},
		map[string]capability.JSONHandler{"x": echoTrace, "y": echoTrace}, []string{"x", "y"})
	code, body := post(t, srv.URL, `{"capability":"echo.v1.fan","input":{},"meta":{"trace_id":"T-42"}}`)
	if code != 200 || strings.Count(body, `"trace":"T-42"`) != 2 {
		t.Fatalf("meta must reach every fan-out source: %d %s", code, body)
	}
}
