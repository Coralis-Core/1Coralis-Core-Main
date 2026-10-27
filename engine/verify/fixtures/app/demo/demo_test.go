package demo

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"example.com/project/engine/verify/fixtures/app/console"
)

// cfg builds a config that mounts two sample directories (and optionally a
// counter) and binds identity to the given directory. A capability nobody is
// declared to serve simply has no provider.
func cfg(identityCore string, withCounter bool) string {
	cores := `{"id":"profile","kind":"profile"},
	    {"id":"dir-a","kind":"directory","options":{"users":{"7":"Sara (from A)"}}},
	    {"id":"dir-b","kind":"directory","options":{"users":{"7":"Sara (from B)"}}}`
	bindings := fmt.Sprintf(`{"capability":"identity.v1.user.get","cores":["%s"]}`, identityCore)
	if withCounter {
		cores += `,{"id":"counter","kind":"counter"}`
		bindings += `,{"capability":"stats.v1.messages.count","cores":["counter"]}`
	}
	return fmt.Sprintf(`{"version":1,"default_timeout_ms":500,"cores":[%s],"bindings":[%s]}`, cores, bindings)
}

func run(t *testing.T, config, script string) string {
	t.Helper()
	p, err := Build(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())
	var out bytes.Buffer
	console.Run(context.Background(), p.Host().Router, p.Host().Bus, strings.NewReader(script), &out)
	return out.String()
}

const script = "msg hi\nmsg again\n/profile\nquit\n"

// The whole point: swap the core behind a capability with a config edit only.
func TestSwapCoreByConfigOnly(t *testing.T) {
	a := run(t, cfg("dir-a", true), script)
	b := run(t, cfg("dir-b", true), script)
	if !strings.Contains(a, "Sara (from A)") || !strings.Contains(a, "messages in this chat: 2") {
		t.Fatalf("A: %q", a)
	}
	if !strings.Contains(b, "Sara (from B)") {
		t.Fatalf("B: %q", b)
	}
}

// The complement keeps working (degraded) when a capability has no provider at all.
func TestGracefulDegradation(t *testing.T) {
	out := run(t, cfg("dir-a", false), "/profile\nquit\n")
	if !strings.Contains(out, "messages in this chat: n/a") {
		t.Fatalf("got %q", out)
	}
}

func TestBadConfigFailsBeforeAnythingStarts(t *testing.T) {
	for name, doc := range map[string]string{
		"binding to an undeclared core": `{"version":1,"bindings":[{"capability":"identity.v1.user.get","cores":["ghost"]}]}`,
		"unknown kind":                  `{"version":1,"cores":[{"id":"x","kind":"does-not-exist"}]}`,
		"unknown config field":          `{"version":1,"bindngs":[]}`,
	} {
		if _, err := Build(doc); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}
