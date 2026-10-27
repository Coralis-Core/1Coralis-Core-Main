# Status (verified Oct 25, 2026 — Go 1.22.2 and 1.27.3, Python 3.12, Node 22; no Docker available to the author)

## Verified by running
- Main module: go build, go vet, go test -race ./... (all packages), gofmt clean.
- Adapters: 11 Python + 9 Node tests (scripts/test.sh). Live demo with a Python and a Node core (scripts/demo.sh).
- gRPC (engine/contrib/grpc, separate module): a real transport (JSON codec, service registration, real client).
  7 end-to-end tests with -race over real TCP (6 for the transport, 1 for the platform-grpc binary): A->B mount, domain-error passthrough, invalid input, token enforced,
  only exposed capabilities served, server down = unavailable, loop guard, a token in the body cannot forge auth.
  A plain Python grpcio client (no stubs) was run against the Go server: all cases correct.
  The gRPC dependencies were fetched from GitHub clones with local `replace` in a scratch copy
  (google.golang.org is unreachable for the author). The shipped go.mod has no such replaces.
- Docker build STEP verified by running its exact `go build` (static binary) and the binary in the image layout
  (401 without the platform token, 200 with). docker-compose.yml keeps the isolation design (each core on its own
  internal network; only the platform on `edge`); YAML structure and every volume path validated.
- The platform's HTTP token checks are constant-time on every endpoint.
- Every JSON example in README.md that is a complete document was parsed with the real config parser / contract loader.
- The cross-platform verification tool itself (PASS / FAIL / SKIP paths, `-core-bin` path with an external executable).

- The `platform-grpc` binary (engine/contrib/grpc/cmd/platform-grpc): HTTP + optional gRPC listener + core kind `grpc`; run for real against a Python core with a plain grpcio
  client (token, domain error, graceful stop) and a platform mounting another over gRPC (test).

## Check the rest on your machine with one command (Windows/Linux/macOS), from the project root
    go run ./scripts/verify                                    main module, adapters, live demo, gRPC (needs internet), Docker if installed
    go run ./scripts/verify -core-bin <your built core> [-core-dir D]    also start your own core, mount it, call it through the platform
    go run ./scripts/verify -docker-up                         also run docker compose up and smoke-test it
It prints PASS/FAIL/SKIP and, for failures, the last log lines. Full log: verify.log.

## NOT verified (needs your machine / CI)
- `docker build` and `docker compose up` themselves (no Docker here). The first CI run will check them.
- gRPC go.sum: run scripts/test-grpc.sh once (needs network) and commit engine/contrib/grpc/go.sum.
- The GitHub Actions workflow has never run.
- The platform has not yet been run end to end against a real third-party program.

## Known limits
- gRPC: unary only, no streaming; default Dial is plaintext (use "tls": true or a TLS proxy beyond localhost); no mTLS.
- A wrong token on a mounted remote shows up to the caller as `internal` (same as HTTP); the server log has the detail.
- compose publishes the platform on 127.0.0.1 only; put a TLS reverse proxy in front before exposing it.
- The bundled platform binary knows only the core kinds `http` and `gateway`; `platform-grpc` (gRPC module) adds `grpc`;
  in-process cores and wrapper kinds need a small Go host program (see docs/13-extending-the-host.md and examples/custom-host).
