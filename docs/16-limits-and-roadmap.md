# 16 · Limits and roadmap

An honest list. Nothing here is hidden elsewhere in the docs.

## Maturity

Alpha. The engine, both transports, the adapters and the contracts are built and tested. The platform has **not yet been run end to end against a real third-party program**; that is the
most valuable next step because it is where unknown unknowns live. Items marked *unverified* below were written but could not be run by the author ([14](14-testing-and-verification.md#what-has-been-verified-honestly)).

## Limits by design

* **Control plane, not engine**: it cannot change a program's internals, only its boundary.
* **Not multi-tenant**; tenancy, if any, is a per-project layer on top.
* **Synchronous request/response**: no streaming, no long-lived sessions, no transactions across cores, no durable workflows, no built-in queue or persistence.
* **Contracts are a JSON Schema subset**: no `oneOf`/`anyOf`/`$ref`/`pattern`/`format`; model variants with optional fields or separate capabilities.
* **Events are best effort** with an in-process bus; a subscriber that is down misses the event.
* **No live config reload**; restart to change anything.

## Current limits (could change)

* **One bearer token** guards the whole exposed facade; no per-client tokens, no per-capability ACL, no rate limiting (use a wrapper or a front proxy).
* **The bundled `platform` binary knows two core kinds** (`http`, `gateway`). The `platform-grpc` binary adds `grpc` and a gRPC listener; in-process cores and wrapper kinds need a small Go host program ([13](13-extending-the-host.md)).
* **The Docker image builds the generic `platform` only**; a gRPC image needs a second Dockerfile (the module needs the internet to build).
* **gRPC**: unary only, no streaming, plaintext by default (`"tls": true` for TLS), no client-certificate mTLS; the module's `go.sum` still has to be generated (*unverified*: run `scripts/test-grpc.sh`).
* **Adapters** exist for Python and Node only. Other languages implement the protocol directly (it is small, [05](05-writing-cores.md#implementing-the-protocol-yourself)).
* **Typed in-process calls skip contract validation** (same process); transports and remote mounts are validated.
* **Docker and CI**: written and structurally validated; `docker build`, `docker compose up` and the first CI run were not executed by the author (*unverified*).
* **Health** is informational: routing uses the circuit breaker, not the health status.
* **No metrics endpoint** and no tracing export; logs and `trace_id` only.

## Deliberately deferred

Lazy start of in-process cores, a WASM sandbox for untrusted cores, state shared across platform instances, and per-capability access control are designed for (the config leaves room) but not built.

## Ideas for next steps

1. Run the platform against a real third-party program and fix what that reveals.
2. Per-client tokens and per-capability ACL (a small, optional layer).
3. A metrics endpoint (call counts, latencies, breaker state) and trace export.
4. A persistent, optional event queue as a core, keeping the bus simple.
5. More adapters (Go and Java/Kotlin, Rust) and a conformance kit usable from any language.
6. Live config reload with validation before swap.

## Contributing a change safely

1. Write or update the **contract** first; add examples and negative examples.
2. Keep the dependency rules ([03](03-architecture.md#dependency-rules)); `go test ./engine/verify/archtest` enforces them.
3. Add a conformance test when behavior crosses a boundary.
4. Run `go run ./scripts/verify` before and after; keep `STATUS.md` honest.
