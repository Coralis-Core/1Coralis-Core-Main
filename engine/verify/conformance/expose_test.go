package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/project/engine/base/boot"
	"example.com/project/engine/base/capability"
	"example.com/project/engine/verify/fixtures/app/demo"
	"example.com/project/engine/verify/fixtures/contracts/identity"
)

// post sends one wire request and returns status and body.
func post(t *testing.T, url, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/call", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func serveConfig(t *testing.T, doc string, token string) (*boot.Platform, *httptest.Server) {
	t.Helper()
	p, err := demo.Build(doc)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p.Handler(allSet(t), token, nil))
	t.Cleanup(func() { srv.Close(); p.Stop(context.Background()) })
	return p, srv
}

const idCall = `{"capability":"identity.v1.user.get","input":{"id":7}}`
const chatCall = `{"capability":"identity.v1.chat.get","input":{"id":1}}`
const statsCall = `{"capability":"stats.v1.messages.count","input":{"chat_id":1}}`
const ghostCall = `{"capability":"nothing.v1.here","input":{}}`

func wireCode(body string) string {
	var r struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &r)
	return r.Error.Code
}

// The customer sees the outer layer only: unlisted capabilities exist
// internally and still look exactly like capabilities that do not exist.
func TestExposeShowsOnlyListedCapabilities(t *testing.T) {
	_, srv := serveConfig(t, baseExposed, "tok")
	if st, body := post(t, srv.URL, "tok", idCall); st != 200 || !strings.Contains(body, "Sara") {
		t.Fatalf("exposed capability: %d %s", st, body)
	}
	hidden := map[string]string{"identity.v1.chat.get": chatCall, "stats.v1.messages.count": statsCall}
	gst, gbody := post(t, srv.URL, "tok", ghostCall)
	for name, call := range hidden {
		st, body := post(t, srv.URL, "tok", call)
		if st != gst || wireCode(body) != wireCode(gbody) || wireCode(body) != "unknown_capability" {
			t.Errorf("%s: hidden capability must look nonexistent: got %d %s, nonexistent gives %d %s", name, st, body, gst, gbody)
		}
	}
	// describe lists exactly what is exposed, never the internals
	_, d := post(t, srv.URL, "tok", `{"capability":"core.v1.describe","input":{}}`)
	var desc struct {
		Output struct{ Capabilities []string } `json:"output"`
	}
	_ = json.Unmarshal([]byte(d), &desc)
	if len(desc.Output.Capabilities) != 1 || desc.Output.Capabilities[0] != "identity.v1.user.get" {
		t.Fatalf("describe leaks or hides the wrong things: %s", d)
	}
	// the exposure decision happens after authentication: no token, no information
	if st, body := post(t, srv.URL, "", chatCall); st != 401 || wireCode(body) != "unauthorized" {
		t.Fatalf("unauthenticated probing must get 401, got %d %s", st, body)
	}
}

func TestDefaultExposesNothingButStructuralAndWildcardExposesAll(t *testing.T) {
	_, closed := serveConfig(t, strings.Replace(baseExposed, `"serve":{"expose":["identity.v1.user.get"]}`, `"serve":{}`, 1), "")
	if st, body := post(t, closed.URL, "", idCall); st != 404 || wireCode(body) != "unknown_capability" {
		t.Fatalf("by default nothing is exposed: %d %s", st, body)
	}
	if st, _ := post(t, closed.URL, "", `{"capability":"core.v1.health","input":{}}`); st != 200 {
		t.Fatalf("structural capabilities stay available: %d", st)
	}
	_, open := serveConfig(t, strings.Replace(baseExposed, `["identity.v1.user.get"]`, `["*"]`, 1), "")
	for _, call := range []string{idCall, statsCall} {
		if st, body := post(t, open.URL, "", call); st != 200 {
			t.Fatalf("\"*\" must expose everything: %d %s", st, body)
		}
	}
}

func TestExposeTypoFailsAtStartup(t *testing.T) {
	_, err := demo.Build(strings.Replace(baseExposed, `"identity.v1.user.get"]}}`, `"identity.v1.user.get","identity.v1.nope"]}}`, 1))
	if err == nil || !strings.Contains(err.Error(), "identity.v1.nope") {
		t.Fatalf("a typo in serve.expose must fail at startup, got %v", err)
	}
}

// Health from outside says "ok/degraded/down" and nothing about what is behind.
func TestHealthDetailStaysInside(t *testing.T) {
	doc := `{"version":1,
	  "cores":[{"id":"dir","kind":"directory","options":{"users":{"7":"Sara"}}},
	           {"id":"secret-core-name","kind":"http","capabilities":["identity.v1.chat.get"],"options":{"url":"http://127.0.0.1:1","timeout_ms":200}}],
	  "bindings":[{"capability":"identity.v1.user.get","cores":["dir"]}],
	  "serve":{"expose":["identity.v1.user.get"]}}`
	p, srv := serveConfig(t, doc, "tok")
	if _, d := boot.Summary(p.Health(context.Background())); !strings.Contains(d, "secret-core-name") {
		t.Fatalf("internal health (Go API) keeps the detail: %q", d)
	}
	_, body := post(t, srv.URL, "tok", `{"capability":"core.v1.health","input":{}}`)
	if strings.Contains(body, "secret-core-name") || strings.Contains(body, "detail") || !strings.Contains(body, `"status"`) {
		t.Fatalf("outside health must be status only: %s", body)
	}
	// /healthz has per-core detail, so it is protected like the API; /livez is open and bare
	if st, _ := get(t, srv.URL+"/healthz", ""); st != 401 {
		t.Fatalf("/healthz without the token must be 401, got %d", st)
	}
	if st, b := get(t, srv.URL+"/healthz", "tok"); st == 401 || !strings.Contains(b, "secret-core-name") {
		t.Fatalf("/healthz with the token shows detail: %d %s", st, b)
	}
	st, b := get(t, srv.URL+"/livez", "")
	if st == 401 || strings.Contains(b, "secret-core-name") || strings.Contains(b, "cores") {
		t.Fatalf("/livez must be open and bare: %d %s", st, b)
	}
}

// Nesting respects exposure: a platform mounted from outside only gets what the
// platform below chose to show.
func TestNestedPlatformSeesOnlyWhatIsExposed(t *testing.T) {
	_, below := serveConfig(t, baseExposed, "")
	upper, err := boot.Build(mustParse(t, `{"version":1,"cores":[{"id":"below","kind":"http","options":{"url":"`+below.URL+`"}}]}`), boot.Options{Contracts: allSet(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := upper.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer upper.Stop(context.Background())
	if _, err := identity.GetUser.Call(context.Background(), upper.Host().Router, identity.UserQuery{ID: 7}); err != nil {
		t.Fatalf("exposed capability must be reachable: %v", err)
	}
	if _, err := identity.GetChat.Call(context.Background(), upper.Host().Router, identity.ChatQuery{ID: 1}); err == nil || !strings.Contains(err.Error(), capability.ErrNoProvider.Error()) {
		t.Fatalf("an unexposed capability must not even be mounted above: %v", err)
	}
}
