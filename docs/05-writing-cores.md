# 05 · Writing a core

A core is a program that provides capabilities. It can be written in **any language** and run anywhere the platform can reach.
There are three ways to write one; the contract is identical in all of them.

| Way | Use when | Where it is described |
|---|---|---|
| **An adapter** (Python, Node) | the fastest route in those languages | [below](#using-an-adapter) |
| **The raw wire protocol** | any other language, or a service you already have | [below](#implementing-the-protocol-yourself) |
| **In-process Go** | a Go core that lives inside the platform process | [13 Extending the host](13-extending-the-host.md) |

## What every core must do

1. Accept `POST /v1/call` with `{"capability": "...", "input": {...}, "meta": {...}}` (`meta` optional).
2. Answer `200 {"output": {...}}` on success, or `{"error": {"code": "...", "message": "..."}}` on failure.
3. Answer the two structural capabilities (below).
4. If a token is configured for it, require `Authorization: Bearer <token>`.

You do **not** have to validate against the contract: the platform already checked the input before sending it and will check your output
before returning it. You should still be strict about what you accept; the contract is the minimum, not a replacement for your own logic.

### The two structural capabilities

`core.v1.describe` (input `{}`) tells the platform who you are and what you provide:

```json
{"output": {"name": "text", "version": "1", "capabilities": ["text.v1.reverse"], "publishes": [], "subscribes": []}}
```

`core.v1.health` (input `{}`) reports your state: `ok`, `degraded` or `down`, with an optional `detail`:

```json
{"output": {"status": "ok"}}
```

The platform folds all cores' health into `/healthz` and `/livez` ([08](08-wire-protocols.md#health-endpoints)).

## How the platform interprets your answers

This is what lets the platform decide whether a retry is safe, so it is worth knowing exactly:

| What your core does | What the platform concludes |
|---|---|
| `{"error": {"code": "X"}}` with any HTTP status | the call failed with code `X` (a declared domain code, or a generic one) |
| `{"error": {"code": "unavailable"}}` | "nothing was processed": **only send this if that is true**; it allows retry and fallback for every capability |
| `{"output": {...}}` with status 200 | success (then checked against the output schema) |
| the connection is refused (nothing listening) | `unavailable` (nothing was sent) |
| a timeout, a reset connection, or an unreadable body after sending | `maybe_delivered` (it may have been processed) |
| a 5xx with a body that is not JSON | `maybe_delivered` |
| any other malformed answer | `internal` |
| an error code that is neither declared in the contract nor generic | `internal` (and logged) |

Responses larger than 1 MiB are cut off and treated as unreadable.

## Domain errors

A domain error is a situation your core defines and the contract declares in `"errors"` (for example `limit_exceeded`, `not_found`).
It crosses the wire as `{"error": {"code": "limit_exceeded"}}`; over HTTP the platform answers with status **422**. Use codes that are
lowercase words with underscores, stable and language-neutral. Do not put secrets or stack traces in `message`; keep it short and safe to show.
For anything unexpected, let the adapter return `internal`: it hides the details on purpose.

## Using an adapter

The adapters are tiny (one file each, no dependencies) and are the reference implementation of the protocol.

### Python: `adapters/python/platform_adapter.py` (Python 3.9+)

```python
import os, sys
sys.path.insert(0, "adapters/python")
from platform_adapter import Core, CoreError

core = Core("counter-demo", token_env="CORE_TOKEN")     # the NAME of the env var that holds this core's token
counters = {}

@core.capability("counter.v1.increment")
def increment(inp, meta):                                # inp: the validated input; meta: the envelope's meta
    value = counters.get(inp["name"], 0) + inp.get("by", 1)
    if value > 1000:
        raise CoreError("limit_exceeded", "a counter cannot exceed 1000")   # declared in the contract
    counters[inp["name"]] = value
    return {"value": value}

core.health = lambda: ("ok", "")          # optional: return ("degraded", "why") or ("down", "why")
core.serve(host="127.0.0.1", port=9001)
```

### Node: `adapters/node/platform_adapter.js` (Node 18+)

```js
const { Core, CoreError } = require('./adapters/node/platform_adapter.js');
const core = new Core('counter-demo', { tokenEnv: 'CORE_TOKEN' });
const counters = new Map();
core.capability('counter.v1.increment', (inp) => {
  const value = (counters.get(inp.name) || 0) + (inp.by ?? 1);
  if (value > 1000) throw new CoreError('limit_exceeded', 'a counter cannot exceed 1000');
  counters.set(inp.name, value);
  return { value };
});
core.serve(9001, '127.0.0.1');
```

What the adapters do for you: serve `POST /v1/call` only, check the bearer token in constant time, cap the body at 1 MiB, answer
`core.v1.describe` (listing the capabilities you registered, sorted) and `core.v1.health`, map `CoreError` to a domain error, and turn **any other exception**
into `500 {"error": {"code": "internal"}}` without leaking its message. They are covered by 11 (Python) and 9 (Node) tests.

Handlers are synchronous in the Python adapter (a threaded server runs them in parallel; protect shared state yourself, as in any threaded server).
The Node adapter awaits async handlers.

## Implementing the protocol yourself

Any HTTP server will do. The whole job:

```
POST /v1/call
Authorization: Bearer <token>                   (if you require one; compare in constant time)
Content-Type: application/json

{"capability": "counter.v1.get", "input": {"name": "visits"}, "meta": {"trace_id": "9f1c…", "depth": "1"}}

→ 200  {"output": {"value": 3}}
→ 422  {"error": {"code": "limit_exceeded", "message": "…"}}       (a domain error)
→ 401  {"error": {"code": "unauthorized"}}                          (bad token)
→ 404  {"error": {"code": "unknown_capability"}}                    (you do not provide it)
→ 500  {"error": {"code": "internal"}}                              (your bug; hide the details)
```

Checklist for a hand-written core:

* answer `core.v1.describe` and `core.v1.health` like any other capability;
* never trust `meta` for security decisions; it is free-form text (but you may log `meta.trace_id` to correlate with the platform's logs);
* limit the request size you read, and do not log secrets from `input`;
* bind to `127.0.0.1` (or a private network) unless you really need otherwise; the platform is the only intended caller;
* make capabilities **idempotent** where you can, and say so in the contract: that is what makes retries and fallback safe ([09](09-routing-and-resilience.md#retry-safety)).

## Cores that publish or receive events

A core listens for an event by being named in a subscription in the config ([10](10-extension-points-and-patterns.md#events)). It receives the event as an
ordinary `POST /v1/call` whose `capability` is the event's name and whose `input` is the payload, and it answers `{"output": {}}`.
List the events you handle under `subscribes` in your `core.v1.describe`. To publish, call `POST /v1/event` on the platform (only events in `serve.expose` are accepted from outside).

## Mounting a core

```json
{ "id": "counter-demo", "kind": "http",
  "capabilities": ["counter.v1.increment", "counter.v1.get", "counter.v1.reset"],
  "options": { "url": "http://127.0.0.1:9001", "token_env": "CORE_TOKEN", "timeout_ms": 3000 } }
```

* `url`: where the core listens. `https://` works if the core terminates TLS.
* `token_env`: the **name** of the environment variable that holds the token the platform sends to this core. The variable is read when the platform starts.
* `timeout_ms`: the per-request budget for this core (otherwise `default_timeout_ms`, 10 s).
* `capabilities`: optional. If omitted, the platform discovers them with `core.v1.describe`. If listed and the core is not up yet, the platform logs a warning and
  trusts the list, so start order does not matter much; capabilities you list that the core does not really provide will fail at call time.

Transport alternatives: a core can be mounted over gRPC (kind `grpc`, [08](08-wire-protocols.md#grpc)), or be another platform (nesting, [03](03-architecture.md#nesting-and-the-complement)).

## Testing a core against its contract

The contract's `examples` (positive and error examples) and `invalid_inputs` are executable: `contract.Check` runs them against a provider and returns the list of problems.
`engine/verify/conformance` does this for a Go core and for a real Python core as templates ([14](14-testing-and-verification.md)). A good habit: write the examples first, run them against your core, then ship.
