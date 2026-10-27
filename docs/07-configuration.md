# 07 · Configuration

One JSON file, `config/platform.json` by default, describes a whole platform: what is mounted, who serves what, what is exposed, and how
failures are handled. The schema is `contracts/config/v1/`.

## Strictness and defaults

* **Unknown keys are errors**, at any depth (`json: unknown field "bogus"`). The only free-form place is `extensions`.
* Every key has a default, so a platform works with no more than `{"version": 1}` plus the cores you want. Defaults live in one file, `engine/base/config/defaults.go`.
* **Secrets never appear in the config**: wherever a secret is needed, the config holds the *name* of an environment variable (`token_env`, `env`).
* `platform -config FILE -print-config` prints the final configuration with defaults filled in, and the dotted names of the keys that came from defaults, then exits.
  Use it in review and in CI.

```bash
go run ./engine/cmd/platform -config config/platform.json -print-config
```

## Full example

```json
{
  "version": 1,
  "default_timeout_ms": 10000,
  "contracts_dirs": ["../domain-contracts"],

  "cores": [
    { "id": "counter-a", "kind": "http", "capabilities": ["counter.v1.increment", "counter.v1.get", "counter.v1.reset"],
      "options": { "url": "http://counter-a:9001", "token_env": "COUNTER_A_TOKEN", "timeout_ms": 3000 } },
    { "id": "counter-b", "kind": "http",
      "options": { "url": "http://counter-b:9001", "token_env": "COUNTER_B_TOKEN" } },
    { "id": "out", "kind": "gateway",
      "options": { "mode": "strict", "allowed_domains": ["api.example.com"],
                   "keys": [ { "domain": "api.example.com", "header": "Authorization", "env": "EXAMPLE_KEY", "prefix": "Bearer " } ] } }
  ],

  "roles": { "counting": ["counter-a", "counter-b"] },

  "bindings": [
    { "capability": "counter.v1.get",       "cores": ["role:counting"], "mode": "fallback", "timeout_ms": 2000, "retries": 1 },
    { "capability": "counter.v1.increment", "cores": ["counter-a"],     "mode": "single" }
  ],

  "wrappers":      [],
  "subscriptions": [],

  "resilience": { "retries": 0, "retry_delay_ms": 50, "breaker_failures": 5, "breaker_cooldown_ms": 30000 },
  "limits":     { "max_depth": 8 },

  "serve": { "listen": "127.0.0.1:8080", "token_env": "PLATFORM_TOKEN", "expose": ["counter.v1.get", "counter.v1.increment"] },

  "extensions": { "anything": "the engine never reads this; your own top layer may" }
}
```

## Reference

### Top level

| Key | Type | Default | Meaning |
|---|---|---|---|
| `version` | integer | required | must be `1` |
| `default_timeout_ms` | integer ≥ 0 | `10000` | per-call budget when nothing narrower applies |
| `contracts_dirs` | string[] | none | extra contract folders merged with the structural contracts; **relative paths resolve against the config file** |
| `cores` | object[] | none | what to mount |
| `roles` | object | none | `name → [core ids]` |
| `bindings` | object[] | none | how each capability is served |
| `wrappers` | object[] | none | decorators (kinds come from the host program) |
| `subscriptions` | object[] | none | event routing |
| `resilience` | object | see below | platform-wide retry and breaker defaults |
| `limits` | object | `{}` | protections, off by default |
| `serve` | object | see below | the platform's own endpoint |
| `extensions` | any | none | free space, never read by the engine |

### `cores[]`

| Key | Meaning |
|---|---|
| `id` | unique name (required). Used in bindings, roles and logs; never shown to callers. |
| `kind` | how to reach it (required): `http` (built in), `gateway` (built in), or a kind your host program registers (`grpc` is registered by the `platform-grpc` binary; your in-process kinds by your own host). |
| `capabilities` | the capabilities it serves; empty = discover with `core.v1.describe`. |
| `options` | depends on `kind`. For `http`: `url` (required), `token_env`, `timeout_ms`. For `gateway`: see below. For `grpc`: `target`, `token_env`, `timeout_ms`, `tls`. |

### `roles`

`{"counting": ["counter-a", "counter-b"]}`. A role name must be non-empty and contain no `:`; it must list at least one declared core (a role nobody plays is an error,
not a silent no-op). Reference it as `role:counting` in `bindings[].cores` or `subscriptions[].core`. Replacing a core means editing this line only.

### `bindings[]`

| Key | Meaning |
|---|---|
| `capability` | required; a capability may be bound only once |
| `cores` | required; core ids or `role:<name>`, **in preference order** |
| `mode` | `single`, `fallback`, `fanout`, or empty (lenient) |
| `on_error` | `skip` (default) or `fail`; **only with `fanout`** |
| `timeout_ms` | budget for this capability; in a fan-out, **each source's** budget |
| `retries` | extra attempts for retriable failures of this capability (0 = platform default) |

