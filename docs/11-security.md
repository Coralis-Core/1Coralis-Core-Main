# 11 · Security

## Trust boundaries

```
untrusted caller ─► [ platform: token · exposure · contracts ] ─► core (trusted by you, isolated)
                                                                  core ─► [ gateway: allow-list ] ─► internet
```

* **Callers are untrusted.** They reach only exposed capabilities, only with a valid token, only with inputs the contract accepts.
* **Cores are trusted to do their job but not to be reachable by each other or by the internet.** The deployment isolates them ([12](12-deployment.md)); the platform validates what they return.
* **The platform process is trusted.** It holds the tokens of the cores it calls. Protect its environment like any secret store.

## Controls

| Concern | Control |
|---|---|
| **Exposure** | nothing is reachable unless listed in `serve.expose`; unexposed and nonexistent capabilities both answer `404 unknown_capability`; a typo in `expose` stops startup |
| **Authentication** | one bearer token from the env var named by `serve.token_env`; constant-time comparison on every endpoint (`/v1/call`, `/v1/event`, `/healthz`; `/livez` is public by design); a configured-but-empty token refuses to start. Each remote core gets its own token via its `token_env` |
| **Input trust** | every input is validated against its contract before a core sees it; bodies are capped at 1 MiB; the envelope is strict |
| **Output trust** | every output is validated before a caller sees it; a core that returns an undeclared error code or an invalid output is logged and hidden behind `internal` |
| **Information leakage** | `internal` never carries a message; core error messages are optional and never include stack traces in the adapters; `/healthz` details need the token |
| **Secrets** | config holds only env-var names; the gateway logs no query strings, keys or headers |
| **Outbound traffic** | only through the gateway: domain allow-list, no private/internal addresses (checked at connect time), size and time limits, no redirects by default |
| **Loops / runaway nesting** | `limits.max_depth` + the `depth` meta key |
| **Resource exhaustion** | per-call timeouts, circuit breaker, body caps; configure `timeout_ms` narrowly for slow cores |
| **Network isolation** | the provided Docker setup puts each core on its own `internal` network shared only with the platform |
| **Transport security** | TLS is terminated in front of the platform (HTTP) or configured on gRPC (`"tls": true`); there is no mTLS |

## What the platform does *not* do

* No per-capability or per-client authorization: **one token = full access to the exposed facade.** Put finer rules in a wrapper (for example one that reads `meta`) or in a complement.
* No rate limiting by itself (write a wrapper).
* No protection from a malicious *core*: a core you mount can return wrong but contract-valid data, or misuse the token the platform gave it. Trust follows what you mount.
* No encryption at rest, no audit store, no secret rotation (rotate the variable and restart).
* `/livez` is unauthenticated and reveals only `ok|degraded|down`.
* gRPC is plaintext by default.

## Hardening checklist

1. Set `serve.token_env` and a long random token (`openssl rand -hex 24`); give every core its own token.
2. Expose the minimum: list capabilities explicitly, never `"*"` in production.
3. Bind `serve.listen` to localhost or a private interface and terminate TLS in a reverse proxy; do not publish core ports.
4. Run each core on its own internal network with no route to the internet; send outbound traffic through the gateway in `strict` mode with an explicit allow-list.
5. Turn on `limits.max_depth` whenever platforms nest; set `timeout_ms` on every slow capability.
6. Declare `idempotent` honestly; use `single` for capabilities that must never be duplicated.
7. Keep contracts closed (`additionalProperties: false`) and bounded (`maxLength`, `minimum`, `maximum`).
8. Run containers read-only, non-root, with capabilities dropped (the provided compose file does).
9. Treat the platform's environment and logs as sensitive; ship logs to a place with access control.
10. Rotate tokens by changing the variables and restarting; there is no live reload.

## Reporting and review

Run `go run ./scripts/verify` after any change to config or code; it exercises the live path with tokens, exposure, contracts and error mapping. The tests that matter for security
are in `engine/verify/conformance` (exposure, tokens, depth) and the transport tests in `engine/base/transports/httpjson` and `engine/contrib/grpc`.
