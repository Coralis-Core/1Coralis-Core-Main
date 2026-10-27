# 15 · FAQ and troubleshooting

## FAQ

**Is this a service mesh / API gateway / workflow engine?**
No. A mesh moves bytes between services; this checks what each call *means* against a contract and lets you replace, decorate and combine the providers. It has no discovery, streaming or durable workflows ([01](01-overview.md)).

**Do my cores have to be written in Go?**
No. Any language that can serve HTTP+JSON (or the gRPC variant) works; adapters exist for Python and Node ([05](05-writing-cores.md)). Only *in-process* cores are Go.

**Why JSON files for contracts instead of protobuf or OpenAPI?**
One small format every language reads without a compiler plugin, reviewable in a diff, and strict enough to enforce at runtime. The price is a limited schema vocabulary ([06](06-contracts.md#the-json-schema-subset)).

**Does the platform validate everything, always?**
Everything that crosses a transport or a mounted remote core: inputs before a core sees them, outputs before a caller sees them. Calls between *Go* cores through the typed in-process API do not go through validation (same process, shared types).

**Can I change a core's behavior without forking it?**
Yes, if the change can be expressed at the boundary: bind the capability to another core, decorate it with a wrapper, or put a core in front that calls the original and adjusts the result ([10](10-extension-points-and-patterns.md)).

**How do I replace a core without callers noticing?**
Put cores in a role and bind capabilities to `role:<name>`; change the role's member. Both cores must satisfy the same contract ([07](07-configuration.md#patterns)).

**How do I give each customer a different set of capabilities?**
Run one platform (or nested platform) per customer with its own `serve.expose` and token. Multi-tenancy inside one platform is not built.

**Can the platform reload its config?**
No. Restart it; the supervisor makes that cheap, and a bad config fails at startup instead of mid-flight.

**Is it safe to expose directly to the internet?**
Not by itself. It authenticates with one bearer token and does no rate limiting; terminate TLS and add rate limiting in front ([11](11-security.md)).

**What is the performance cost?**
Roughly 50 µs of platform overhead per HTTP hop on loopback; in-process typed calls are microseconds ([03](03-architecture.md#performance)).

**Which Go version?** 1.22 or newer for the platform. Your own host program may need whatever your own dependencies need.

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| Platform exits at start: `json: unknown field "..."` | a typo or unsupported key in the config; keys are strict. Use `-print-config` after fixing |
| `serve.token_env "X" is set but the environment variable is empty` | export the variable before starting; this is deliberate so you never run unauthenticated by accident |
| Exits at start naming a capability in `serve.expose` | nothing provides it: typo, or the core that provides it is not mounted |
| Exits at start: a binding points at a core that does not provide the capability | fix the binding's `cores`, or the core's `capabilities` |
| Exits at start: unknown core `kind` / unknown wrapper kind | the bundled `platform` binary only knows `http` and `gateway`; for `grpc` use `platform-grpc`, for anything else write your own host program ([13](13-extending-the-host.md)) |
| `WARN boot: core could not be described; trusting the config` | the core was not up yet; harmless if it comes up, but calls fail until then |
| `404 unknown_capability` for something that exists | it is not in `serve.expose`, or the name has a typo (both look identical on purpose) |
| `400 invalid_input` | the input violates the contract (or the envelope has an unknown field / no `input`); read the contract file |
| `401 unauthorized` from the platform | missing or wrong `Authorization: Bearer ...` header |
| Caller sees `internal` after you changed a core | the core returned an error code its contract does not declare, an output that violates the contract, or a wrong token; the platform log has the real reason |
| `503 unavailable` right after start | the core is down or its circuit is open (5 consecutive failures, 30 s); check `GET /healthz` |
| `503 maybe_delivered` | the core may have processed the request but the answer was lost; if repeating is harmless, declare the capability `idempotent` |
| `508 depth_exceeded` | the call passed through `limits.max_depth` platforms; a loop in nesting, or the limit is too low |
| Fallback did not happen | the first failure was a domain error (final) or `maybe_delivered` on a non-idempotent capability (not retried); only `unavailable` always falls back |
| Everything times out | the first provider consumed the whole `timeout_ms` budget; raise it, or lower the core's own timeout |
| gRPC call returns `unauthorized` via a mounted remote, but `internal` to the end caller | expected: the code is not declared in any contract, so it is hidden; fix the token |
| `scripts/verify`: Python tests fail on Windows | `python3` may be the Microsoft Store stub; install Python or use `python` |
| `go: module lookup disabled` or cannot reach `google.golang.org` | the gRPC module needs the internet once: `scripts/test-grpc.sh` |
| `-race` fails to build | no C compiler; run the tests without `-race` |

## Getting more detail

* Logs go to standard error; failed attempts, contract violations and undelivered events are logged with the capability and provider.
* `GET /healthz` (with the token) shows each core's status and detail.
* Pass your own `trace_id` in `meta` and search for it in the platform's and cores' logs.
* `-print-config` shows exactly what configuration the platform runs with.
