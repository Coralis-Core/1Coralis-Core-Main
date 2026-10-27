# Documentation

This folder explains the whole project: what it is, how it is built, how to use it, how to extend it, and what is
(and is not) finished. It is written to be read front to back by someone who has never seen the code, but every page
also stands on its own.

## Reading paths

| You want to… | Read |
|---|---|
| Understand what this is and whether it fits your problem | [01 Overview](01-overview.md), then [02 Concepts](02-concepts.md) |
| Understand how it works inside | [03 Architecture](03-architecture.md), [09 Routing and resilience](09-routing-and-resilience.md) |
| Run it and call it for the first time | [04 Getting started](04-getting-started.md) |
| Plug your own program in (any language) | [05 Writing cores](05-writing-cores.md), [06 Contracts](06-contracts.md) |
| Configure a platform | [07 Configuration](07-configuration.md) |
| Call a platform from your own code | [08 Wire protocols](08-wire-protocols.md) |
| Customize behavior without touching a core | [10 Extension points and patterns](10-extension-points-and-patterns.md) |
| Put it in production safely | [11 Security](11-security.md), [12 Deployment](12-deployment.md) |
| Add Go code (in-process cores, wrappers, transports) | [13 Extending the host](13-extending-the-host.md) |
| Check that everything works | [14 Testing and verification](14-testing-and-verification.md) |
| Fix a problem | [15 FAQ and troubleshooting](15-faq-and-troubleshooting.md) |
| Turn a program you do not control into a core | [17 Integrating a program](17-integrating-a-program.md) |
| Know the limits and what is not built | [16 Limits and roadmap](16-limits-and-roadmap.md) |

## Conventions used in these pages

* **Verified** means it was actually run (build, tests, or a live call). **Not verified** is said explicitly where it applies.
  The authoritative list is `STATUS.md` in the repository root.
* Capability names look like `domain.v1.operation`. The examples use a small neutral demo contract set, `counter.v1.*`
  (`increment`, `get`, `reset`), and a `greet.v1.hello` capability in the custom-host example.
* Paths are relative to the repository root. Commands work on Linux, macOS and Windows unless noted; where the shell syntax
  differs, both forms are shown.
* "The platform" is the process built from `engine/cmd/platform` (or your own host program built on the same packages).
  "A core" is any program mounted by it.
