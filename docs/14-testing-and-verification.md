# 14 · Testing and verification

## The layers

| Layer | Where | What it proves |
|---|---|---|
| **Unit tests** | `engine/base/*` (capability 18, config 5, contract 4, httpjson 11, gateway 6, jsonschema 3) | each package's rules: routing, retries and breaker, config validation, schema subset, HTTP robustness, gateway limits |
| **Conformance** | `engine/verify/conformance` (38 tests) | the platform as a whole against its contracts: Go and **real Python** cores conform to contract examples; exposure rules; startup refusals; fan-out; fallback and retry safety; wrappers; gateway; events; nesting and loop depth; health; wire envelopes match their schemas |
| **Architecture test** | `engine/verify/archtest` (3 tests) | the dependency rules and the "no specific program or vendor in the base" rule, including that the rules really catch violations |
| **Adapter tests** | `adapters/python` (11), `adapters/node` (9) | success, domain error, hidden internals, token handling, bad JSON, wrong path, `describe` |
| **gRPC tests** | `engine/contrib/grpc` (7, separate module) | a real gRPC server and client over TCP: mounting, domain errors, tokens, exposure, server down, loop guard, forged token in the body; and the `platform-grpc` binary serving HTTP and gRPC with one token, with a second platform mounting it over gRPC |
| **Custom-host test** | `examples/custom-host` | an in-process core, a wrapper kind and your own contracts through the real HTTP handler |
| **Live demos** | `scripts/demo.sh python|node` | the real platform binary and a real core process, with real HTTP calls |

## Commands

| What | Command |
|---|---|
| Main module | `go build ./... && go vet ./... && go test -race -count=1 ./...` (drop `-race` without a C compiler) |
| Everything offline: Go + Python adapter + Node adapter | `scripts/test.sh` |
| Live demo with a Python or Node core | `scripts/demo.sh python` / `scripts/demo.sh node` |
| gRPC module (internet once, for `go mod tidy`) | `scripts/test-grpc.sh` |
| **Everything, cross-platform, with a pasteable report** | `go run ./scripts/verify` |
| Nesting latency numbers | `go test -run TestNestingLatencyReport -v ./engine/verify/conformance` |

Python-based conformance tests need `python3` on the PATH; they skip with a message if it is missing.

## `go run ./scripts/verify`

One command for Windows, Linux and macOS (no bash). Run it from the project root.

```bash
go run ./scripts/verify                              # main module, adapters, live demo, gRPC, Docker if installed
go run ./scripts/verify -core-bin ./mycore           # also test YOUR built core executable end to end
go run ./scripts/verify -core-bin X -core-dir D      # ... with D as the core's working directory (for a .env, say)
go run ./scripts/verify -docker-up                   # also docker compose up + a smoke test
```

Steps and what each does:

1. `go build`, `go vet`, `go test` (with `-race` when a C compiler is available) on the main module.
2. Python adapter tests and Node adapter tests (skipped if the interpreter is missing; it recognizes the Microsoft Store `python3` stub as "not Python").
3. Builds the platform binary and runs it with a real demo core (Python, else Node): calls `increment`, `get`, a domain error (422), `reset`, and an invalid input (400).
4. In `engine/contrib/grpc`: `go mod tidy`, `go vet`, `go test`. **Needs internet**; this is the step that produces `go.sum`.
5. If Docker exists: `docker build`, `docker compose config`; with `-docker-up` also `up`, an authorized call, and `down`.
6. With `-core-bin`: starts your executable with `CORE_TOKEN`, `LISTEN=127.0.0.1:<port>`, `HOST` and `PORT` in its environment, mounts it, and runs the same calls against it.
   **It writes and then deletes one test record** (counter name `verify-probe`) in whatever your core stores.

Output: PASS / FAIL / SKIP per step, then a block between `REPORT (paste this)` and the end with the last log lines of every failed step. The full log is `verify.log`.
The exit code is non-zero if anything failed.

## Testing a core you wrote

1. Write the contract with real examples and negative inputs ([06](06-contracts.md)).
2. Run `contract.Check` against your provider. `TestGoCoresConformToContracts` and `TestPythonCoreConformsToContracts` in `engine/verify/conformance` show how to wrap a Go function and a Python process
   as a provider; copy one.
3. Mount your core in a scratch config and run `go run ./scripts/verify -core-bin <your executable>` (if it implements the three `counter.v1.*` contracts), or call it by hand with `curl`.
4. Add it to CI.

## What has been verified, honestly

Run and passing: everything in the table above, on Go 1.22 and 1.27, with Python 3.12 and Node 22, including a real `grpcio` Python client against the Go gRPC server.
**Not verified by the author**: `docker build` and `docker compose up` themselves (no Docker was available; the build step and the binary in the image layout *were* run), the first run of the
GitHub Actions workflow, the `go.sum` of the gRPC module, and the platform against a real third-party program. `STATUS.md` in the repository root is kept current.
