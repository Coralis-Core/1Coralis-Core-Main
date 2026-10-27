package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/project/contracts"
	"example.com/project/engine/base/contract"
	grpcjson "example.com/project/engine/contrib/grpc"
)

const echoContract = `{
  "capability": "echo.v1.say", "version": "1.0.0", "kind": "request", "idempotent": true,
  "summary": "Return the text unchanged.",
  "input":  {"type":"object","required":["text"],"additionalProperties":false,"properties":{"text":{"type":"string","maxLength":100}}},
  "output": {"type":"object","required":["text"],"additionalProperties":false,"properties":{"text":{"type":"string"}}},
  "examples": [{"name":"basic","input":{"text":"hi"},"output":{"text":"hi"}}],
  "invalid_inputs": [{"name":"missing","input":{}}]
}`

// fakeCore is a tiny HTTP core: describe, health and echo.v1.say.
func fakeCore(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Capability string          `json:"capability"`
			Input      json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Capability {
		case "core.v1.describe":
			fmt.Fprint(w, `{"output":{"name":"echo","version":"1","capabilities":["echo.v1.say"],"publishes":[],"subscribes":[]}}`)
		case "core.v1.health":
			fmt.Fprint(w, `{"output":{"status":"ok"}}`)
		case "echo.v1.say":
			fmt.Fprintf(w, `{"output":%s}`, req.Input)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"unknown_capability"}}`)
		}
	}))
}

func TestPlatformGRPCServesHTTPAndGRPCAndMountsGRPC(t *testing.T) {
	core := fakeCore(t)
	defer core.Close()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "dc", "echo", "v1"), 0o755)
	os.WriteFile(filepath.Join(dir, "dc", "echo", "v1", "say.contract.json"), []byte(echoContract), 0o644)
	t.Setenv("PLATFORM_TOKEN", "tok")

	write := func(name, doc string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(doc), 0o644)
		return p
	}
	cfgA := write("a.json", fmt.Sprintf(`{"version":1,"contracts_dirs":["dc"],
	  "cores":[{"id":"echo","kind":"http","options":{"url":%q}}],
	  "serve":{"listen":"127.0.0.1:0","token_env":"PLATFORM_TOKEN","expose":["echo.v1.say"]}}`, core.URL))

	type addrs struct{ http, grpc string }
	start := func(cfg, grpcListen string) (addrs, context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan addrs, 1)
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, options{cfgPath: cfg, grpcListen: grpcListen}, func(h, g string) { got <- addrs{h, g} })
		}()
		select {
		case a := <-got:
			return a, cancel, done
		case err := <-done:
			t.Fatalf("platform did not start: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("platform did not start in time")
		}
		return addrs{}, cancel, done
	}

	// --- platform A: HTTP + gRPC in front of an HTTP core
	a, stopA, doneA := start(cfgA, "127.0.0.1:0")
	defer func() { stopA(); <-doneA }()

	post := func(url, token, body string) (int, string) {
		req, _ := http.NewRequest("POST", url+"/v1/call", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		b, _ := json.Marshal(m)
		return resp.StatusCode, string(b)
	}
	if st, b := post("http://"+a.http, "tok", `{"capability":"echo.v1.say","input":{"text":"hi"}}`); st != 200 || b != `{"output":{"text":"hi"}}` {
		t.Fatalf("HTTP: %d %s", st, b)
	}
	if st, _ := post("http://"+a.http, "", `{"capability":"echo.v1.say","input":{"text":"hi"}}`); st != 401 {
		t.Fatalf("HTTP without token must be 401, got %d", st)
	}

	// --- gRPC straight into A (a real client, the token in gRPC metadata)
	set, err := contract.LoadAll(contracts.FS, os.DirFS(filepath.Join(dir, "dc")))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := set.Get("echo.v1.say")
	r, err := grpcjson.Dial(a.grpc)
	if err != nil {
		t.Fatal(err)
	}
	r.Token = "tok"
	out, code, _ := r.Provider(c)(context.Background(), []byte(`{"text":"over grpc"}`))
	if code != "" || string(out) != `{"text":"over grpc"}` {
		t.Fatalf("gRPC call: %q %q", out, code)
	}
	r.Token = "wrong"
	if _, code, _ := r.Provider(c)(context.Background(), []byte(`{"text":"x"}`)); code != "unauthorized" {
		t.Fatalf("gRPC with a wrong token must be unauthorized, got %q", code)
	}
	r.Token = "tok"
	if _, code, _ := r.Provider(c)(context.Background(), []byte(`{"text":123}`)); code != "invalid_input" {
		t.Fatalf("gRPC with an invalid input must be invalid_input, got %q", code)
	}

	// --- platform B mounts A over gRPC (kind "grpc") and serves it on HTTP
	t.Setenv("A_TOKEN", "tok")
	cfgB := write("b.json", fmt.Sprintf(`{"version":1,"contracts_dirs":["dc"],
	  "cores":[{"id":"a","kind":"grpc","capabilities":["echo.v1.say"],"options":{"target":%q,"token_env":"A_TOKEN","timeout_ms":3000}}],
	  "serve":{"listen":"127.0.0.1:0","token_env":"PLATFORM_TOKEN","expose":["echo.v1.say"]}}`, a.grpc))
	b, stopB, doneB := start(cfgB, "")
	defer func() { stopB(); <-doneB }()
	if st, body := post("http://"+b.http, "tok", `{"capability":"echo.v1.say","input":{"text":"through two platforms"}}`); st != 200 || body != `{"output":{"text":"through two platforms"}}` {
		t.Fatalf("B -> A over gRPC: %d %s", st, body)
	}
}
