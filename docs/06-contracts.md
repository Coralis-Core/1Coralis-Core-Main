# 06 · Contracts

A contract is the complete, language-independent definition of one capability. The contract files are the source of truth: no Go
struct, Python class or TypeScript interface defines a capability. A core, a caller, a wrapper and the platform's own guard all read the same file.

## A request contract

`domain-contracts/<domain>/v1/<operation>.contract.json`:

```json
{
  "$schema": "../../../contracts/contract.schema.json",
  "capability": "counter.v1.increment",
  "version": "1.0.0",
  "kind": "request",
  "summary": "Add to a named counter (by 1 unless 'by' is given) and return its new value. A counter cannot exceed 1000.",
  "input": {
    "type": "object",
    "required": ["name"],
    "additionalProperties": false,
    "properties": {
      "name": {"type": "string", "minLength": 1},
      "by":   {"type": "integer", "minimum": 1}
    }
  },
  "output": {
    "type": "object",
    "required": ["value"],
    "additionalProperties": false,
    "properties": {"value": {"type": "integer", "minimum": 1}}
  },
  "errors": ["limit_exceeded"],
  "examples": [
    {"name": "first", "input": {"name": "visits"}, "output": {"value": 1}, "match": "schema"}
  ],
  "invalid_inputs": [
    {"name": "missing_name", "input": {}},
    {"name": "zero_step", "input": {"name": "visits", "by": 0}}
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `$schema` | no | editor help only |
| `capability` | yes | the name, `domain.vN.operation` ([naming](#naming-and-versioning)) |
| `version` | yes | semver; its major number must equal the `vN` in the name |
| `kind` | yes | `request` (a call with an answer) or `event` ([events](#event-contracts)) |
| `summary` | yes | one sentence a human can read |
| `input`, `output` | for requests | JSON Schema ([subset](#the-json-schema-subset)) |
| `errors` | no | domain error codes this capability may return |
| `idempotent` | no | `true` if calling it again after it may have been processed is harmless ([09](09-routing-and-resilience.md#retry-safety)); default `false` |
| `examples` | yes | at least one; executable ([below](#examples-are-tests)) |
| `invalid_inputs` | no | inputs that **must** be rejected |

## The JSON Schema subset

Contracts use a deliberately small, documented subset of JSON Schema so that every language can implement and verify it cheaply.

Supported: `type` (a single type name: `object`, `array`, `string`, `integer`, `number`, `boolean`, `null`), `properties`, `required`,
`additionalProperties` (boolean), `enum`, `items`, `minLength`, `maxLength`, `minimum`, `maximum`, `minItems`.
Annotation keywords (`title`, `description`, `$comment`, `examples`, `default`, `$schema`) are accepted and ignored.
**Any other keyword makes the contract fail to load** (so a schema never silently means less than it says). The validator is in `engine/base/jsonschema`.

Advice: set `additionalProperties: false` on every object (typos in field names become errors), mark required fields explicitly, and bound strings
and numbers (`maxLength`, `minimum`, `maximum`) so a contract also limits abuse.

## Domain errors

`"errors": ["limit_exceeded"]` declares the codes the capability may return in addition to the generic ones
(`invalid_input`, `unauthorized`, `unknown_capability`, `unavailable`, `maybe_delivered`, `internal`, `no_provider`, `depth_exceeded`, `fanout_only`, `not_fanout`).
At the boundary the guard lets only declared or generic codes through; any other code becomes `internal` and is logged as a contract violation.
Over HTTP a domain error is status 422.

## Examples are tests

* `examples[]`: `{"name", "input", "output"}` for a success, or `{"name", "input", "error": "code"}` for an error example.
  By default an example's `output` is a **minimum guarantee**: the provider's output must contain every listed field with an equal value, and may add
  other optional fields allowed by the schema (this is what lets different cores satisfy one contract). `"match": "schema"` says "only validate the output against
  the schema" (use it for non-deterministic answers); it cannot be combined with an error example. `contract.CheckExact` demands exact equality.
* `invalid_inputs[]`: `{"name", "input"}`; a provider must reject each of them.

`contract.Check(ctx, contract, provider)` runs all of them and returns the list of problems (empty = conformant). A provider is any function from JSON in to JSON (or code) out,
so the same check works for a Go function, a Python process behind HTTP, or anything else you wrap.

## Event contracts

```json
{
  "capability": "orders.v1.placed", "version": "1.0.0", "kind": "event",
  "summary": "An order was placed.",
  "payload": {"type": "object", "required": ["order_id"], "additionalProperties": false, "properties": {"order_id": {"type": "integer"}}},
  "examples": [{"name": "basic", "payload": {"order_id": 7}}],
  "invalid_payloads": [{"name": "missing_id", "payload": {}}]
}
```

An event has a `payload` (not `input`/`output`), examples carry a `payload`, negative examples are `invalid_payloads`.
`contract.CheckEmitted` verifies what a publisher actually emits; `contract.CheckSubscriber` verifies that a subscriber accepts every valid example and rejects every invalid one.

## Naming and versioning

* Name: `domain.vN.operation` or deeper (`identity.v1.user.get`). It must match `^[a-z][a-z0-9]*\.v[0-9]+(\.[a-z][a-z0-9_]*)+$`.
* **In a `v1` contract only optional things may be added** (a new optional input field, a new optional output field, a new error code a caller can ignore).
  Any other change (renaming, making a field required, changing a type, removing something) is a **new `v2` contract file**. Both can be served side by side during a migration.
* Capability names are unique across all loaded folders; defining one twice is a load error.

## The structural contracts

Fixed, shipped in `contracts/` and embedded in the binary. They contain no domain logic.

| File | What it fixes |
|---|---|
| `contract.schema.json` | the format of every contract file (this page) |
| `core/v1/describe` | the answer to "who are you": `name`, `version`, `capabilities`, `publishes`, `subscribes` |
| `core/v1/health` | `status` (`ok`/`degraded`/`down`) and optional `detail` |
| `gateway/v1/request` | outbound HTTP through the gateway ([10](10-extension-points-and-patterns.md#gateway)) |
| `wire/v1/*` | request, response and fan-out envelopes |
| `config/v1/*` | the schema of `platform.json` |

Your own contracts live elsewhere (`domain-contracts/` by convention) and are added with `contracts_dirs` in the config ([07](07-configuration.md)). The `-contracts DIR` flag of the
platform binary replaces the **embedded** structural set with a folder (useful when you work on the structural contracts themselves).

## Loading and the guard

`contract.LoadAll(fs...)` merges several file systems (`fs.FS`) into one set and self-checks every file: valid JSON, schemas compile (no unsupported keywords),
the version's major matches the name, examples satisfy their own schemas, negative examples violate them, no duplicates.
`contract.Guard(contract, provider)` wraps any provider so that input is validated before the provider sees it, output before the caller sees it, and undeclared error codes
cannot escape. Every transport serves providers through `Guard`; that is why a core cannot opt out. (Go's own JSON decoder is case-insensitive and ignores missing fields, so
the schema is deliberately the only judge.)

## Writing good contracts: a checklist

1. One capability = one verb the caller cares about; name it from the caller's side.
2. Closed objects (`additionalProperties: false`), explicit `required`, bounded strings and numbers.
3. Declare every domain error; do not reuse generic codes for domain situations.
4. Say `idempotent: true` only when a repeated call changes nothing further.
5. At least one success example, one error example per declared error, and negative inputs for each constraint.
6. Never put secrets or personal data in examples.
