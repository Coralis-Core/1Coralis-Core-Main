package contract

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

const demo = `{
  "capability": "demo.v1.get", "version": "1.0.0", "kind": "request", "summary": "demo",
  "input":  {"type":"object","required":["id"],"additionalProperties":false,"properties":{"id":{"type":"integer"}}},
  "output": {"type":"object","required":["id"],"additionalProperties":false,
             "properties":{"id":{"type":"integer"},"name":{"type":"string"},"language":{"type":"string"}}},
  "errors": ["not_found"],
  "examples": [{"name":"found","input":{"id":1},"output":{"id":1,"name":"A"}},
               {"name":"missing","input":{"id":2},"error":"not_found"}],
  "invalid_inputs": [{"name":"string id","input":{"id":"1"}}]
}`

func load(t *testing.T) *Contract {
	t.Helper()
	set, err := Load(fstest.MapFS{"demo/v1/get.contract.json": {Data: []byte(demo)}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := set.Get("demo.v1.get")
	return c
}

func provider(out string) Provider {
	return func(_ context.Context, in []byte) ([]byte, string, error) {
		if string(in) == `{"id":2}` {
			return nil, "not_found", nil
		}
		return []byte(out), "", nil
	}
}

// Examples are minimum guarantees: extra OPTIONAL fields pass Check, not CheckExact.
func TestCheckAllowsExtraOptionalFieldsExactDoesNot(t *testing.T) {
	c := load(t)
	withExtra := Guard(c, provider(`{"id":1,"name":"A","language":"en"}`))
	if p := Check(context.Background(), c, withExtra); len(p) != 0 {
		t.Fatalf("Check should accept extra optional field: %v", p)
	}
	if p := CheckExact(context.Background(), c, withExtra); len(p) == 0 {
		t.Fatal("CheckExact must reject an extra field")
	}
}

func TestCheckStillCatchesMissingWrongAndInvalid(t *testing.T) {
	c := load(t)
	for name, out := range map[string]string{
		"missing field":    `{"id":1}`,
		"wrong value":      `{"id":1,"name":"B"}`,
		"schema violation": `{"id":"1","name":"A"}`,
		"unknown property": `{"id":1,"name":"A","extra":1}`,
	} {
		if p := Check(context.Background(), c, Guard(c, provider(out))); len(p) == 0 {
			t.Errorf("%s: not detected", name)
		}
	}
	// a provider that does not validate input AND is not behind Guard accepts invalid input
	lax := provider(`{"id":1,"name":"A"}`)
	if p := Check(context.Background(), c, lax); len(p) == 0 {
		t.Fatal("accepting a forbidden input must be reported")
	}
}

func TestContainsRecursive(t *testing.T) {
	if !jsonContains([]byte(`{"a":{"b":1,"c":2},"d":[1,2]}`), []byte(`{"a":{"b":1},"d":[1,2]}`)) {
		t.Fatal("nested subset should match")
	}
	if jsonContains([]byte(`{"a":{"b":1}}`), []byte(`{"a":{"b":2}}`)) || jsonContains([]byte(`{"d":[1,2,3]}`), []byte(`{"d":[1,2]}`)) {
		t.Fatal("different values / arrays must not match")
	}
}

func TestSchemaMatchExamplesAcceptAnyConformingOutput(t *testing.T) {
	doc := `{"capability":"demo.v1.who","version":"1.0.0","kind":"request","summary":"s",
	  "input":{"type":"object","additionalProperties":false},
	  "output":{"type":"object","required":["name"],"additionalProperties":false,"properties":{"name":{"type":"string"}}},
	  "examples":[{"name":"any","input":{},"output":{"name":"sample"},"match":"schema"}]}`
	set, err := Load(fstest.MapFS{"demo/v1/who.contract.json": {Data: []byte(doc)}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := set.Get("demo.v1.who")
	ok := func(context.Context, []byte) ([]byte, string, error) {
		return []byte(`{"name":"something else"}`), "", nil
	}
	if p := Check(context.Background(), c, ok); len(p) != 0 {
		t.Fatalf("a different but schema-valid output must pass: %v", p)
	}
	bad := func(context.Context, []byte) ([]byte, string, error) { return []byte(`{"name":5}`), "", nil }
	if p := Check(context.Background(), c, bad); len(p) == 0 {
		t.Fatal("a schema-invalid output must still fail")
	}
	// an unknown match mode and schema+error are rejected at load time
	for _, ex := range []string{
		`{"name":"x","input":{},"output":{"name":"a"},"match":"fuzzy"}`,
		`{"name":"x","input":{},"error":"nope","match":"schema"}`,
	} {
		d := strings.Replace(doc, `{"name":"any","input":{},"output":{"name":"sample"},"match":"schema"}`, ex, 1)
		d = strings.Replace(d, `"examples":`, `"errors":["nope"],"examples":`, 1)
		if _, err := Load(fstest.MapFS{"demo/v1/who.contract.json": {Data: []byte(d)}}); err == nil {
			t.Errorf("must reject %s", ex)
		}
	}
}
