# 04 · Getting started

## Requirements

| Needed for | What |
|---|---|
| the platform and its tests | **Go 1.22+** (the main module has no third-party dependencies) |
| the Python adapter and demo core | Python **3.9+** (standard library only) |
| the Node adapter and demo core | Node **18+** (no packages) |
| the race detector (`-race`) | a C compiler; drop the flag if you have none |
| the gRPC module, optional | internet access once, for `go mod tidy` |
| Docker files, optional | Docker with Compose v2 |

Everything works on Linux, macOS and Windows. On Windows use PowerShell or any shell; environment variables are set with
`$env:NAME = "value"` instead of `export NAME=value`. If `python3` opens the Microsoft Store, install Python or use `python`.

## 1. Check the project builds and passes its tests

```bash
go build ./... && go vet ./... && go test -race -count=1 ./...
```

## 2. Run the demo: a platform and a core in two terminals

The repository ships a neutral demo: a core that keeps named counters in memory and implements three capabilities,
`counter.v1.increment`, `counter.v1.get` and `counter.v1.reset` (their contracts are in `domain-contracts/counter/v1/`).

```bash
# terminal 1: the core (Python)
export CORE_TOKEN=s3cret                 # PowerShell: $env:CORE_TOKEN = "s3cret"
python3 examples/counter-demo-python/main.py        # listens on 127.0.0.1:9001

# terminal 2: the platform
export CORE_TOKEN=s3cret
go run ./engine/cmd/platform -config config/platform.json
```

```bash
curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"counter.v1.increment","input":{"name":"visits"}}'
# {"output":{"value":1}}

curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"counter.v1.increment","input":{"name":"visits","by":5000}}'
# {"error":{"code":"limit_exceeded"}}                 <- a domain error declared in the contract (HTTP 422)

curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"counter.v1.increment","input":{}}'
# {"error":{"code":"invalid_input"}}                  <- rejected by the contract; the core never saw it (HTTP 400)
```

`scripts/demo.sh python` and `scripts/demo.sh node` do the same and tear everything down. A Node version of the core is in
`examples/counter-demo-node/`.

PowerShell has no `curl -d` with the same quoting; use:

```powershell
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8080/v1/call -Body '{"capability":"counter.v1.get","input":{"name":"visits"}}'
```

## 3. Build your first core (about ten minutes)

Goal: a core in Python providing `text.v1.reverse`. Every file below was run exactly as shown.

**Step 1: write the contract.** The capability is defined by a file, before any code exists.
Create `domain-contracts/text/v1/reverse.contract.json`:

```json
{
  "$schema": "../../../contracts/contract.schema.json",
  "capability": "text.v1.reverse",
  "version": "1.0.0",
  "kind": "request",
  "idempotent": true,
  "summary": "Return the text written backwards.",
  "input": {
    "type": "object",
    "required": ["text"],
    "additionalProperties": false,
    "properties": {"text": {"type": "string", "maxLength": 1000}}
  },
  "output": {
    "type": "object",
    "required": ["text"],
    "additionalProperties": false,
    "properties": {"text": {"type": "string"}}
  },
  "errors": ["empty_text"],
  "examples": [
    {"name": "simple", "input": {"text": "abc"}, "output": {"text": "cba"}},
    {"name": "empty", "input": {"text": ""}, "error": "empty_text"}
  ],
  "invalid_inputs": [
    {"name": "missing_text", "input": {}},
    {"name": "wrong_type", "input": {"text": 5}}
  ]
}
```

**Step 2: write the core.** Create `cores/text-python/main.py` (the adapter does the HTTP, token check, `describe`, `health` and error mapping):

```python
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "adapters", "python"))
from platform_adapter import Core, CoreError

core = Core("text", token_env="TEXT_TOKEN")        # TEXT_TOKEN: the NAME of the env var that holds the token

@core.capability("text.v1.reverse")
def reverse(inp, meta):
    if inp["text"] == "":
        raise CoreError("empty_text", "nothing to reverse")   # declared in the contract's "errors"
    return {"text": inp["text"][::-1]}

core.serve(host="127.0.0.1", port=9100)
```

**Step 3: mount it.** Create `config/tutorial.json`. `capabilities` is omitted, so the platform asks the core with `core.v1.describe`:

```json
{
  "version": 1,
  "contracts_dirs": ["../domain-contracts"],
  "cores": [
    { "id": "text", "kind": "http", "options": { "url": "http://127.0.0.1:9100", "token_env": "TEXT_TOKEN" } }
  ],
  "serve": { "expose": ["text.v1.reverse"] }
}
```

**Step 4: run.**

```bash
export TEXT_TOKEN=s3cret
python3 cores/text-python/main.py                                   # terminal 1
go run ./engine/cmd/platform -config config/tutorial.json           # terminal 2 (start the core first)
```

**Step 5: call it.**

```bash
curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"text.v1.reverse","input":{"text":"hello"}}'
# {"output":{"text":"olleh"}}                         HTTP 200
curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"text.v1.reverse","input":{"text":""}}'
# {"error":{"code":"empty_text"}}                     HTTP 422, the domain error from the contract
curl -s -X POST 127.0.0.1:8080/v1/call -d '{"capability":"text.v1.reverse","input":{"text":5}}'
# {"error":{"code":"invalid_input"}}                  HTTP 400, rejected by the contract
```

What just happened: the platform loaded your contract from `contracts_dirs`, asked the core what it provides, exposed exactly the capability
listed in `serve.expose`, and checked every input and output against the contract. If you now move the core to another machine, change
its `url`; if you rewrite it in another language, nothing else changes.

Next: [05 Writing cores](05-writing-cores.md) (all the ways to write one), [06 Contracts](06-contracts.md) (everything a contract can say).

## 4. Check everything on your machine with one command

```bash
go run ./scripts/verify
```

It builds, vets and tests the main module, runs the adapter tests, runs a live platform with a real core, tests the gRPC module (needs
internet), and builds the Docker image if Docker exists. It prints PASS / FAIL / SKIP per step and the last log lines of anything that
failed. Details: [14 Testing and verification](14-testing-and-verification.md).
