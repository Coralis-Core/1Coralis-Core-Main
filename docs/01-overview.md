# 01 · Overview

## The problem

You depend on programs you do not own: an open-source service, a proxy, a game server, a legacy service. You want to change
how they are used, combine several of them, hide them behind one controlled interface, or swap one for another later.
The usual options each hurt somewhere:

| Option | Where it hurts |
|---|---|
| **Fork and patch** | You carry the patch forever; every upstream update is a merge fight; the fork leaks into everything. |
| **Plugin system** | Only exists if the program has one, and then it locks you to that program's language and API. |
| **Glue scripts** | Quick, but untyped, unversioned, and each one reaches into the others' internals. |
| **Network proxies / service meshes** | Excellent at moving bytes between services; they know nothing about *what* a call means or whether it is valid. |
| **Workflow engines** | Built for durable, long-running orchestration, not for a synchronous, contract-checked facade. |

## The idea

Put a small **control plane** in front. Each program is wrapped as a **core** that speaks one simple, language-independent
wire format. Everything a core offers is a named, versioned **capability** whose input, output and errors are written down in a
**contract** (a JSON file, not code). The platform mounts cores from one configuration file, checks every call against its
contract on the way in and on the way out, and exposes only the capabilities you list.

Result:

* You **customize without forking**: change behavior at the boundary (bind a capability to a different core, decorate it
  with a wrapper, add a new capability that composes others) while the original program stays untouched and updatable.
* Each piece is **isolated**: it can run in its own process, container and network; it never sees the others.
* Pieces are **swappable**: two programs that satisfy the same contract are interchangeable by editing one line of config.
* The outside world sees **one facade** with an explicit allow-list, one authentication point, and one place to observe.

## What the platform is

A *control plane*, not an engine. It wraps engines (a proxy core, a game server, a legacy service); it does not replace them or run
inside them. It has no domain logic of its own: no notion of chats, users, commands, pages or any customer-facing shape.
That absence is enforced by a test (see [03 Architecture](03-architecture.md#dependency-rules)).

## What it is not

* **Not multi-tenant.** Tenancy, when a project needs it, is decided per project in the top layer.
* **Not a replacement for forking** when you must change a program's internals. It works when the change can be expressed at
  the boundary: calls in, calls out, events, decoration.
* **Not a service mesh, workflow engine, queue or database.** No service discovery, no streaming, no durable workflows,
  no persistence of its own.
* **Not an application.** It has no frontend or backend. Anything that can speak the wire format is your top layer.

## When it fits, when it does not

Good fit:

* a program with no plugin system that you must customize and keep updating;
* several programs in different languages that should look like one product;
* a deliverable for a single customer where the customer must see only a curated set of capabilities;
* places where outbound internet access must be controlled and audited;
* teams that want contracts (not shared code) to be the only coupling between components.

Poor fit:

* you can simply add a feature to the program and upstream it;
* you need sub-millisecond calls between components (the HTTP hop costs tens of microseconds on loopback; see
  [03 Architecture](03-architecture.md#performance));
* you need streaming, long-lived sessions or transactions across cores;
* you need a managed, multi-tenant control panel out of the box.

## Design principles

These shaped every decision and are the lens to read the rest of the docs through.

1. **Independence from any particular core.** The base knows no specific program; cores are replaceable.
2. **Roles come from configuration.** Free labels group cores; the engine knows no fixed role names.
3. **Composability.** Fallback, fan-out, wrapping, overriding and nesting are all configuration.
4. **A swappable gateway.** Outbound access is itself a role any compliant core can play.
5. **Polyglot by design.** Cores are separate processes in any language; the only shared thing is the wire format.
6. **Contracts instead of dependencies.** Components depend on contract files, never on each other's code.
7. **Physical and logical isolation.** One folder, one process, ideally one network per core.
8. **The outer layer is the only face.** Callers see exactly the capabilities you expose, nothing else.
9. **A generic base.** No chat, command, page or customer shape exists in the engine.
10. **Safe by default, strict by opt-out.** Nothing is exposed, limits are explicit, secrets never live in config.

## Status

Alpha. The engine, both transports, the adapters and the contracts are built and tested; see
[14 Testing and verification](14-testing-and-verification.md) for what was run and [16 Limits and roadmap](16-limits-and-roadmap.md)
for what is missing. The most important gap is that it has not yet been run end to end against a real third-party program.
