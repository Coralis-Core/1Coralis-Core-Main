package jsonschema

import (
	"strings"
	"testing"
)

func mustCompile(t *testing.T, s string) *Schema {
	t.Helper()
	sc, err := Compile([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestObjectRequiredAdditionalAndTypes(t *testing.T) {
	sc := mustCompile(t, `{"type":"object","required":["id"],"additionalProperties":false,
	  "properties":{"id":{"type":"integer","minimum":1},"name":{"type":"string","minLength":1,"maxLength":5}}}`)
	ok := []string{`{"id":7}`, `{"id":7.0}`, `{"id":1,"name":"Sara"}`}
	for _, d := range ok {
		if err := sc.Validate([]byte(d)); err != nil {
			t.Errorf("%s should be valid: %v", d, err)
		}
	}
	bad := map[string]string{
		`{}`:                        "missing required property \"id\"",
		`{"id":7.5}`:                "expected integer",
		`{"id":"7"}`:                "expected integer",
		`{"id":0}`:                  "below minimum",
		`{"id":1,"extra":true}`:     "unexpected property",
		`{"id":1,"name":""}`:        "shorter than minLength",
		`{"id":1,"name":"toolong"}`: "longer than maxLength",
		`[1]`:                       "expected object",
		`{"id":1} {"id":2}`:         "trailing data",
		`{"id":`:                    "invalid JSON",
	}
	for d, want := range bad {
		err := sc.Validate([]byte(d))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want error containing %q, got %v", d, want, err)
		}
	}
}

func TestArrayEnumNested(t *testing.T) {
	sc := mustCompile(t, `{"type":"object","properties":{
	  "tags":{"type":"array","minItems":1,"items":{"type":"string","enum":["a","b"]}},
	  "n":{"type":"number","enum":[1,2.5]}}}`)
	if err := sc.Validate([]byte(`{"tags":["a","b"],"n":2.5}`)); err != nil {
		t.Fatal(err)
	}
	for d, want := range map[string]string{
		`{"tags":[]}`:      "at least 1",
		`{"tags":["c"]}`:   "/tags/0: value not in enum",
		`{"n":3}`:          "not in enum",
		`{"tags":["a",1]}`: "/tags/1: expected string",
	} {
		if err := sc.Validate([]byte(d)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q got %v", d, want, err)
		}
	}
}

func TestUnsupportedKeywordsRejectedAtCompile(t *testing.T) {
	for _, s := range []string{
		`{"type":"string","pattern":"^a"}`,
		`{"oneOf":[{"type":"string"}]}`,
		`{"type":["string","null"]}`,
		`{"properties":{"x":{"$ref":"#/x"}}}`,
		`{"enum":[{"a":1}]}`,
	} {
		if _, err := Compile([]byte(s)); err == nil {
			t.Errorf("should reject: %s", s)
		}
	}
	// annotations are fine
	if _, err := Compile([]byte(`{"$schema":"x","title":"t","description":"d","type":"string"}`)); err != nil {
		t.Fatal(err)
	}
}
