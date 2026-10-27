package conformance

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"example.com/project/engine/base/capability"
	"example.com/project/engine/base/config"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/kernel"
	"example.com/project/engine/base/transports/httpjson"
	"example.com/project/engine/verify/fixtures/contracts"
	"example.com/project/engine/verify/fixtures/contracts/identity"
	"example.com/project/engine/verify/fixtures/contracts/stats"
	"example.com/project/engine/verify/fixtures/cores/memory"
)

func loadSet(t *testing.T) *contract.Set {
	t.Helper()
	set, err := contract.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// goPlatform: a platform whose Go cores hold the data the contract examples use.
func goPlatform(t *testing.T, set *contract.Set) *kernel.Platform {
	t.Helper()
	p := kernel.New(config.Config{DefaultTimeoutMS: 2000}, nil)
	p.RequireContracts(set)
	dir := memory.NewDirectory("dir")
	dir.Users[7] = identity.User{ID: 7, Username: "sara", Name: "Sara", Language: "fa"}
	dir.Users[8] = identity.User{ID: 8}
	dir.Users[5000000000] = identity.User{ID: 5000000000, Username: "big"}
	dir.Chats[1] = identity.Chat{ID: 1, Name: "Team", Language: "fa"}
	dir.Chats[2] = identity.Chat{ID: 2}
	dir.Chats[-1001234567890] = identity.Chat{ID: -1001234567890, Name: "Supergroup"}
	for _, c := range []kernel.Core{dir, memory.NewCounter("counter")} {
		if err := p.AddCore(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })
	return p
}

var requestCaps = []string{"identity.v1.user.get", "identity.v1.chat.get", "stats.v1.messages.count"}

// Go cores exposed over HTTP; a remote client must see a contract-conformant core.
func TestHTTPServerExposesConformantCores(t *testing.T) {
	set := loadSet(t)
	p := goPlatform(t, set)
	srv := httptest.NewServer(httpjson.NewHandler(set, p.Host().Router, httpjson.Options{Token: "s3cret"}))
	defer srv.Close()

	rm := httpjson.Remote{BaseURL: srv.URL, Token: "s3cret"}
	for _, name := range requestCaps {
		c, _ := set.Get(name)
		if problems := contract.Check(context.Background(), c, rm.Provider(c)); len(problems) > 0 {
			t.Errorf("%s over HTTP not conformant: %v", name, problems)
		}
	}
	// wrong token
	bad := httpjson.Remote{BaseURL: srv.URL, Token: "nope"}
	c, _ := set.Get("identity.v1.user.get")
	if _, code, _ := bad.Provider(c)(context.Background(), []byte(`{"id":7}`)); code != "unauthorized" {
		t.Fatalf("want unauthorized, got %q", code)
	}
}

func TestHTTPServerRobustness(t *testing.T) {
	set := loadSet(t)
	p := goPlatform(t, set)
	srv := httptest.NewServer(httpjson.NewHandler(set, p.Host().Router, httpjson.Options{MaxBody: 256}))
	defer srv.Close()
	post := func(body string) int {
		resp, err := http.Post(srv.URL+httpjson.Path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := map[string]int{
		`{"capability":"identity.v1.user.get","input":{"id":7}}`:                                          200,
		`{"capability":"identity.v1.user.get","input":{"id":999}}`:                                        422, // declared domain error
		`{"capability":"identity.v1.user.get","input":{"id":"x"}}`:                                        400, // schema violation
		`{"capability":"identity.v1.user.get"}`:                                                           400, // no input
		`not json`:                                                                                        400,
		`{"capability":"identity.v1.user.get","input":{"id":7},"extra":1}`:                                400, // unknown envelope field
		`{"capability":"nope.v1.thing","input":{}}`:                                                       404,
		`{"capability":"chat.v1.message.seen","input":{}}`:                                                404, // event contract is not callable
		`{"capability":"identity.v1.user.get","input":{"id":7,"pad":"` + strings.Repeat("x", 400) + `"}}`: 413,
	}
	for body, want := range cases {
		if got := post(body); got != want {
			t.Errorf("%.60s... -> HTTP %d, want %d", body, got, want)
		}
	}
	resp, _ := http.Get(srv.URL + httpjson.Path)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET -> %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// A provider that hangs must surface as 503 unavailable (router timeout), not hang the server.
func TestHTTPServerSlowProviderBecomesUnavailable(t *testing.T) {
	set := loadSet(t)
	r := capability.NewRouter()
	r.Timeout = 50 * time.Millisecond
	identity.GetUser.Provide(r, "slow", func(ctx context.Context, _ identity.UserQuery) (identity.User, error) {
		<-ctx.Done()
		return identity.User{}, ctx.Err()
	})
	srv := httptest.NewServer(httpjson.NewHandler(set, r, httpjson.Options{}))
	defer srv.Close()
	c, _ := set.Get("identity.v1.user.get")
	_, code, _ := httpjson.Remote{BaseURL: srv.URL}.Provider(c)(context.Background(), []byte(`{"id":7}`))
	if code != "unavailable" {
		t.Fatalf("want unavailable, got %q", code)
	}
}

// startPython launches the Python HTTP core on a free port and returns its URL.
func startPython(t *testing.T) (url string, kill func()) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	cmd := exec.Command(py, "server.py", "0")
	cmd.Dir = "../fixtures/cores/python"
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	kill = func() { cmd.Process.Kill(); cmd.Wait() }
	t.Cleanup(kill)
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(out).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		f := strings.Fields(s)
		if len(f) != 2 || f[0] != "LISTENING" {
			t.Fatalf("unexpected python output %q", s)
		}
		return "http://127.0.0.1:" + f[1], kill
	case <-time.After(10 * time.Second):
		t.Fatal("python server did not start")
		return "", kill
	}
}

// A Go platform consumes a PYTHON core over HTTP exactly like a local one,
// and falls back to the local core when the Python process dies.
func TestGoPlatformUsesPythonCoreOverHTTPWithFallback(t *testing.T) {
	set := loadSet(t)
	url, kill := startPython(t)

	r := capability.NewRouter()
	r.Timeout = 2 * time.Second
	rm := httpjson.Remote{BaseURL: url, Client: &http.Client{Timeout: time.Second}}
	if err := rm.Bind(r, "py", set, "identity.v1.user.get", "identity.v1.chat.get"); err != nil {
		t.Fatal(err)
	}
	// local fallback core with different data so we can see who answered
	identity.GetUser.Provide(r, "local", func(_ context.Context, q identity.UserQuery) (identity.User, error) {
		return identity.User{ID: q.ID, Name: "local"}, nil
	})
	r.Bind("identity.v1.user.get", "py", "local")
	ctx := context.Background()

	u, err := identity.GetUser.Call(ctx, r, identity.UserQuery{ID: 7})
	if err != nil || u.Name != "Sara" {
		t.Fatalf("python core answer: %+v %v", u, err)
	}
	// domain error crosses the language boundary as a coded error (no fallback!)
	if _, err := identity.GetUser.Call(ctx, r, identity.UserQuery{ID: 999}); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("want not_found from python, got %v", err)
	}
	// the whole contract suite, python core reached through the router
	for _, name := range []string{"identity.v1.user.get", "identity.v1.chat.get"} {
		c, _ := set.Get(name)
		prov := contract.Guard(c, func(ctx context.Context, in []byte) ([]byte, string, error) {
			out, err := r.CallJSON(ctx, capability.Name(name), in)
			if err != nil {
				return nil, capability.CodeOf(err), nil
			}
			return out, "", nil
		})
		// user.get has a fallback with other data, so only run chat.get examples strictly
		if name == "identity.v1.chat.get" {
			if problems := contract.Check(ctx, c, prov); len(problems) > 0 {
				t.Errorf("%s via python/HTTP: %v", name, problems)
			}
		}
	}

	kill() // python dies -> unavailable -> router falls back
	u, err = identity.GetUser.Call(ctx, r, identity.UserQuery{ID: 7})
	if err != nil || u.Name != "local" {
		t.Fatalf("fallback after python died: %+v %v", u, err)
	}
}

// Bind refuses capabilities without a request contract.
func TestBindRequiresRequestContract(t *testing.T) {
	set := loadSet(t)
	rm := httpjson.Remote{BaseURL: "http://127.0.0.1:1"}
	if err := rm.Bind(capability.NewRouter(), "x", set, "chat.v1.message.seen"); err == nil {
		t.Fatal("event contracts are not callable capabilities")
	}
	if err := rm.Bind(capability.NewRouter(), "x", set, "nope.v1.x"); err == nil {
		t.Fatal("unknown capability must be refused")
	}
	_ = stats.MessageCount
	_ = bytes.MinRead
}
