# 08 · Wire protocols

Callers, cores and other platforms all speak the same message format over two transports: **HTTP+JSON** and **JSON over gRPC**.

## HTTP

### `POST /v1/call`

```
POST /v1/call
Authorization: Bearer <token>          (when serve.token_env is set)
Content-Type: application/json

{"capability": "counter.v1.get", "input": {"name": "visits"}, "meta": {"trace_id": "abc"}}
```

* The envelope is **strict**: unknown fields are rejected, `capability` and `input` are required, `meta` is an optional map of strings.
* `input` must satisfy the capability's contract.
* Request bodies are capped at 1 MiB; so are the responses the platform reads from cores.

Success: `200 {"output": {...}}`. Failure: `{"error": {"code": "...", "message": "..."}}` (the message is optional and never carries internals).

### HTTP status codes

| Status | Code | Meaning |
|---|---|---|
| 200 | | success |
| 400 | `invalid_input` | the envelope or the input is invalid |
| 401 | `unauthorized` | missing or wrong bearer token |
| 404 | `unknown_capability` | not exposed or not provided (deliberately indistinguishable) |
| 405 | `invalid_input` | the method is not `POST` |
| 413 | `invalid_input` | the body is larger than the cap |
| 422 | *a code declared in the contract* | a domain error |
| 500 | `internal` | anything else; details are only in the platform's log |
| 503 | `unavailable` | nothing was processed: no healthy provider, circuit open, could not connect |
| 503 | `maybe_delivered` | it may have been processed (timeout, broken connection, provider crash) and the contract is not idempotent |
| 508 | `depth_exceeded` | the call was already `limits.max_depth` platforms deep |

(`503` is used for both retry-relevant situations; the `code` tells them apart.)

### Meta

`meta` carries key/value strings with the call: into the handler's context, and on to any remote core or nested platform. The platform never interprets it, with two exceptions:

| Key | Behavior |
|---|---|
| `trace_id` | generated if absent; you may pass your own to correlate logs across platforms and cores |
| `depth` | managed by the platform: incremented on each hop; with `limits.max_depth` set, a call already that deep is refused (`508`). Do not set it yourself. |

### Fan-out answers

For a capability bound with `mode: "fanout"` the answer is an aggregate (schema `contracts/wire/v1/fanout.schema.json`):

```json
{"output": {"results": [
  {"source": "0", "output": {"value": 3}},
  {"source": "1", "error": {"code": "unavailable"}}
]}}
```

One entry per source in configured order; `source` is the position as text (never a core id, so the facade does not leak what is behind it); each entry holds exactly one
of `output` or `error`. Each source's output is validated against the capability's contract; a source that fails or breaks the contract is reported as an error entry (or fails the
whole call when `on_error` is `fail`).

### Events: `POST /v1/event`

```
POST /v1/event
{"event": "orders.v1.placed", "payload": {"order_id": 7}, "meta": {}}
→ 202 {"accepted": true}
```

Strict envelope, `payload` required, token and exposure rules as for `/v1/call` (only events listed in `serve.expose`; `404 unknown_capability` otherwise; `400` when the payload violates
the event contract). The request returns after the delivery attempts to subscribers finish or time out ([10](10-extension-points-and-patterns.md#events)).

### Health endpoints

| Endpoint | Auth | Answer |
|---|---|---|
| `GET /livez` | none | `{"status": "ok|degraded|down"}`; `503` when `down` |
| `GET /healthz` | token (when configured) | `{"status", "detail", "cores": [{"id", "status", "detail"}]}`; `503` when `down` |

Aggregation: all cores `ok` → `ok`; all `down` → `down`; anything else → `degraded`. Use `/livez` for liveness probes and `/healthz` (with the token) for readiness or dashboards.

### Calling from code

```bash
curl -s -X POST 127.0.0.1:8080/v1/call -H "Authorization: Bearer $PLATFORM_TOKEN" \
     -d '{"capability":"counter.v1.get","input":{"name":"visits"}}'
```

```python
import json, os, urllib.request
req = urllib.request.Request("http://127.0.0.1:8080/v1/call",
    data=json.dumps({"capability": "counter.v1.get", "input": {"name": "visits"}}).encode(),
    headers={"Content-Type": "application/json", "Authorization": "Bearer " + os.environ.get("PLATFORM_TOKEN", "")})
try:
    print(json.load(urllib.request.urlopen(req)))
except urllib.error.HTTPError as e:           # 4xx/5xx still carry the JSON error body
    print(e.code, json.load(e))
```

```js
(async () => {
  const res = await fetch('http://127.0.0.1:8080/v1/call', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${process.env.PLATFORM_TOKEN}` },
    body: JSON.stringify({ capability: 'counter.v1.get', input: { name: 'visits' } }),
  });
  console.log(res.status, await res.json());
})();
```

Always branch on the **`code`**, not on the message, and treat `unavailable` and `maybe_delivered` differently ([09](09-routing-and-resilience.md#retry-safety)).

TLS is a deployment concern: terminate it in front of the platform (see [12](12-deployment.md)), or give a remote core an `https://` URL.

