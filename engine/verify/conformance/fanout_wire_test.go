package conformance

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	structural "example.com/project/contracts"
	"example.com/project/engine/base/jsonschema"
)

// A binding with mode fanout, built from a real config through the real boot
// path, answers over the wire with one aggregate: every source is a labelled
// entry, no core id appears, failures are per source, and the aggregate
// satisfies the published wire/v1/fanout.schema.json.
func TestFanoutBindingAnswersWithAnAggregateOverTheWire(t *testing.T) {
	cfg := `{"version":1,
	  "cores":[
	    {"id":"dir-alpha","kind":"directory","capabilities":["identity.v1.user.get"],"options":{"users":{"7":"Sara"}}},
	    {"id":"dir-beta","kind":"directory","capabilities":["identity.v1.user.get"],"options":{"users":{"7":"Sarah"}}},
	    {"id":"dir-gamma","kind":"directory","capabilities":["identity.v1.user.get"],"options":{"users":{"9":"Other"}}}],
	  "roles":{"dirs":["dir-alpha","dir-beta","dir-gamma"]},
	  "bindings":[{"capability":"identity.v1.user.get","cores":["role:dirs"],"mode":"fanout","on_error":"skip","timeout_ms":2000}],
	  "serve":{"expose":["identity.v1.user.get"]}}`
	_, srv := serveConfig(t, cfg, "")
	st, body := post(t, srv.URL, "", idCall)
	if st != 200 {
		t.Fatalf("%d %s", st, body)
	}
	for _, id := range []string{"dir-alpha", "dir-beta", "dir-gamma", "dirs"} {
		if strings.Contains(body, id) {
			t.Fatalf("internal name %q leaked onto the wire: %s", id, body)
		}
	}
	var env struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(structural.FS, "wire/v1/fanout.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	sch, err := jsonschema.Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(env.Output); err != nil {
		t.Fatalf("aggregate violates wire/v1/fanout.schema.json: %v\n%s", err, body)
	}
	var agg struct {
		Results []struct {
			Source string
			Output struct{ Name string }
			Error  *struct{ Code string }
		}
	}
	if err := json.Unmarshal(env.Output, &agg); err != nil {
		t.Fatal(err)
	}
	if len(agg.Results) != 3 {
		t.Fatalf("want 3 entries (one per source), got %s", body)
	}
	if agg.Results[0].Output.Name != "Sara" || agg.Results[1].Output.Name != "Sarah" {
		t.Fatalf("answers must keep the configured order: %s", body)
	}
	if e := agg.Results[2].Error; e == nil || e.Code != "not_found" {
		t.Fatalf("the source without that user answers with its declared error, the others still answer: %s", body)
	}
}
