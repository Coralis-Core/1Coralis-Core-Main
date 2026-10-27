# adapters
Per-language connection to the platform (not tied to any system). Stdlib only.
- python/platform_adapter.py — `Core(name)`, `@core.capability("x.v1.y")`, `CoreError(code)`, `core.serve(port=)`
- node/platform_adapter.js — same API.
Both serve POST /v1/call, answer core.v1.describe / core.v1.health, check the bearer token (token_env),
and map CoreError to HTTP 422 with the contract-declared code. Contract validation is done by the platform.
Demo: `scripts/demo.sh python` or `scripts/demo.sh node`.

Your own capability contracts live in `domain-contracts/` (listed in config as `contracts_dirs`);
`contracts/` holds only the fixed structural contracts.
