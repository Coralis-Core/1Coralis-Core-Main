# 17 · Integrating a program ("program X")

This page answers one question for **any** program you do not control, which we will call *program X*: how does it become part of a platform, and how is it configured and customized?
Nothing about X is assumed. X might be a service, a command-line tool, a daemon, a library, a game server or a legacy system. The method is the same.

## The method in six steps

1. **Find X's boundary.** What can you do *to* X from outside, and what does X do that others may want to know about?
2. **Choose capabilities.** Name the few verbs a caller actually needs, from the caller's point of view.
3. **Write contracts** for them (inputs, outputs, errors, examples).
4. **Write a thin core** that translates between the contract and whatever X offers.
5. **Mount and expose** it in the config.
6. **Customize at the boundary** (bindings, wrappers, gateway, events, a complement) instead of changing X.

X itself is never modified. That is what keeps it updatable.

## Step 1: find X's boundary

| X offers… | The core does… | Notes |
|---|---|---|
| an HTTP or RPC API | calls it | the simplest case: the core is a translator |
| a command-line interface | runs the command with arguments built from the input, parses its output | validate and escape arguments; never build a shell string from input |
| a socket or message protocol | speaks it | keep one connection or pool inside the core |
| files it reads or writes | writes or reads them | the core owns those paths; X's files are not exposed to callers |
| a database it owns | goes through X's own interface where there is one; direct access only if you accept coupling to X's schema | a schema change in X breaks the core, not your callers |
| nothing but being a library in your language | runs in-process as a Go core, or in a small process of its own | see [13](13-extending-the-host.md) |
| notifications (webhooks, logs, callbacks) | turns them into **events** (`POST /v1/event`, or a subscription) | the core is the only place that knows X's format |

If X offers nothing at all from outside, it cannot be a core; put the integration where X *can* be reached, or change what you ask of X.

## Step 2: choose capabilities

* One capability per verb the caller cares about, named for the caller: `x.v1.create_job`, not `x.v1.post_to_internal_endpoint`.
* Keep them few and coarse enough that X's internals do not leak; fine-grained enough that each has one clear meaning.
* Do **not** create a catch-all (`x.v1.do` with a free-form input). It cannot be validated, retried safely or swapped.
* Do **not** expose X's administrative or dangerous operations unless a caller truly needs them, and then as separate capabilities you can leave out of `serve.expose`.
* The domain name (`x`) is yours; the `v1` is the contract version ([06](06-contracts.md#naming-and-versioning)).

## Step 3: write the contracts

For each capability one file under your contracts folder: closed objects, bounded values, the domain errors X can produce (`not_found`, `limit_exceeded`, ...) declared in `errors`,
at least one example, and negative inputs. Decide `idempotent` honestly: *does repeating the call after it may already have run change anything further?*
(reading is idempotent; "set to value V" is; "append a record" is not).

## Step 4: write a thin core

A sketch in Python; `call_x` is the only part that knows X:

```python
import sys
sys.path.insert(0, "adapters/python")
from platform_adapter import Core, CoreError

core = Core("x", token_env="X_CORE_TOKEN")        # the NAME of the env var that holds this core's token

def call_x(operation, payload):
    """Your code: talk to program X in whatever way X allows (HTTP, a command, a socket, ...)."""
    raise NotImplementedError

@core.capability("x.v1.operation")
def operation(inp, meta):                          # inp already satisfies the contract
    try:
        reply = call_x("operation", {"value": inp["value"]})
    except ConnectionRefusedError:
        raise CoreError("unavailable", "X is not running")     # nothing was done: safe to retry or fall back
    if reply.get("missing"):
        raise CoreError("not_found")                            # a domain error declared in the contract
    return {"result": reply["result"]}

core.serve(host="127.0.0.1", port=9001)
```

Rules that keep the core honest:

* **Classify failures by what you know about delivery.** Could not reach X, nothing sent → `unavailable`. Sent it and do not know whether X acted (timeout, connection dropped) → let it surface as
  `maybe_delivered` (a timeout or a 5xx does this automatically). Something X reported as a normal outcome → a declared domain error.
* **Keep it thin.** No business rules the platform could express as a wrapper or a complement; the core only translates.
* **Secrets** (X's credentials) come from environment variables read by the core, never from the config or the contract.
* **No state of its own** unless it has to; X owns the truth.
* **Bind it to a private address**; only the platform should reach it ([11](11-security.md)).

## Step 5: mount and expose

```json
{
  "version": 1,
  "contracts_dirs": ["../domain-contracts"],
  "cores": [
    { "id": "x", "kind": "http",
      "options": { "url": "http://x-core:9001", "token_env": "X_CORE_TOKEN", "timeout_ms": 3000 } }
  ],
  "serve": { "token_env": "PLATFORM_TOKEN", "expose": ["x.v1.operation"] }
}
```

Deploy X and its core together on their own network, with no route to the internet, and let nothing else share it ([12](12-deployment.md#the-isolation-design)).
Callers now see one capability, `x.v1.operation`, with a contract and a token. They cannot tell what X is.

## Step 6: customize without touching X

| You want to… | Use | Where |
|---|---|---|
| change how one operation behaves | a different core bound to that capability (an override), or a wrapper | config, [10](10-extension-points-and-patterns.md#overrides) |
| run a rule *before* X sees a request (validation, authorization, rate limit) | an **outer** wrapper | host code, [13](13-extending-the-host.md) |
| adjust or redact X's answer | a wrapper's "after" part | same |
| combine X with another program Y into one new operation | a **complement** core that calls both | [10](10-extension-points-and-patterns.md#complements) |
| keep working when X is down | a second core behind the same capability with `fallback` | [07](07-configuration.md#patterns), [09](09-routing-and-resilience.md) |
| compare or aggregate several sources | `fanout` plus a complement that merges | [09](09-routing-and-resilience.md#fan-out) |
| let X (or its core) reach an outside service | the gateway: the core calls `gateway.v1.request` through the platform | [10](10-extension-points-and-patterns.md#gateway) |
| react when something happens in X | the core publishes an event; other cores subscribe | [10](10-extension-points-and-patterns.md#events) |
| replace X with a different program later | a role: both cores satisfy the same contract; change the role's member | [07](07-configuration.md#roles) |
| hand a customer a product | expose only a complement's capabilities | [10](10-extension-points-and-patterns.md#complements) |

## Updating X

Because X is unmodified, you update it the normal way. If its behavior or interface changes, only the thin core changes; callers and contracts do not, as long as the contract still holds.
The contract's examples are your regression test: run them against the core after every upgrade of X ([14](14-testing-and-verification.md#testing-a-core-you-wrote)).

## Several programs behind one product

Give each its own core, its own network and its own contracts. Define a role per *kind of job* (`"roles": {"storage": ["x", "y"]}`), bind capabilities to roles, and put the product's shape in a complement
that is the only thing you expose. Each program can then be replaced independently.

## Checklist

1. X's boundary identified; nothing in X changed.
2. A small set of caller-oriented capabilities; no catch-all.
3. Contracts with closed schemas, declared errors, honest `idempotent`, examples and negative inputs.
4. A thin core: failures classified, secrets from the environment, bound to a private address.
5. Mounted with a timeout that fits X; only the intended capabilities exposed; a token set.
6. X and its core isolated on their own network; outbound traffic only through the gateway.
7. Conformance examples run against the core, and again after every upgrade of X.
8. Customizations expressed as bindings, wrappers, events or a complement, never as a patch to X.
