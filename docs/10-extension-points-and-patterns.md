# 10 · Extension points and patterns

The platform changes behavior in a handful of ways, all configuration or small, isolated code. None edits a core.

| Mechanism | What it changes | Where |
|---|---|---|
| **Bindings and roles** | which core serves a capability, and how | config |
| **Overrides** | replace what serves a capability | a binding to a different core |
| **Wrappers** | decorate a capability (before / after / around) | config + a wrapper kind in the host |
| **Gateway** | controlled outbound access | config |
| **Events** | react to things happening | contracts + subscriptions |
| **Complements** | new capabilities built from others | a core |
| **Nesting** | platforms inside platforms | config |

## Overrides

Bind a capability to another core than the one that "naturally" serves it:

```json
{ "capability": "counter.v1.get", "cores": ["my-better-counter"], "mode": "single" }
```

Any core that satisfies the contract can take over, including one that is a thin adapter around the original with changed behavior.

## Wrappers

A wrapper is an **around** decorator. Code before calling `next` is "before"; code after is "after"; it may change the input, skip the call, retry it, or change the output.
It never edits the core it wraps, and an error it returns reaches the caller (nothing swallows errors silently).

```json
"wrappers": [
  { "capability": "counter.v1.increment", "kind": "audit",     "placement": "outer" },
  { "capability": "counter.v1.increment", "kind": "ratelimit", "placement": "inner", "options": { "per_minute": 30 } }
]
```

* `outer` wraps the whole call; `inner` wraps each provider call (and each source of a fan-out).
* The list order is the wrapping order: the first entry is the outermost.
* A wrapper sees only inputs that already passed the contract; its output is validated against the contract again (`check_contract`, default on).
* A wrapper that returns a domain error must use a code the contract declares, or the caller sees `internal`.
* **Wrapper kinds are code.** The bundled binary has none; you register them in a host program ([13](13-extending-the-host.md) shows a complete one, with an `audit` wrapper).
* Typical wrappers: audit log, rate limit, cache, authorization by `meta`, input normalization, response redaction.

## Gateway

Cores are kept off the internet. Anything that must reach the outside calls `gateway.v1.request` through the platform, served by a core of kind `gateway` (or any core that provides that capability, so the role is replaceable).

```json
{ "id": "out", "kind": "gateway",
  "options": { "mode": "strict", "allowed_domains": ["api.example.com"],
               "keys": [ { "domain": "api.example.com", "header": "Authorization", "env": "EXAMPLE_KEY", "prefix": "Bearer " } ] } }
```

Request: `{"method": "GET", "url": "https://api.example.com/v1/items", "headers": {}, "body": ""}` →
`{"status": 200, "headers": {}, "body": "…"}`. Errors: `domain_not_allowed`, `blocked_address`, `too_large`, `upstream_error`.

Strict mode (default): only listed domains (`"api.example.com"` or `"*.example.com"`), no redirects, private and internal addresses blocked **at connect time** (so DNS tricks do not help),
response size and time limits, API keys injected from environment variables (the config holds only the variable *names*), every outbound call logged **without** its query string or any secret.
`mode: "open"` allows any domain; everything else stays in force, and you accept that risk explicitly.

To expose it to callers, add `gateway.v1.request` to `serve.expose`; to keep it internal (usual), leave it out and let cores call it through the platform.

## Events

An event has its own contract (`kind: "event"`, a `payload` schema) and is delivered **best effort** to the cores subscribed in config.

```json
"subscriptions": [ { "event": "orders.v1.placed", "core": "role:notifier", "timeout_ms": 1000 } ]
```

* Publishers: `POST /v1/event` (only events in `serve.expose`), or code in the host (`Platform.Publish`).
* The payload is validated against the event contract before delivery.
* Delivery goes to all subscribers **concurrently**; the publishing request waits until each has answered or timed out (default 2 s each), then returns `202 {"accepted": true}`.
* A subscriber that is down or slow misses the event; a warning is logged ("no delivery guarantee"). Anything that must not be lost needs its own persistence (a core with a queue).
* A remote core receives an event as an ordinary `POST /v1/call` whose `capability` is the event's name and whose `input` is the payload; it answers `{"output": {}}`.

## Complements

A complement is an ordinary core that *calls* capabilities and *provides* new ones. In any language: call the platform over the wire with a token, provide your capability, mount yourself.
In Go: use the typed API in-process ([13](13-extending-the-host.md#the-typed-go-api)). Use complements for merging fan-out results, workflows across several cores,
and the customer-facing shape of a product. Expose only the complement's capabilities, and the cores below stay invisible.

## Nesting

An inner platform is mounted as an `http` core. It exposes only its `serve.expose` list. Set `limits.max_depth` on each platform in a chain; every hop adds a `depth` and a call that comes
back around is refused with `depth_exceeded` (HTTP 508). Use nesting to separate trust domains (a team, a customer, a network zone), or to present one stable facade over several internal platforms.

## Patterns at a glance

| Goal | Recipe |
|---|---|
| Replace a program without touching callers | one role, change its member; or a binding to the new core |
| Keep working when a core is down | `fallback` with two cores, `timeout_ms`, `retries` |
| Compare or merge several sources | `fanout` + a complement that merges |
| Add auditing or rate limiting | an outer or inner wrapper |
| Change one behavior of an existing program | a core that wraps it, bound in place of the original (override) |
| Let a program call the outside world safely | the gateway; the core calls `gateway.v1.request` |
| Present a curated product | expose only a complement's capabilities |
| Isolate a customer or zone | a nested platform with its own `expose` list and token |
| React to something happening | an event contract + subscriptions |
| Support many languages | one adapter per language; the contracts do not change |
