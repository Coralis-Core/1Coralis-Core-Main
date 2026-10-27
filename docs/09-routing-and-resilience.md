# 09 · Routing and resilience

How the platform decides *who* serves a call, and what it does when something goes wrong. Everything here lives in `engine/base/capability`.

## The provider chain

For each capability the router builds an ordered chain of **providers** (cores):

* with a **binding**: exactly the cores it lists (roles expanded), in the listed order, minus any that do not actually implement the capability (startup refuses that case);
* **without a binding**: every core that provides the capability, in the order the cores appear in the config.

## Modes

| Mode | Behavior | Typical use |
|---|---|---|
| `single` | exactly one provider; never falls back | the one correct implementation, or a write that must not be duplicated |
| `fallback` | first provider that works; later ones only when the earlier failure was *retriable* | redundancy, migrations |
| `fanout` | all providers at once; every answer returned | aggregation, comparing sources |
| *(empty)* | lenient: the same chain, usable as a single-result call or as a fan-out | the default when you do not care |

Calling a fan-out capability as a single-result call, or the reverse, is refused (`fanout_only`, `not_fanout`) unless the mode is empty.

## What happens in one single-result call

```
outer wrappers
   │
   ▼
timeout starts (binding timeout_ms, else default_timeout_ms)       ← covers the WHOLE call, retries and fallbacks included
   │
   ▼
for each provider in the chain:
     circuit open?  ──yes──► treat as `unavailable`, try the next
     │ no
     ▼
     attempt (up to 1 + retries times)
        inner wrappers → provider call (panics are caught)
        success ──► reset the provider's failure count, return the answer
        failure ──► classify (below)
              not retriable ──► return the error NOW (no fallback)
              retriable     ──► wait retry_delay_ms, try again; when attempts are used up, go to the next provider
   │
   ▼
every provider failed ──► return the last error (`unavailable` or `maybe_delivered`)
```

Important consequences:

* **One timeout budget for the whole call.** If the first provider hangs for the full budget, there is no time left for a fallback. Set `timeout_ms` with that in mind.
* **A domain error is final.** `limit_exceeded` or `not_found` from the first provider is returned to the caller; it does not trigger fallback (the core answered; trying another would change the meaning).
* A panic inside an in-process core is caught and reported as `maybe_delivered`.

## Retry safety

The platform classifies every failed attempt by *what is known about delivery*:

| Failure | Code | Retry / fallback allowed? |
|---|---|---|
| could not connect, circuit open, no provider reachable | `unavailable` | **always**: nothing was sent |
| timeout after sending, connection reset, unreadable answer, 5xx, panic in-process | `maybe_delivered` | **only if the contract says `"idempotent": true`** |
| a domain or validation error | its own code | never |

If a `maybe_delivered` failure meets a non-idempotent capability, the call ends with that error: **the platform will not guess** whether doing a write twice is harmless.
Declare `idempotent: true` in the contract exactly when repeating the call after it may already have been applied changes nothing further (reads, "set to X", "delete if present").

## Retries

`retries` (binding or `resilience`, default 0) is the number of *extra* attempts per provider; `retry_delay_ms` (default 50) separates them. A binding's non-zero value wins over the platform default.
Retries multiply load on a struggling core; keep them small and rely on the breaker.

## The circuit breaker

Per core (keyed by core id), counting **consecutive infrastructure failures** (`unavailable` or `maybe_delivered`; domain errors do not count):

* a success resets the count;
* after `breaker_failures` consecutive failures (default 5) the circuit **opens** for `breaker_cooldown_ms` (default 30 s): calls skip that core immediately as `unavailable`;
* when the cooldown ends the core is tried again, and it needs another full run of failures to open again;
* `breaker_failures: -1` switches the breaker off.

The breaker protects a failing core from being hammered and protects callers from waiting on it. It does not replace health checks: `/healthz` reflects what each core reports.

## Fan-out

* All providers are called concurrently, each through the same attempt logic (breaker, retries, inner wrappers), under a shared deadline of the binding's `timeout_ms`.
* The answer lists every source in chain order, each as `output` or `error` ([08](08-wire-protocols.md#fan-out-answers)). Partial results are normal.
* `on_error: "skip"` (default) returns the partial answer; `on_error: "fail"` fails the whole call if any source failed.
* The platform does **not** merge results: merging is domain logic, so it belongs in a complement core.
* An **outer** wrapper on a fan-out can only *gate* it (inspect or change the input, refuse); it never rewrites the per-source results. An **inner** wrapper runs per source.

## Timeouts and cancellation

`default_timeout_ms` (default 10 s) applies when a binding sets none. The caller's own cancellation also stops the work. A remote core's HTTP client additionally has `options.timeout_ms`.
In a fan-out, `timeout_ms` is each source's budget because they run concurrently.

## Health

Each core can report `ok`, `degraded` or `down` (`core.v1.health`; in-process cores may implement `Health`). The platform's own status is `ok` when all are ok,
`down` when all are down, `degraded` otherwise. Health is for operators and probes; routing decisions use the breaker, not the health status.

## Observability

* Failed attempts are logged with the capability, provider, attempt number and error.
* A contract violation by a core (undeclared error code, invalid output) is logged and surfaces to the caller only as `internal`.
* Every call carries a `trace_id`; pass your own to correlate logs across platforms and cores.
* Logs use Go's `slog` text format on standard error.
