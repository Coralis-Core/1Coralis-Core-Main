package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	structural "example.com/project/contracts"
	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/jsonschema"
	"example.com/project/engine/verify/fixtures/app/demo"
	fixtures "example.com/project/engine/verify/fixtures/contracts"
	"example.com/project/engine/verify/fixtures/contracts/identity"
)

func allSet(t *testing.T) *contract.Set {
	t.Helper()
	set, err := contract.LoadAll(structural.FS, fixtures.FS)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func jsonProvider(r *capability.Router, name string) contract.Provider {
	return func(ctx context.Context, in []byte) ([]byte, string, error) {
		out, err := r.CallJSON(ctx, capability.Name(name), in)
		if err != nil {
			if code := capability.CodeOf(err); code != "" {
				return nil, code, nil
			}
			return nil, "", err
		}
		return out, "", nil
	}
}

const base = `{"version":1,"cores":[
  {"id":"dir","kind":"directory","options":{"users":{"7":"Sara (from A)"}}},
  {"id":"counter","kind":"counter"}],
 "bindings":[{"capability":"identity.v1.user.get","cores":["dir"]}]}`

// baseExposed is base plus an explicit decision about what callers may see.
const baseExposed = `{"version":1,"cores":[
  {"id":"dir","kind":"directory","options":{"users":{"7":"Sara (from A)"}}},
  {"id":"counter","kind":"counter"}],
 "bindings":[{"capability":"identity.v1.user.get","cores":["dir"]}],
 "serve":{"expose":["identity.v1.user.get"]}}`

func mustParse(t *testing.T, doc string) config.Config {
	t.Helper()
	c, err := config.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ---- structural contracts ---------------------------------------------------

// A platform answers the structural contracts itself (that is what lets
// platforms nest), and passes the same examples + negative cases as any core.
func TestPlatformConformsToStructuralContracts(t *testing.T) {
	set := allSet(t)
	p, err := demo.Build(baseExposed)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	for _, name := range []string{boot.CapDescribe, boot.CapHealth} {
		c, _ := set.Get(name)
		prov := contract.Guard(c, jsonProvider(p.Host().Router, name))
		if problems := contract.Check(context.Background(), c, prov); len(problems) > 0 {
			t.Errorf("%s: %v", name, problems)
		}
	}
	out, err := p.Host().Router.CallJSON(context.Background(), boot.CapDescribe, []byte(`{}`))
	if err != nil || !strings.Contains(string(out), "identity.v1.user.get") || strings.Contains(string(out), "core.v1.") {
		t.Fatalf("describe should list mounted capabilities but not the structural ones: %s %v", out, err)
	}
}

// ---- configuration: JSON Schema and Go parser must agree ---------------------

func TestConfigCorpusAgreesBetweenSchemaAndParser(t *testing.T) {
	raw, err := fs.ReadFile(structural.FS, "config/v1/platform.config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.Compile(raw)
	if err != nil {
		t.Fatalf("config schema must stay inside the supported subset: %v", err)
	}
	files, _ := filepath.Glob("../testdata/config/*.json")
	if len(files) < 10 {
		t.Fatalf("corpus missing (%d files)", len(files))
	}
	for _, f := range files {
		doc, _ := os.ReadFile(f)
		name := filepath.Base(f)
		serr := schema.Validate(doc)
		_, perr := config.Parse(bytes.NewReader(doc))
		switch {
		case strings.HasPrefix(name, "valid-"):
			if serr != nil || perr != nil {
				t.Errorf("%s: schema=%v parser=%v (both must accept)", name, serr, perr)
			}
		case strings.HasPrefix(name, "invalid-schema-"):
			if serr == nil || perr == nil {
				t.Errorf("%s: schema=%v parser=%v (both must reject)", name, serr, perr)
			}
		case strings.HasPrefix(name, "invalid-semantic-"):
			if serr != nil || perr == nil {
				t.Errorf("%s: schema=%v parser=%v (schema accepts, parser must reject)", name, serr, perr)
			}
		default:
			t.Errorf("%s: unknown corpus prefix", name)
		}
	}
}

// ---- wire schemas -------------------------------------------------------------

func TestWireEnvelopesMatchTheirSchemas(t *testing.T) {
	compile := func(path string) *jsonschema.Schema {
		raw, err := fs.ReadFile(structural.FS, path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := jsonschema.Compile(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return s
	}
	reqSchema, respSchema := compile("wire/v1/request.schema.json"), compile("wire/v1/response.schema.json")

	set := allSet(t)
	p, err := demo.Build(base)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	srv := httptest.NewServer(p.Handler(set, "tok", nil))
	defer srv.Close()

	post := func(body, token string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/call", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	exactlyOne := func(b []byte) (hasOut, hasErr bool) {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		_, hasOut = m["output"]
		_, hasErr = m["error"]
		return
	}

	good := `{"capability":"core.v1.health","input":{}}`
	if err := reqSchema.Validate([]byte(good)); err != nil {
		t.Fatalf("a real request must satisfy the request schema: %v", err)
	}
	status, body := post(good, "tok")
	if o, e := exactlyOne(body); status != 200 || !o || e || respSchema.Validate(body) != nil {
		t.Fatalf("success envelope wrong: %d %s", status, body)
	}

	// every failure is an error envelope that satisfies the response schema, and never has an output
	for name, c := range map[string]struct {
		body, token string
		status      int
		code        string
	}{
		"unknown capability":  {`{"capability":"x.v1.nope","input":{}}`, "tok", 404, "unknown_capability"},
		"invalid input":       {`{"capability":"core.v1.health","input":{"x":1}}`, "tok", 400, "invalid_input"},
		"missing input":       {`{"capability":"core.v1.health"}`, "tok", 400, "invalid_input"},
		"extra request field": {`{"capability":"core.v1.health","input":{},"x":1}`, "tok", 400, "invalid_input"},
		"not json":            {`nope`, "tok", 400, "invalid_input"},
		"bad token":           {good, "wrong", 401, "unauthorized"},
		"no token":            {good, "", 401, "unauthorized"},
	} {
		status, body := post(c.body, c.token)
		o, e := exactlyOne(body)
		if status != c.status || o || !e || respSchema.Validate(body) != nil || !strings.Contains(string(body), `"`+c.code+`"`) {
			t.Errorf("%s: got %d %s (want %d %s)", name, status, body, c.status, c.code)
		}
	}
	// the request schema rejects exactly what the server rejects at the envelope level
	for _, bad := range []string{`{"capability":"core.v1.health"}`, `{"capability":"core.v1.health","input":{},"x":1}`, `{"capability":"","input":{}}`, `{"input":{}}`} {
		if reqSchema.Validate([]byte(bad)) == nil {
			t.Errorf("request schema should reject %s", bad)
		}
	}
}

// ---- mounting by configuration --------------------------------------------------

// A whole platform mounted as a core of another one, discovered through
// core.v1.describe, token read from the environment, nested health reported.
func TestMountPlatformOverHTTPWithAutoDiscoveryAndHealth(t *testing.T) {
	set := allSet(t)
	p1, err := demo.Build(baseExposed)
	if err != nil {
		t.Fatal(err)
	}
	defer p1.Stop(context.Background())
	srv := httptest.NewServer(p1.Handler(set, "tok-1", nil))

	cfg2 := mustParse(t, `{"version":1,"cores":[{"id":"below","kind":"http","options":{"url":"`+srv.URL+`","token_env":"BELOW_TOKEN","timeout_ms":300}}]}`)
	env := func(k string) string { return map[string]string{"BELOW_TOKEN": "tok-1"}[k] }
	p2, err := boot.Build(cfg2, boot.Options{Contracts: set, Getenv: env})
	if err != nil {
		t.Fatal(err)
	}
	if err := p2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p2.Stop(context.Background())

	// no capabilities were listed in the config: they were discovered
	u, err := identity.GetUser.Call(context.Background(), p2.Host().Router, identity.UserQuery{ID: 7})
	if err != nil || u.Name != "Sara (from A)" {
		t.Fatalf("call through the mounted platform: %+v %v", u, err)
	}
	if _, err := identity.GetUser.Call(context.Background(), p2.Host().Router, identity.UserQuery{ID: 999}); capability.CodeOf(err) != "not_found" {
		t.Fatalf("domain errors must cross the mount: %v", err)
	}
	if st, _ := boot.Summary(p2.Health(context.Background())); st != "ok" {
		t.Fatalf("health with a live core: %s", st)
	}
	srv.Close() // the core below disappears
	hs := p2.Health(context.Background())
	if st, d := boot.Summary(hs); st != "down" || !strings.Contains(d, "below") {
		t.Fatalf("health with the only core gone: %s / %s", st, d)
	}
}

func TestBadMountsFailAtStartup(t *testing.T) {
	set := allSet(t)
	p1, err := demo.Build(`{"version":1,"cores":[{"id":"counter","kind":"counter"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	defer p1.Stop(context.Background())
	srv := httptest.NewServer(p1.Handler(set, "", nil))
	defer srv.Close()

	build := func(doc string, env map[string]string) error {
		p, err := boot.Build(mustParse(t, doc), boot.Options{Contracts: set, Getenv: func(k string) string { return env[k] }, Factories: demo.Factories()})
		if err != nil {
			return err
		}
		return p.Start(context.Background())
	}
	url := srv.URL
	for name, c := range map[string]struct {
		doc  string
		want string
	}{
		"config claims a capability the core does not provide": {`{"version":1,"cores":[{"id":"x","kind":"http","capabilities":["identity.v1.user.get"],"options":{"url":"` + url + `"}}]}`, "does not provide"},
		"token env var is empty":                               {`{"version":1,"cores":[{"id":"x","kind":"http","options":{"url":"` + url + `","token_env":"NOPE"}}]}`, "refusing to mount"},
		"http core without url":                                {`{"version":1,"cores":[{"id":"x","kind":"http","options":{}}]}`, "url is required"},
		"http core without options":                            {`{"version":1,"cores":[{"id":"x","kind":"http"}]}`, "options.url"},
		"unknown option":                                       {`{"version":1,"cores":[{"id":"x","kind":"http","options":{"url":"` + url + `","urll":1}}]}`, "options"},
		"unknown kind":                                         {`{"version":1,"cores":[{"id":"x","kind":"nope"}]}`, "unknown kind"},
		"unreachable core and nothing listed":                  {`{"version":1,"cores":[{"id":"x","kind":"http","options":{"url":"http://127.0.0.1:1"}}]}`, "cannot discover"},
	} {
		err := build(c.doc, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
}

// A core in another language, mounted purely by config: discovered through
// describe, preferred by binding, and replaced by the next one when it dies.
func TestPythonCoreMountedByConfigWithFallback(t *testing.T) {
	pyURL, kill := startPython(t)
	set := allSet(t)
	doc := `{"version":1,"default_timeout_ms":1000,
	  "cores":[{"id":"py","kind":"http","options":{"url":"` + pyURL + `"}},
	           {"id":"local","kind":"directory","options":{"users":{"7":"Sara (local)"}}}],
	  "bindings":[{"capability":"identity.v1.user.get","cores":["py","local"]}]}`
	p, err := boot.Build(mustParse(t, doc), boot.Options{Contracts: set, Factories: demo.Factories()})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())

	call := func() (identity.User, error) {
		return identity.GetUser.Call(context.Background(), p.Host().Router, identity.UserQuery{ID: 7})
	}
	if u, err := call(); err != nil || u.Name != "Sara" {
		t.Fatalf("python core should answer first: %+v %v", u, err)
	}
	all, err := identity.GetUser.CallAll(context.Background(), p.Host().Router, identity.UserQuery{ID: 7})
	if err != nil || len(all) != 2 || all[0].Provider != "py" || all[1].Provider != "local" {
		t.Fatalf("fan-out across a Python core and a Go core: %+v %v", all, err)
	}
	if st, _ := boot.Summary(p.Health(context.Background())); st != "ok" {
		t.Fatalf("both healthy: %s", st)
	}
	kill()
	if u, err := call(); err != nil || u.Name != "Sara (local)" {
		t.Fatalf("after the python core dies the next bound core must answer: %+v %v", u, err)
	}
	if st, d := boot.Summary(p.Health(context.Background())); st != "down" && st != "degraded" || !strings.Contains(d, "py") {
		t.Fatalf("health must notice the dead core: %s / %s", st, d)
	}
}
