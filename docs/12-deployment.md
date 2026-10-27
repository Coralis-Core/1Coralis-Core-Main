# 12 · Deployment

## What you deploy

* **The platform**: one static binary (`go build ./engine/cmd/platform`), or your own host program. It needs the config file, and (if you use the `-contracts` flag, as the image does) the contracts folder.
* **Your cores**: separate processes, anywhere the platform can reach: same machine, containers, other hosts.
* **A reverse proxy for TLS** in front of the platform if it faces anything but localhost.

## Running the binary

There are two ready-made binaries: `engine/cmd/platform` (core kinds `http` and `gateway`) and `engine/contrib/grpc/cmd/platform-grpc` (the same plus gRPC; build it inside its own module,
`cd engine/contrib/grpc && go build ./cmd/platform-grpc`, which needs the internet once). The provided Dockerfile builds the first; to ship gRPC, copy it and change the build target and the module path.

```bash
export PLATFORM_TOKEN=...            # the variable named by serve.token_env
export CORE_TOKEN=...                # one variable per core token_env
platform -config /etc/platform/platform.json -listen 127.0.0.1:8080
```

* It reads only the variables your config names, once, at start. Changing a token or the config means a restart.
* It logs to standard error (`slog` text format) and shuts down gracefully on `SIGINT`/`SIGTERM` (in-flight requests get 5 s, then cores stop in reverse start order).
* Run it under a process supervisor (your service manager or container platform) so it restarts on failure. A configuration mistake fails at startup, loudly; the supervisor should not hide it.
* Probes: `GET /livez` for liveness; `GET /healthz` (with the bearer token) for readiness and per-core status.

## Docker

`engine/deploy/` contains:

| File | Purpose |
|---|---|
| `Dockerfile` | builds **only** the generic platform: `golang:1.22-alpine` → `CGO_ENABLED=0 go build -trimpath` → `gcr.io/distroless/static-debian12:nonroot`. Config and contracts are mounted at run time, never baked in. Entrypoint: `/platform -config /config/platform.json -contracts /contracts -listen 0.0.0.0:8080` |
| `docker-compose.yml` | the platform plus a demo core, with the isolation design below |
| `platform.compose.json` | the config used by the compose file |
| `.env.example` | the two required variables |

```bash
cd engine/deploy
cp .env.example .env              # set CORE_TOKEN and PLATFORM_TOKEN to long random values (openssl rand -hex 24)
docker compose up --build
curl -s -X POST 127.0.0.1:8080/v1/call -H "Authorization: Bearer $PLATFORM_TOKEN" \
     -d '{"capability":"counter.v1.get","input":{"name":"visits"}}'
```

The compose file refuses to start without both tokens (`${VAR:?}`), so there is no way to run it with empty secrets by accident.

### The isolation design

```
                 edge network (internet access)
                      │
                 ┌────┴─────┐
                 │ platform │──── gateway core (in-process): the only way out
                 └──┬────┬──┘
        net-counter │    │ net-<other>          (each: internal: true = no route to the internet)
              ┌─────┴─┐ ┌┴──────┐
              │ core  │ │ core  │              cores never share a network with each other
              └───────┘ └───────┘
```

* Every core sits on **its own `internal: true` network** together with the platform only. Cores cannot talk to each other and have no route to the internet.
* Only the platform joins the `edge` network. Outbound calls go core → platform → gateway.
* The platform's port is published on `127.0.0.1` only; put a TLS reverse proxy in front before exposing it.
* Containers run **read-only**, with all Linux capabilities dropped and `no-new-privileges`.
* To add a core: a new service, a new `net-<core>` network (`internal: true`), an entry in `platform.compose.json`.

Replace the demo `counter` service with your real core's image; keep the network and the healthcheck pattern.

## Continuous integration

`.github/workflows/ci.yml` has three jobs:

| Job | What it runs |
|---|---|
| `core` | repository hygiene (fails on merge-conflict markers or tracked build leftovers such as `__pycache__`), gofmt check, `scripts/test.sh` (vet + race tests + Python and Node adapter tests), then the live demo with a Python core and with a Node core |
| `grpc` | in `engine/contrib/grpc`: `go mod tidy`, `go vet`, `go test -race` (needs network) |
| `image` | `docker build -f engine/deploy/Dockerfile` and `docker compose config` |

After the first successful `grpc` job, run `scripts/test-grpc.sh` once locally and commit `engine/contrib/grpc/go.sum`.

## Operating checklist

1. Tokens: long, random, one per core, one for the platform; supplied as environment variables by your secret store.
2. `serve.expose` lists exactly the facade; `limits.max_depth` is set if platforms nest.
3. Every core has a `timeout_ms` that fits it; non-idempotent capabilities are `single`.
4. TLS terminates in front; the platform listens on a private address.
5. Logs are collected; alert on `WARN`/`ERROR` lines (failed attempts, contract violations, undelivered events) and on `/healthz` going `degraded`.
6. Configuration changes are reviewed with `-print-config` and deployed by restart.
