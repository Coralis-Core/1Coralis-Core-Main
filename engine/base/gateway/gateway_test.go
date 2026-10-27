package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"example.com/project/engine/base/capability"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func newGW(t *testing.T, opts string, e map[string]string) (*Core, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	g, err := New("gw", json.RawMessage(opts), env(e), slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return g, &buf
}

func call(g *Core, in string) (response, error) {
	out, err := g.handle(context.Background(), []byte(in))
	var r response
	if err == nil {
		_ = json.Unmarshal(out, &r)
	}
	return r, err
}

func hostOf(t *testing.T, srv *httptest.Server) string {
	u, _ := url.Parse(srv.URL)
	return u.Hostname()
}

func TestStrictByDefaultOnlyListedDomains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hi")) }))
	defer srv.Close()
	g, _ := newGW(t, `{"allow_private":true,"allowed_domains":["`+hostOf(t, srv)+`"]}`, nil)
	if r, err := call(g, `{"url":"`+srv.URL+`/x"}`); err != nil || r.Status != 200 || r.Body != "hi" {
		t.Fatalf("listed domain: %+v %v", r, err)
	}
	if _, err := call(g, `{"url":"http://other.invalid/"}`); capability.CodeOf(err) != "domain_not_allowed" {
		t.Fatalf("unlisted domain must be refused: %v", err)
	}
	// nothing listed = nothing allowed (the safe default)
	g2, _ := newGW(t, `{"allow_private":true}`, nil)
	if _, err := call(g2, `{"url":"`+srv.URL+`"}`); capability.CodeOf(err) != "domain_not_allowed" {
		t.Fatalf("empty allow-list must allow nothing: %v", err)
	}
	// wildcard
	if !domainMatches("*.example.com", "api.example.com") || domainMatches("*.example.com", "example.com") || domainMatches("*.example.com", "evilexample.com") {
		t.Fatal("wildcard matching is wrong")
	}
}

func TestPrivateAddressesBlockedByDefaultEvenIfListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer srv.Close()
	g, _ := newGW(t, `{"allowed_domains":["`+hostOf(t, srv)+`"]}`, nil) // allow_private not set
	if _, err := call(g, `{"url":"`+srv.URL+`"}`); capability.CodeOf(err) != "blocked_address" {
		t.Fatalf("loopback must be blocked at connect time: %v", err)
	}
	g2, _ := newGW(t, `{"mode":"open"}`, nil)
	if _, err := call(g2, `{"url":"http://169.254.169.254/latest/meta-data"}`); capability.CodeOf(err) != "blocked_address" {
		t.Fatalf("metadata address must be blocked even in open mode: %v", err)
	}
}

func TestRedirectsNotFollowedByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/end", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("end"))
	}))
	defer srv.Close()
	h := hostOf(t, srv)
	g, _ := newGW(t, `{"allow_private":true,"allowed_domains":["`+h+`"]}`, nil)
	if r, err := call(g, `{"url":"`+srv.URL+`/start"}`); err != nil || r.Status != 302 {
		t.Fatalf("redirect must be returned, not followed: %+v %v", r, err)
	}
	g2, _ := newGW(t, `{"allow_private":true,"follow_redirects":true,"allowed_domains":["`+h+`"]}`, nil)
	if r, err := call(g2, `{"url":"`+srv.URL+`/start"}`); err != nil || r.Body != "end" {
		t.Fatalf("non-default mode follows: %+v %v", r, err)
	}
}

func TestSizeLimitKeyInjectionCookiesAndLogHygiene(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "sid=1")
		if r.URL.Path == "/big" {
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
			return
		}
		_, _ = w.Write([]byte(r.Header.Get("Authorization")))
	}))
	defer srv.Close()
	h := hostOf(t, srv)
	opts := `{"allow_private":true,"max_response_bytes":50,"allowed_domains":["` + h + `"],"keys":[{"domain":"` + h + `","header":"Authorization","env":"K","prefix":"Bearer "}]}`
	g, logs := newGW(t, opts, map[string]string{"K": "s3cret"})
	r, err := call(g, `{"url":"`+srv.URL+`/who?token=querysecret","headers":{"Authorization":"caller-tries"}}`)
	if err != nil || r.Body != "Bearer s3cret" {
		t.Fatalf("the platform's key must win over the caller's header: %+v %v", r, err)
	}
	if _, has := r.Headers["set-cookie"]; has {
		t.Fatal("cookies must not reach cores")
	}
	if _, err := call(g, `{"url":"`+srv.URL+`/big"}`); capability.CodeOf(err) != "too_large" {
		t.Fatalf("size limit: %v", err)
	}
	if out := logs.String(); strings.Contains(out, "s3cret") || strings.Contains(out, "querysecret") || !strings.Contains(out, "outbound call") {
		t.Fatalf("every call is logged, but never with keys or query strings:\n%s", out)
	}
}

func TestConfigErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	for name, raw := range map[string]string{
		"unknown field": `{"nope":1}`,
		"bad mode":      `{"mode":"wide"}`,
		"negative":      `{"timeout_ms":-1}`,
		"key missing":   `{"keys":[{"domain":"a"}]}`,
		"empty env var": `{"keys":[{"domain":"a","header":"H","env":"MISSING"}]}`,
	} {
		if _, err := New("g", json.RawMessage(raw), env(nil), log); err == nil {
			t.Errorf("%s must fail at startup", name)
		}
	}
	if _, err := New("g", nil, env(nil), log); err != nil {
		t.Fatalf("no options at all works (strict, nothing allowed): %v", err)
	}
}

func TestOnlyHttpAndHttpsAreAcceptedAndNoCredentialsInURL(t *testing.T) {
	g, _ := newGW(t, `{"mode":"open"}`, nil)
	for _, u := range []string{"file:///etc/passwd", "ftp://a.example/x", "http://user:pw@a.example/", "http:///nohost"} {
		if _, err := call(g, `{"url":"`+u+`"}`); capability.CodeOf(err) != "invalid_input" {
			t.Errorf("%s must be invalid_input, got %v", u, err)
		}
	}
}
