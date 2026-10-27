# 03 · Architecture

## The shape

```
          callers (your app, curl, scripts, another platform)
                         │  HTTP+JSON  or  gRPC(JSON)
        ┌────────────────▼───────────────────────────────────────────────┐
        │ PLATFORM                                                       │
        │                                                                │
        │  edge        token · size cap · strict envelope · expose list  │
        │   │                                                            │
        │  guard       input checked against the contract                │
        │   │                                                            │
        │  router      outer wrappers → provider chain (bindings, roles) │
        │   │          timeout · circuit breaker · retries · fallback    │
        │   │          inner wrappers → provider                         │
        │  guard       output checked against the contract               │
        │                                                                │
        │  bus (events) · health · config · contracts                    │
        └───────┬─────────────────┬──────────────────┬───────────────────┘
                │ http            │ grpc             │ in-process / gateway
          core A (any lang)  core B (any lang)  Go core · gateway core ──► allowed internet only
```

Three properties follow from this shape:

* the edge and the guard are the **same code for every capability**, so no core can opt out of validation or exposure rules;
* a core is reached only through a **transport adapter**, so adding a language or a transport never touches the router;
* the platform **is itself a core** (it answers `core.v1.describe` and `core.v1.health`), so platforms nest.

## Packages

| Package | Responsibility |
|---|---|
| `engine/base/config` | parse (strict: unknown keys are errors), validate, defaults (`defaults.go` is the one place defaults live) |
| `engine/base/contract` | load contract files from any `fs.FS`, self-check them, `Guard` (enforce at a boundary), `Check*` (conformance of a provider against its examples) |
| `engine/base/jsonschema` | the validator for the documented JSON Schema subset ([06](06-contracts.md#the-json-schema-subset)) |
| `engine/base/capability` | the `Router`: providers, bindings, per-capability policy, resilience, wrappers, fan-out; `Meta`; coded errors; the typed Go API (`Define`, `Provide`, `Call`, `CallAll`) |
| `engine/base/bus` | in-process publish/subscribe (synchronous, ordered, panic-isolated) |
| `engine/base/kernel` | `Core` interface, `Host` (router, bus, logger), start in order / stop in reverse, startup checks |
| `engine/base/gateway` | the outbound core (`gateway.v1.request`) |
| `engine/base/boot` | builds a platform from a config; mounts remote cores; HTTP handler, events, health; `Options` for extensions |
| `engine/base/transports/httpjson` | the HTTP server and client for the wire format |
| `engine/contrib/grpc` | JSON-over-gRPC transport and the `platform-grpc` binary (HTTP + gRPC); a **separate Go module** so the base stays dependency-free |
| `engine/cmd/platform` | the bundled host program (core kinds `http` and `gateway`) |
| `engine/verify` | conformance tests, the architecture test, fixtures |

The main module has **no third-party dependencies** (`go.mod` contains none).

## Dependency rules

Enforced by `engine/verify/archtest` (the test injects forbidden imports to prove it would catch them):

| Code in… | may import |
|---|---|
| `engine/base` | itself and the standard library only |
| `contracts` | `engine/base` |
| `engine/cmd` | `engine/base`, `contracts` |
| `cores/<x>` | `engine/base`, `contracts`, itself (never another core, never `app`) |
| `app/<dir>` | `engine/base`, `contracts`, itself (never `cores`, never another app folder) |
| `app/main.go` (the one composition root, if you have one) | everything except `engine/verify`: it wires cores and app parts together |
| anything else (`examples/`, `scripts/`) | `engine/base`, `contracts`, itself |
| `engine/verify` | everything (it is tooling) |

A second rule keeps the base generic: **no specific third-party program, library or platform name may appear in `engine/base`**
(the test has a ban list), and the base has no chat-shaped types. Directories starting with `_` and nested modules (their own `go.mod`)
are outside the graph, which is how `engine/contrib/grpc` stays separate.

## Life of a call

For `POST /v1/call` (the gRPC transport runs the same steps):

1. **Method**: anything but `POST` → `405 invalid_input`.
2. **Token**: if `serve.token_env` is set, compare the bearer token in constant time → `401 unauthorized`.
3. **Body cap**: read at most `MaxBody` bytes (default 1 MiB) → `413 invalid_input`.
4. **Envelope**: strict decode of `{capability, input, meta?}`; unknown fields or a missing `input` → `400 invalid_input`.
5. **Exposure**: the capability must be in `serve.expose` (or `"*"`), else `404 unknown_capability`. The same answer is given for a
   capability that does not exist, so callers cannot probe what is behind the platform.
6. **Contract lookup**: it must have a request contract, else `404`.
7. **Fan-out?** If the binding's mode is `fanout`, a separate path runs every source and builds the aggregate answer ([09](09-routing-and-resilience.md#fan-out)).
8. **Meta**: `trace_id` is minted if absent; `depth` is incremented; a call already at `max_depth` is refused → `508 depth_exceeded`.
9. **Guard, in**: the input is validated against the contract's input schema → `400 invalid_input`. The core never sees an invalid input.
10. **Router**: outer wrappers run; then the provider chain is walked (timeout, breaker, retries, fallback); each provider call is wrapped by the
    inner wrappers; the core answers (a remote core is called over its transport with the same two checks on its side).
11. **Guard, out**: the output is validated against the contract's output schema; an error code the contract does not declare (and that is not
    one of the generic codes) is turned into `internal`. A core that breaks its contract is logged, and the caller gets `500 internal` with no detail.
12. **Respond**: `200 {"output": …}` or `{"error": {"code": …}}` with the status from the table in [08](08-wire-protocols.md#http-status-codes).

Because the guard runs *outside* the router, wrappers see only valid inputs, and a wrapper's output is checked too.

## Life of the process

**Build** (`boot.Build`, mounts, starts nothing):

1. Parse and validate the config; fill defaults ([07](07-configuration.md#validation-rules) lists every rule). Duplicate ids, a role nobody plays, a binding to an
   undeclared core, `single` mode with several cores, and similar mistakes are errors **before anything runs**.
2. Load contracts: the structural ones plus every folder in `contracts_dirs` (relative paths resolve against the config file). Each file is
   self-checked; a capability defined twice, a version that does not match the major in its name, or a schema using an unsupported keyword is an error.
3. Mount each core in config order, by `kind`: `http`, `gateway`, a registered remote factory (for example `grpc`), or a registered in-process
   factory. An unknown kind, or a factory that returns a core with a different name than the config says, is an error.
4. Mount wrappers (unknown wrapper kind or contract = error), then event subscriptions (a subscriber that cannot receive events = error),
   then the platform's own `describe`/`health` core.

**Start** (`Platform.Start`):

1. Start cores in mount order (a remote core asks `core.v1.describe`; if you listed `capabilities` and the core is not up yet, a warning is logged and
   the config is trusted).
2. `Router.Validate`: every binding must point at cores that actually provide that capability.
3. Every provided capability must have a *request* contract.
4. Every name in `serve.expose` must be provided by something (or be an event contract). A typo here stops the start.
5. Event subscriptions are attached to the bus.

If any step fails, everything already started is stopped and the error is returned. The design goal: **a configuration mistake fails at startup, not at 3 a.m.**

**Run**: the HTTP server serves `/v1/call`, `/v1/event`, `/livez`, `/healthz`.

**Stop**: on `SIGINT`/`SIGTERM` the server shuts down gracefully (5 s), then cores stop in **reverse** start order.

## Nesting and the complement

A platform answers `core.v1.describe` (listing only its *exposed* capabilities) and `core.v1.health`, so an outer platform can mount it with
`kind: "http"` like any core. Inner platforms are therefore separate trust and failure domains, and the `depth` meta key stops cycles.

A **complement** is an ordinary core that calls other capabilities and provides new ones. In Go you write it with the typed API and call the
router directly; in any other language you call the platform over the wire. The base has no concept of "the customer-facing layer": it is
whatever you expose.

## Design decisions and their trade-offs

| # | Decision | Why | Cost / trade-off |
|---|---|---|---|
| 1 | Contracts are **JSON files**, not code | any language reads the same source of truth; reviewable; versionable | schemas are limited to a small subset of JSON Schema |
| 2 | The platform is a **control plane**, not an engine | you keep using the real program; upgrades stay trivial | cannot change a program's internals |
| 3 | **Both sides** of every hop validate the contract | a drifting core is caught at the boundary, not in production data | a validation per hop (microseconds); in-process typed calls between Go cores skip it (they share types and a process) |
| 4 | An **adapter is a language binding**, not a wrapper for one system | one small adapter per language serves every core in that language | each language needs its own adapter |
| 5 | **Secrets are env-var names** in config | config can be committed and shared | the operator must provide the variables; an empty one is a startup error for `serve.token_env` |
| 6 | **Nothing is exposed** until listed | a new capability never leaks by accident | one more line of config per capability |
| 7 | **Protections are off until you turn them on** (`max_depth`, retries) | no surprising behavior in simple setups | you must remember to enable them for nesting or flaky cores |
| 8 | A wrapper around a fan-out can only **gate** it | per-source results are a map, not one document; rewriting them silently would be wrong | aggregation logic belongs in a complement core |
| 9 | A panic in an in-process core is `maybe_delivered` | the work may have partly happened | non-idempotent calls are not retried after a panic |
| 10 | **Retry safety comes from the contract** (`idempotent`) | the platform cannot guess whether a repeat is harmless | you must declare it |
| 11 | Default timeout **10 s**, retries **0** | predictable, conservative | slow cores need `timeout_ms` |
| 12 | Roles are **free labels** | no vocabulary baked into the engine | no built-in meaning; you define it |
| 13 | **Not multi-tenant** | most deliveries are single-customer; tenancy differs per project | multi-tenant needs a layer you write on top |
| 14 | HTTP is **one transport among several**; others plug in through `boot.Options` | add gRPC (or anything) without touching the base | the bundled binary only knows `http` and `gateway` |
| 15 | **Events are best effort** | simple and fast; no broker to run | a subscriber that is down misses the event |
| 16 | **No live config reload** | one clear state per process; failures happen at start | restart to change config |

## Performance

Measured on a single-CPU sandbox with `TestNestingLatencyReport` (platforms nested over HTTP on loopback, a trivial core at the bottom;
this measures the platform's own overhead, not a network):

| Nesting depth | Latency per call |
|---|---|
| 0 (in-process call) | ≈ 1.6 µs |
| 1 | ≈ 72 µs |
| 2 | ≈ 105 µs |
| 4 | ≈ 222 µs |
| 8 | ≈ 424 µs |

Each additional HTTP hop costs roughly 50 µs of platform overhead on loopback. Real networks, TLS and your cores' own work dominate in practice.
Rerun the numbers with `go test -run TestNestingLatencyReport -v ./engine/verify/conformance`.
