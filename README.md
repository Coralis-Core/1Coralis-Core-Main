# Core-Centric Composable Platform

*(working title)*

A small, language-independent **control plane**. It mounts independent programs ("cores"), lets them call each other only through
versioned **contracts**, and exposes one controlled facade to the outside. It sits *in front of* existing software instead of replacing it,
so you can customize and combine programs **without forking them**, keep every piece isolated, and swap any piece by editing one line of configuration.

> **Status: alpha.** The Go engine, the HTTP and gRPC transports, the Python and Node adapters and the contracts are built and tested.
> See [Verification status](#verification-status) for exactly what was and was not run. Full documentation is in [`docs/`](docs/README.md).

---

## Contents

1. [What it is, and what it is not](#1-what-it-is-and-what-it-is-not)
2. [Core ideas](#2-core-ideas)
3. [Architecture](#3-architecture)
4. [How cores connect](#4-how-cores-connect)
5. [Repository layout](#5-repository-layout)
6. [Quick start](#6-quick-start)
7. [Configuration at a glance](#7-configuration-at-a-glance)
8. [The wire protocol at a glance](#8-the-wire-protocol-at-a-glance)
9. [Security](#9-security)
10. [Deployment](#10-deployment)
11. [Testing and verification](#11-testing-and-verification)
12. [Documentation](#12-documentation)
13. [Verification status](#verification-status)
14. [Known limits](#known-limits)
15. [License](#license)

---

## 1. What it is, and what it is not

**It is** a *control plane*: one process that reads one configuration file, mounts the cores it lists, checks every call against a
contract on the way in and on the way out, and serves the result over a single wire format. A core can be written in **any language** and
run anywhere; the platform only needs it to speak that wire format (HTTP+JSON, or JSON over gRPC).

**Typical uses**

* Put a controlled facade in front of a program that has no plugin system, and change or extend its behavior from outside instead of forking it.
* Combine several programs, written in different languages, behind one API with one authentication point and one place to swap implementations.
* Give a customer exactly the capabilities you choose, while the pieces underneath stay hidden and replaceable.
* Control and audit all outbound internet access from one place.
* Mount one platform inside another (nesting), with loop protection.

**It is not**

* **Not an engine.** It wraps programs; it does not replace them or run inside them.
* **Not multi-tenant.** Tenancy, when a project needs it, is decided per project in the top layer.
* **Not a substitute for forking** when you must change a program's internals. It works when the change can be expressed at the boundary.
* **Not a service mesh, queue, database or workflow engine.** No discovery, streaming, durable workflows or persistence.

---

## 2. Core ideas

| Term | Meaning |
|---|---|
| **Core** | A mounted program that provides capabilities. Any language, any technology, usually its own process or container. |
| **Capability** | A named operation, `domain.v1.operation`. The only thing that can be called. |
| **Contract** | A language-independent JSON file defining a capability: input and output (JSON Schema), error codes, examples and negative examples. The files are the source of truth, not code types. |
| **Binding** | Says which core(s) serve a capability and how: `single`, `fallback` (first healthy) or `fanout` (all at once). |
| **Role** | A free label grouping cores. Bindings can name `role:<label>`, so swapping a core is a one-line edit. The engine knows no fixed role names. |
| **Wrapper** | A decorator (before / after / around) mounted on a capability without editing any core. |
| **Gateway** | The controlled path for outbound internet calls: allow-list, blocked private addresses, size and time limits, secret injection. Itself a replaceable role. |
| **Platform** | The process in this repository. It serves `POST /v1/call`, and can itself be mounted by another platform as a core. |
| **Complement / app** | Whatever you add on top. A complement is an ordinary core that calls other capabilities and provides new ones. Optional. |
| **Event** | A fire-and-forget message with its own contract, delivered best-effort to subscribed cores. |

Principles that are fixed: contracts are JSON files; both sides of every hop validate them; secrets are only ever *names* of environment
variables in config; nothing is exposed until listed; protections such as nesting limits are explicit; an outer wrapper on a fan-out can only gate it; a panic in an
in-process core is reported as "may have been processed"; the base contains no program-, vendor- or domain-specific code (enforced by a test).
Details and trade-offs: [docs/03](docs/03-architecture.md#design-decisions-and-their-trade-offs).

---

## 3. Architecture

```
                         ┌──────────────────────────────────────────────┐
  callers                │                  PLATFORM                    │
 (your app, curl,        │                                              │
  another platform) ───► │  POST /v1/call   POST /v1/event   /livez     │
        HTTP+JSON        │       │                                      │
        or gRPC          │  ┌────▼─────┐  expose allow-list, token,     │
                         │  │  edge    │  strict envelope, depth        │
                         │  └────┬─────┘                                │
                         │  ┌────▼─────┐  contract check (input)        │
                         │  │  guard   │                                │
                         │  └────┬─────┘                                │
                         │  ┌────▼─────┐  bindings: single│fallback│    │
                         │  │  router  │  fanout, roles, timeouts,      │
                         │  │          │  retries, circuit breaker,     │
                         │  └──┬───┬───┘  wrappers (outer / inner)      │
                         │     │   │                                    │
                         │  contract check (output) on the way back     │
                         └─────┼───┼────────────────────────────────────┘
                               │   │
                       ┌───────▼┐ ┌▼───────┐ ┌───────────────┐
                       │ core A │ │ core B │ │ gateway core  │──► allowed internet only
                       │ (any   │ │ (any   │ │ (in platform) │
                       │  lang) │ │  lang) │ └───────────────┘
                       └────────┘ └────────┘   (each core on its own isolated network in Docker)
```

Packages (`engine/base/…`): `config`, `contract`, `jsonschema`, `capability` (router), `bus`, `kernel`, `gateway`, `boot`, `transports/httpjson`;
gRPC is a separate Go module (`engine/contrib/grpc`). The main module has **no third-party dependencies**. A walk-through of a call, startup and shutdown is in
[docs/03](docs/03-architecture.md#life-of-a-call).

---

## 4. How cores connect

A core is any program that speaks the platform's wire format. There are three ways to connect one; the contract is identical in all of them.

### 4.1 With an adapter (Python, Node)

An adapter is one dependency-free file that implements the protocol. You register handlers; it does the HTTP, the token check, `core.v1.describe`,
`core.v1.health` and the error mapping.

```python
import sys
sys.path.insert(0, "adapters/python")
from platform_adapter import Core, CoreError

core = Core("my-core", token_env="MY_CORE_TOKEN")        # the NAME of the env var that holds this core's token

@core.capability("domain.v1.operation")
def operation(inp, meta):                                 # inp has already been validated against the contract
    if inp["value"] > 100:
        raise CoreError("too_large", "value is above the limit")   # a domain error declared in the contract
    return {"result": inp["value"] * 2}

core.serve(host="127.0.0.1", port=9001)
```

```js
const { Core, CoreError } = require('./adapters/node/platform_adapter.js');
const core = new Core('my-core', { tokenEnv: 'MY_CORE_TOKEN' });
core.capability('domain.v1.operation', (inp) => ({ result: inp.value * 2 }));
core.serve(9001, '127.0.0.1');
```

### 4.2 By implementing the protocol (any language)

Serve `POST /v1/call` with `{"capability", "input", "meta"}` and answer `{"output": …}` or `{"error": {"code": …}}`; answer the two structural capabilities `core.v1.describe`
(who you are and what you provide) and `core.v1.health`; require the bearer token if one is configured. That is the whole protocol ([docs/05](docs/05-writing-cores.md#implementing-the-protocol-yourself)).
The same message format is available as JSON over gRPC ([docs/08](docs/08-wire-protocols.md#grpc)).

### 4.3 In-process (Go)

A Go core registers providers with the typed API and runs inside the platform process, registered by a small host program
([docs/13](docs/13-extending-the-host.md)).

### 4.4 Define it, then mount it

1. **Write the contract** (a JSON file in a folder listed under `contracts_dirs`): name, input and output schemas, error codes, examples ([docs/06](docs/06-contracts.md)).
2. **Mount the core** in the config:

   ```json
   { "id": "my-core", "kind": "http",
     "options": { "url": "http://127.0.0.1:9001", "token_env": "MY_CORE_TOKEN", "timeout_ms": 3000 } }
   ```

   `capabilities` may be omitted: the platform asks the core with `core.v1.describe`.
3. **Expose** what callers may use: `"serve": { "expose": ["domain.v1.operation"] }`.
4. Optionally add **bindings** (fallback, fan-out, roles), **wrappers**, **events**, a **gateway**.

What the platform guarantees about every connected core: inputs are validated against the contract before the core sees them; outputs before a caller sees them; a failure is classified as
`unavailable` (nothing was processed, always safe to retry) or `maybe_delivered` (it may have been processed; retried only if the contract declares `idempotent`); and the core is reachable
only through the facade you expose. A complete, tested walk-through of building a first core is in [docs/04](docs/04-getting-started.md#3-build-your-first-core-about-ten-minutes); the general method for any existing program ("program X") is in [docs/17](docs/17-integrating-a-program.md).

---

## 5. Repository layout

```
.
├── cores/                  one folder per core (any language). Intentionally empty: project-1/, project-2/
├── app/                    whatever you add on top (frontend, backend, the product). Optional.
├── config/
│   └── platform.json       the mounting point: what is mounted, who serves what, what is exposed
├── contracts/              the fixed STRUCTURAL contracts (embedded in the binary)
├── domain-contracts/       your own contracts (listed in config as contracts_dirs); ships a small neutral demo set, counter/v1
├── adapters/
│   ├── python/             platform_adapter.py (+ tests), standard library only
│   └── node/               platform_adapter.js (+ tests), no dependencies
├── examples/               runnable demos (not part of the structure)
│   ├── counter-demo-python/    demo core, Python
│   ├── counter-demo-node/      demo core, Node
│   ├── custom-host/            a Go host with an in-process core, a wrapper kind and its own contracts
│   └── grpc-python-client/     calls a platform over gRPC with plain grpcio
├── engine/
│   ├── base/               the engine (config, contract, jsonschema, capability, bus, kernel, gateway, boot, transports)
│   ├── cmd/platform/       the bundled platform binary (core kinds: http, gateway)
│   ├── contrib/grpc/       gRPC transport + cmd/platform-grpc, the platform binary with gRPC (separate Go module)
│   ├── deploy/             Dockerfile, docker-compose.yml (isolated networks), config, .env.example
│   └── verify/             conformance tests, architecture test, fixtures
├── scripts/                test.sh, demo.sh, test-grpc.sh, verify/ (cross-platform verification tool)
├── docs/                   the complete documentation (start at docs/README.md)
├── .github/workflows/ci.yml
├── STATUS.md               what was verified and what was not
├── README.md               this file          README.fa.md  short Persian layout notes
```

`cores/` and `app/` are deliberately empty: their position says what they may hold, and a project may have no frontend or backend at all.

---

## 6. Quick start

**Requirements:** Go 1.22+ for the platform; Python 3.9+ and/or Node 18+ only to run the demo cores. Linux, macOS and Windows.

```bash
go build ./... && go vet ./... && go test -race -count=1 ./...        # drop -race if you have no C compiler
```

Run a platform with the demo core (two terminals):

```bash
export CORE_TOKEN=s3cret                                  # PowerShell: $env:CORE_TOKEN = "s3cret"
python3 examples/counter-demo-python/main.py                         # terminal 1: the core, on 127.0.0.1:9001

export CORE_TOKEN=s3cret
go run ./engine/cmd/platform -config config/platform.json            # terminal 2: the platform, on 127.0.0.1:8080
```

```bash
curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"counter.v1.increment","input":{"name":"visits"}}'
# {"output":{"value":1}}
curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"counter.v1.increment","input":{}}'
# {"error":{"code":"invalid_input"}}        rejected by the contract before it reached the core
```

`scripts/demo.sh python` or `scripts/demo.sh node` does this and tears it down.

**gRPC:** the `platform-grpc` binary is the same platform plus the gRPC transport (core kind `grpc` and an optional gRPC listener). It lives in its own Go module and needs the internet once to fetch
`google.golang.org/grpc`:

```bash
cd engine/contrib/grpc && go mod tidy && go run ./cmd/platform-grpc -config ../../../config/platform.json -grpc-listen 127.0.0.1:9000
```

The same token and `serve.expose` list protect both listeners ([docs/08](docs/08-wire-protocols.md#grpc)). The platform's flags: `-config FILE`, `-contracts DIR` (use a folder instead of the embedded
structural contracts), `-listen ADDR`, `-print-config` (show the final configuration with defaults filled in, then exit).

---

## 7. Configuration at a glance

```json
{
  "version": 1,
  "contracts_dirs": ["../domain-contracts"],
  "cores":    [ { "id": "a", "kind": "http", "options": { "url": "http://a:9001", "token_env": "A_TOKEN" } },
                { "id": "b", "kind": "http", "options": { "url": "http://b:9001", "token_env": "B_TOKEN" } } ],
  "roles":    { "counting": ["a", "b"] },
  "bindings": [ { "capability": "counter.v1.get", "cores": ["role:counting"], "mode": "fallback", "timeout_ms": 2000, "retries": 1 } ],
  "resilience": { "retries": 0, "retry_delay_ms": 50, "breaker_failures": 5, "breaker_cooldown_ms": 30000 },
  "limits":   { "max_depth": 8 },
  "serve":    { "listen": "127.0.0.1:8080", "token_env": "PLATFORM_TOKEN", "expose": ["counter.v1.get"] }
}
```

Unknown keys are errors; every key has a default; secrets are only env-var *names*; a typo in `expose` or a binding to a core that does not provide the capability stops the
platform **at startup**, not at runtime. Binding modes: `single` (exactly one core), `fallback` (first healthy, in order), `fanout` (all at once, answers labelled by position).
Full reference with defaults and every validation rule: [docs/07](docs/07-configuration.md).

---

## 8. The wire protocol at a glance

```
POST /v1/call            {"capability": "...", "input": {...}, "meta": {"k": "v"}}      Authorization: Bearer <token>
  200  {"output": {...}}
  400  invalid_input        401  unauthorized        404  unknown_capability (not exposed ≡ not found)
  422  <a code declared in the contract>             500  internal (never carries details)
  503  unavailable | maybe_delivered                 508  depth_exceeded
POST /v1/event           {"event": "...", "payload": {...}}                              → 202 {"accepted": true}
GET  /livez  (public)    GET /healthz  (token)
```

`meta` carries free key/value strings; `trace_id` and `depth` are managed by the platform. A fan-out binding answers
`{"output": {"results": [{"source": "0", "output": …}, {"source": "1", "error": {"code": …}}]}}`.
gRPC carries the same message as JSON on `/platform.v1.Platform/Call`. Everything, with client snippets in curl, Python, Node and PowerShell: [docs/08](docs/08-wire-protocols.md).

---

## 9. Security

Nothing is exposed unless listed; one bearer token (constant-time comparison, refuses to start if configured but empty); strict request envelope and a 1 MiB cap; inputs and outputs
validated against contracts; internal errors never leak details; secrets live only in environment variables; outbound traffic only through the gateway (allow-list, no private
addresses at connect time, size and time limits); nesting loop guard; Docker setup isolates every core on its own internal network. Not included: per-client tokens, per-capability
ACLs, rate limiting (write a wrapper), mTLS. Hardening checklist and the full model: [docs/11](docs/11-security.md).

---

## 10. Deployment

```bash
cd engine/deploy
cp .env.example .env          # set CORE_TOKEN and PLATFORM_TOKEN to long random values
docker compose up --build
```

A distroless, non-root, read-only image; each core on its own `internal: true` network shared only with the platform; only the platform joins `edge`; the port is published on
`127.0.0.1` and a TLS reverse proxy belongs in front. CI (`.github/workflows/ci.yml`) runs the tests, the live demo, the gRPC module and the image build.
See [docs/12](docs/12-deployment.md).

---

## 11. Testing and verification

| What | Command |
|---|---|
| Main module | `go build ./... && go vet ./... && go test -race -count=1 ./...` |
| Everything offline (Go + Python + Node adapters) | `scripts/test.sh` |
| Live demo with a Python or Node core | `scripts/demo.sh python` / `scripts/demo.sh node` |
| gRPC module (needs internet once) | `scripts/test-grpc.sh` |
| **Everything, cross-platform, with a pasteable report** | `go run ./scripts/verify [-core-bin <your built core>] [-docker-up]` |

The suites: unit tests per package; 38 conformance tests (real Go and Python cores against contracts, exposure, startup refusals, fan-out, fallback and retry safety, wrappers, gateway,
events, nesting, health); an architecture test for the dependency rules; 11 Python and 9 Node adapter tests; 7 gRPC end-to-end tests. Details: [docs/14](docs/14-testing-and-verification.md).

---

## 12. Documentation

| | |
|---|---|
| [01 Overview](docs/01-overview.md) | the problem, the idea, fit and non-fit, principles |
| [02 Concepts](docs/02-concepts.md) | glossary and the mental model |
| [03 Architecture](docs/03-architecture.md) | shape, packages, dependency rules, life of a call, startup, decisions, performance |
| [04 Getting started](docs/04-getting-started.md) | run the demo; build your first core step by step |
| [05 Writing cores](docs/05-writing-cores.md) | adapters, the raw protocol, how responses are interpreted |
| [06 Contracts](docs/06-contracts.md) | the contract format, schema subset, examples as tests, versioning |
| [07 Configuration](docs/07-configuration.md) | complete reference and patterns |
| [08 Wire protocols](docs/08-wire-protocols.md) | HTTP, events, health, gRPC |
| [09 Routing and resilience](docs/09-routing-and-resilience.md) | chains, modes, retries, idempotency, breaker, fan-out |
| [10 Extension points and patterns](docs/10-extension-points-and-patterns.md) | overrides, wrappers, gateway, events, complements, nesting |
| [11 Security](docs/11-security.md) | trust boundaries, controls, checklist |
| [12 Deployment](docs/12-deployment.md) | binary, Docker, isolation, CI |
| [13 Extending the host](docs/13-extending-the-host.md) | Go: in-process cores, wrapper kinds, transports |
| [14 Testing and verification](docs/14-testing-and-verification.md) | suites, the verify tool, testing your own core |
| [15 FAQ and troubleshooting](docs/15-faq-and-troubleshooting.md) | questions and fixes |
| [16 Limits and roadmap](docs/16-limits-and-roadmap.md) | what is missing and what could come next |
| [17 Integrating a program](docs/17-integrating-a-program.md) | the method for turning any program ("program X") into a core, and customizing it without touching it |

---

## Verification status

Run and passing (Go 1.22 and 1.27, Python 3.12, Node 22): the main module (build, vet, `go test -race`, gofmt); the Python and Node adapter tests; live demos with a real Python and a real
Node core; the gRPC transport and the `platform-grpc` binary (7 end-to-end tests over TCP, a real `grpcio` Python client against the real binary, and one platform mounting another over gRPC; its dependencies were fetched from GitHub clones with local `replace`,
the shipped `go.mod` has none); the build step of the Docker image and the binary run in the image layout; the custom-host example; the JSON examples in the docs that are complete documents,
parsed by the real parser and loader.

**Not** verified: `docker build` / `docker compose up` themselves, the first run of the CI workflow, the gRPC module's `go.sum` (run `scripts/test-grpc.sh` once and commit it), and the
platform against a real third-party program. `STATUS.md` is kept current.

## Known limits

The bundled `platform` binary knows only the `http` and `gateway` core kinds (`platform-grpc` adds `grpc`; in-process cores and wrapper kinds need a small Go host program); one bearer token and no per-capability ACL;
gRPC is unary, plaintext by default, no mTLS; events are best-effort on an in-process bus; no live config reload; contracts use a JSON Schema subset; not multi-tenant. See
[docs/16](docs/16-limits-and-roadmap.md).

## License

No license has been chosen for this project yet; add one before distributing it.
