package contracts_test

import (
	"io/fs"
	"testing"

	"example.com/project/contracts"
	"example.com/project/engine/base/contract"
	"example.com/project/engine/base/jsonschema"
)

// The structural contracts are small, fixed and must always load and self-check.
func TestStructuralContractsLoad(t *testing.T) {
	set, err := contract.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	got := set.Names()
	// core.v1.* are what every core answers; gateway.v1.request is the contract
	// of the built-in gateway role (the outbound boundary).
	if len(got) != 3 || got[0] != "core.v1.describe" || got[1] != "core.v1.health" || got[2] != "gateway.v1.request" {
		t.Fatalf("structural capability contracts: %v", got)
	}
}

// Wire and config schemas must stay inside the supported schema subset, so
// every language can implement them cheaply.
func TestStructuralSchemasStayInsideTheSubset(t *testing.T) {
	for _, path := range []string{"wire/v1/request.schema.json", "wire/v1/response.schema.json", "wire/v1/fanout.schema.json", "config/v1/platform.config.schema.json"} {
		raw, err := fs.ReadFile(contracts.FS, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jsonschema.Compile(raw); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}