Modes in one line each ([09](09-routing-and-resilience.md) has the full semantics): `single` uses exactly one core and never falls back (so it needs exactly one);
`fallback` uses the first healthy core, in order; `fanout` asks every core at once and returns all answers. With an empty mode both single-result and fan-out
calls are allowed. Without any binding, every core providing the capability is tried in mount order.

### `wrappers[]`

| Key | Meaning |
|---|---|
| `capability`, `kind` | required; `kind` must be registered by the host program (an unknown kind fails at build) |
| `placement` | `outer` (default, wraps the whole call) or `inner` (wraps each provider call, also inside a fan-out) |
| `options` | passed to the wrapper factory |
| `check_contract` | default `true`: the wrapper's output is validated against the contract; `false` accepts the risk |

The list order is the wrapping order: the first entry is the outermost.

### `subscriptions[]`

`{"event": "<event contract name>", "core": "<id or role:name>", "timeout_ms": 2000}`. Best effort, concurrent, per-subscriber timeout (default 2000 ms).

### `resilience`

| Key | Default | Meaning |
|---|---|---|
| `retries` | `0` | extra attempts after a retriable failure (repeating is only safe if the contract says `idempotent`) |
| `retry_delay_ms` | `50` | pause between attempts |
| `breaker_failures` | `5` | consecutive infrastructure failures that open a core's circuit; `-1` turns the breaker off |
| `breaker_cooldown_ms` | `30000` | how long an open circuit skips the core |

### `limits`

`max_depth` (default `0` = unlimited): refuse a call that has already passed through this many platforms (a loop guard for nesting). Turn it on whenever you nest platforms.

### `serve`

| Key | Default | Meaning |
|---|---|---|
| `listen` | `127.0.0.1:8080` | bind address (the `-listen` flag overrides it) |
| `token_env` | none | name of the env var with the bearer token callers must present; if the name is set but the variable is empty, the platform **refuses to start** |
| `expose` | none | capabilities (and events) callers may use; `"*"` = everything; empty = only the structural `core.v1.*` capabilities |

### Gateway options (`kind: "gateway"`)

| Key | Default | Meaning |
|---|---|---|
| `mode` | `strict` | `strict` enforces the allow-list; `open` allows any domain (everything else stays in force) |
| `allowed_domains` | none | `"api.example.com"` or `"*.example.com"` |
| `follow_redirects` | `false` | |
| `allow_private` | `false` | when false, private/internal addresses are blocked **at connect time** |
| `max_response_bytes` | 1 MiB | |
| `timeout_ms` | 10000 | |
| `keys[]` | none | `{domain, header, env, prefix}`: inject a secret header for a domain from an environment variable |

## Validation rules

All of these are errors **before anything starts**:

* `version` is not 1; `default_timeout_ms`, `timeout_ms`, `retries` or resilience values are negative (`breaker_failures` may be `-1`);
* a core has no `id` or `kind`, an id is duplicated, or `capabilities` contains an empty name;
* `serve.expose` contains an empty name;
* a role name is empty or contains `:`, a role has no cores, or lists an undeclared core;
* a binding has no capability, is bound twice, has no cores, names an undeclared core or role, has an invalid `mode`, uses `on_error` without `fanout` or with a value other
  than `skip`/`fail`, or uses `single` with more or fewer than one core;
* a wrapper has no capability or kind, or a `placement` other than `outer`/`inner`;
* a subscription has no event, a negative timeout, or names an undeclared core or role;
* `limits.max_depth` is negative.

And at **start** (not parse) the platform additionally refuses to run when: a core fails to start; a binding points at a core that does not provide the capability; a provided capability
has no request contract; a name in `serve.expose` is provided by nothing and is not an event contract; a wrapper kind is unknown; a subscribed core cannot receive events.

## Patterns

**Swap a core.** Two cores, one role; change which one the role lists. Callers never notice.

```json
"roles": { "counting": ["counter-b"] }
```

**Redundancy.** Several cores behind one capability; the first healthy one answers.

```json
{ "capability": "counter.v1.get", "cores": ["counter-a", "counter-b"], "mode": "fallback" }
```

**Aggregation.** Ask everything, keep partial answers.

```json
{ "capability": "counter.v1.get", "cores": ["counter-a", "counter-b"], "mode": "fanout", "on_error": "skip", "timeout_ms": 1500 }
```

**Nesting.** An inner platform mounted like any HTTP core; set `limits.max_depth` on every platform in the chain.

```json
{ "id": "inner", "kind": "http", "options": { "url": "http://inner:8080", "token_env": "INNER_PLATFORM_TOKEN" } }
```

**Expose a narrow facade.** Only the capabilities you list exist for callers:

```json
"serve": { "expose": ["counter.v1.get"] }
```

## Environment variables

The platform reads only the variables your config names (`serve.token_env`, each core's `token_env`, each gateway key's `env`). A core's token is read when the platform
starts; change it by restarting. There is no live config reload.
