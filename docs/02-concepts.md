# 02 · Concepts and glossary

Read this once; the other pages use these words precisely.

## The mental model in one example

```
caller ──POST /v1/call {capability: "counter.v1.increment", input: {...}}──► PLATFORM
                                                                              │ 1. is it exposed? is the token right?
                                                                              │ 2. does the input satisfy the contract?
                                                                              │ 3. which core(s) serve it? (bindings, roles)
                                                                              ▼
                                                              core "counter-a" (Python, own container)
                                                                              │ 4. answer
                                                                              ▼
                                           5. does the output satisfy the contract? ──► caller gets {output: {...}}
```

The caller never learns that "counter-a" exists, what language it is written in, or whether another core served the call.

## Glossary

**Capability.** A named, versioned operation: `domain.vN.operation`, for example `counter.v1.increment` or `identity.v1.user.get`.
The name must match `^[a-z][a-z0-9]*\.v[0-9]+(\.[a-z][a-z0-9_]*)+$`. A capability is the only thing that can be called.

**Contract.** The definition of a capability, as a JSON file: input schema, output schema, the error codes it may return,
positive and negative examples, and whether repeating it is safe (`idempotent`). Contracts are the source of truth; no Go
type, Python class or TypeScript interface defines a capability. See [06 Contracts](06-contracts.md).

**Structural contracts.** The few fixed contracts that every deployment shares: the contract-file format, `core.v1.describe`,
`core.v1.health`, `gateway.v1.request`, the wire envelopes and the configuration schema. They contain no domain logic and live in
`contracts/`. Your own contracts live in a separate folder listed under `contracts_dirs`.

**Core.** A mounted program that provides capabilities. It runs wherever you like, in any language, and talks to the platform
over the wire format (HTTP+JSON or JSON over gRPC). Go code can also run *in-process* as a core. A core must answer
`core.v1.describe` (who am I, what do I serve) and `core.v1.health` (am I ok).

**Provider.** The router's word for "a core serving one capability". A capability can have several providers.

**Binding.** The configuration entry that says which providers serve a capability and how: `single`, `fallback` or `fanout`,
plus a timeout and retry budget. Without a binding, all providers of a capability are tried in mount order.

**Role.** A free label that groups cores (`"roles": {"counting": ["counter-a", "counter-b"]}`). A binding can name `role:counting` instead of
individual cores. A role is only a grouping, so replacing a core is a one-line edit. The engine knows no built-in role names.

**Wrapper.** An *around* decorator mounted on a capability from config. Code before calling `next` is "before", code after is "after";
it may change the input, skip the call, retry it, or change the output. It never edits the core it wraps.

**Gateway.** The platform's outbound boundary: a core providing `gateway.v1.request` with a domain allow-list, address blocking,
size and time limits, and secret injection. It is a role, not a locked component. Cores stay off the internet and reach out only through it.

**Platform.** The process that reads the configuration, mounts cores, enforces contracts and serves `POST /v1/call`. It also answers
`core.v1.describe` / `core.v1.health` itself, which is exactly what lets **another platform mount it as a core** (nesting).

**Host program.** The Go `main` that builds a platform. The bundled one (`engine/cmd/platform`) knows two core kinds (`http`, `gateway`); `platform-grpc` adds `grpc`.
Your own host can register more kinds, wrapper kinds and transports. See [13 Extending the host](13-extending-the-host.md).

**Complement.** Not a separate type in code: it is an ordinary core that *consumes* other capabilities and *provides* a new one
(for example `profile.v1.get`, built from `identity` and `stats`). It is how you build the product a customer sees on top of the
cores underneath.

**Facade.** The set of capabilities in `serve.expose`. Everything else is invisible from outside: an unexposed capability and a
nonexistent one both answer `404 unknown_capability`.

**App / top layer.** Whatever you build on top (frontend, backend, scripts). It only speaks the wire format, in any language, or may
not exist at all.

**Event.** A fire-and-forget message with its own contract (`kind: "event"`, a `payload` schema). Delivered best-effort to the cores
subscribed to it in config. See [10](10-extension-points-and-patterns.md#events).

**Meta.** A free string-to-string map in the request envelope. It travels with the call, into handlers and on to remote cores, and the
platform never interprets it, except for two reserved keys: `trace_id` and `depth`.

**Fan-out.** A binding mode that calls every core of a capability at once and returns all answers, each labelled by position.

**Nesting.** One platform mounted as a core of another. The inner platform exposes only its `serve.expose` list, so the outer one sees
exactly that.

## Kinds of failure

The platform distinguishes failures by what is known about delivery, because that decides whether retrying is safe:

| Code | Meaning | Safe to repeat? |
|---|---|---|
| `invalid_input` | the input violated the contract | n/a (fix the input) |
| `unauthorized` | missing or wrong token | n/a |
| `unknown_capability` | not exposed or not provided | n/a |
| `unavailable` | **nothing was processed**: could not connect, circuit open, timeout before sending, no healthy provider | always |
| `maybe_delivered` | the request **may have reached** the provider (timeout or broken connection after sending, a 5xx, a panic in-process) | only if the contract says `idempotent` |
| `internal` | anything else; details are logged, never returned | no |
| `depth_exceeded` | the call is already `max_depth` platform hops deep | n/a |
| *your codes* | domain errors declared in the contract (`limit_exceeded`, `not_found`, …) | by the domain |
