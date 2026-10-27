package config

import (
	"strings"
	"testing"
)

func TestParseValidAndSemanticErrors(t *testing.T) {
	ok := `{"version":1,"default_timeout_ms":300,
	  "cores":[{"id":"a","kind":"http","options":{"url":"http://x"}},{"id":"b","kind":"k","capabilities":["x.v1.y"]}],
	  "bindings":[{"capability":"x.v1.y","cores":["b","a"]}],
	  "serve":{"listen":":1","token_env":"T"},"extensions":{"mine":{"anything":[1,2]}}}`
	c, err := Parse(strings.NewReader(ok))
	if err != nil || len(c.Cores) != 2 || c.Bindings[0].Cores[0] != "b" || string(c.Extensions["mine"]) == "" {
		t.Fatalf("valid config rejected or misread: %v %+v", err, c)
	}
	for name, doc := range map[string]string{
		"missing version":        `{}`,
		"wrong version":          `{"version":2}`,
		"unknown field":          `{"version":1,"corez":[]}`,
		"duplicate core id":      `{"version":1,"cores":[{"id":"a","kind":"k"},{"id":"a","kind":"k"}]}`,
		"empty kind":             `{"version":1,"cores":[{"id":"a","kind":""}]}`,
		"binding to ghost":       `{"version":1,"bindings":[{"capability":"c","cores":["ghost"]}]}`,
		"binding without cores":  `{"version":1,"bindings":[{"capability":"c","cores":[]}]}`,
		"capability bound twice": `{"version":1,"cores":[{"id":"a","kind":"k"}],"bindings":[{"capability":"c","cores":["a"]},{"capability":"c","cores":["a"]}]}`,
		"trailing data":          `{"version":1} {"version":1}`,
	} {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

func TestRolesAndBindingModes(t *testing.T) {
	ok := `{"version":1,
	  "cores":[{"id":"a","kind":"k"},{"id":"b","kind":"k"},{"id":"c","kind":"k"}],
	  "roles":{"main":["a"],"sources":["b","c"]},
	  "bindings":[
	    {"capability":"x.v1.one","cores":["role:main"],"mode":"single"},
	    {"capability":"x.v1.all","cores":["role:sources","c","a"],"mode":"fanout","on_error":"fail","timeout_ms":800}]}`
	c, err := Parse(strings.NewReader(ok))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Expand(c.Bindings[0]), ","); got != "a" {
		t.Fatalf("role:main expands to %q", got)
	}
	// order kept, role expanded in place, a repeated core kept once (first wins)
	if got := strings.Join(c.Expand(c.Bindings[1]), ","); got != "b,c,a" {
		t.Fatalf("expansion = %q, want b,c,a", got)
	}
	if b := c.Bindings[1]; b.Mode != ModeFanout || b.OnError != OnErrorFail || b.TimeoutMS != 800 {
		t.Fatalf("binding fields misread: %+v", b)
	}

	cores := `"cores":[{"id":"a","kind":"k"},{"id":"b","kind":"k"}]`
	for name, doc := range map[string]string{
		"role without cores":         `{"version":1,` + cores + `,"roles":{"r":[]}}`,
		"role with undeclared core":  `{"version":1,` + cores + `,"roles":{"r":["ghost"]}}`,
		"role name with colon":       `{"version":1,` + cores + `,"roles":{"r:x":["a"]}}`,
		"empty role name":            `{"version":1,` + cores + `,"roles":{"":["a"]}}`,
		"role value not a list":      `{"version":1,` + cores + `,"roles":{"r":"a"}}`,
		"binding to undeclared role": `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["role:ghost"]}]}`,
		"unknown mode":               `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["a"],"mode":"around"}]}`,
		"single with two cores":      `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["a","b"],"mode":"single"}]}`,
		"single with two-core role":  `{"version":1,` + cores + `,"roles":{"r":["a","b"]},"bindings":[{"capability":"c","cores":["role:r"],"mode":"single"}]}`,
		"on_error without fanout":    `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["a"],"mode":"fallback","on_error":"fail"}]}`,
		"on_error without mode":      `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["a"],"on_error":"fail"}]}`,
		"bad on_error value":         `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["a"],"mode":"fanout","on_error":"ignore"}]}`,
		"negative binding timeout":   `{"version":1,` + cores + `,"bindings":[{"capability":"c","cores":["a"],"timeout_ms":-5}]}`,
	} {
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

func TestExpandWithoutRolesIsIdentity(t *testing.T) {
	c := Config{}
	if got := strings.Join(c.Expand(Binding{Cores: []string{"b", "a"}}), ","); got != "b,a" {
		t.Fatalf("got %q", got)
	}
}