## gRPC

JSON over gRPC: **no protobuf and no code generation**, so any language with a gRPC library can call it. It lives in a separate Go module, `engine/contrib/grpc`, so the base stays
dependency-free. The bundled `platform` binary does not include it; use the ready-made **`platform-grpc`** binary below, or register it in your own host program ([13](13-extending-the-host.md)).

```
service  platform.v1.Platform
method   /platform.v1.Platform/Call              unary
request  {"capability": "...", "input": <json>, "meta": {"k": "v"}}
response {"output": <json>}   or   {"error_code": "...", "error_message": "..."}
auth     gRPC metadata  authorization: Bearer <token>        (a token in the request body is ignored)
```

Behavior is the same as HTTP: contracts enforced on both sides, only exposed capabilities served, `max_depth` loop guard (`depth_exceeded`), transport failures become `unavailable`.
A wrong or missing token is `unauthorized` on the wire; a platform mounting a gRPC core with a wrong token sees `internal` (the code is not declared by any contract, so it is hidden).

### The ready-made binary: `platform-grpc`

`engine/contrib/grpc/cmd/platform-grpc` is the same platform (same config, same flags) plus gRPC:

```bash
cd engine/contrib/grpc
go mod tidy                                   # once, needs internet (fetches google.golang.org/grpc)
go run ./cmd/platform-grpc -config ../../../config/platform.json -listen 127.0.0.1:8080 -grpc-listen 127.0.0.1:9000
```

| Flag | Meaning |
|---|---|
| `-config`, `-contracts`, `-listen`, `-print-config` | exactly as in the bundled binary |
| `-grpc-listen ADDR` | also serve gRPC on this address; empty (default) = HTTP only |

It registers core kind `grpc`, so a config can mount a remote platform over gRPC with no code. The bearer token (`serve.token_env`), the exposure list (`serve.expose`) and `limits.max_depth`
apply to the gRPC listener exactly as to HTTP. It was run for real: a plain `grpcio` Python client against it, a wrong token answered `unauthorized`, a domain error crossed as `limit_exceeded`,
and a second platform mounted the first over gRPC (`engine/contrib/grpc/cmd/platform-grpc/main_test.go`).

### Server side in your own Go host

```go
srv := grpcjson.NewServer(contractSet, platform.Host().Router, grpcjson.Options{Token: tok, Allow: platform.Exposed, MaxDepth: 8})
g := grpcjson.NewGRPCServer()        // forces the JSON codec, so plain "application/grpc" clients work too
srv.Register(g)
go g.Serve(listener)
```

Mounting a remote over gRPC (the host calls `grpcjson.Register(&options)` once):

```json
{ "id": "a", "kind": "grpc", "capabilities": ["counter.v1.get"],
  "options": { "target": "host:9000", "token_env": "A_TOKEN", "timeout_ms": 3000, "tls": true } }
```

Client in Python with plain `grpcio` and no generated code (`examples/grpc-python-client/client.py`):

```python
import grpc, json
call = grpc.insecure_channel("127.0.0.1:9000").unary_unary("/platform.v1.Platform/Call",
        request_serializer=lambda d: json.dumps(d).encode(), response_deserializer=json.loads)
print(call({"capability": "counter.v1.get", "input": {"name": "visits"}}, metadata=[("authorization", "Bearer TOKEN")]))
```

The default dial is **plaintext** (use on localhost or a private network); `"tls": true` uses TLS with the system CAs. Not included: streaming, client-certificate mTLS.
