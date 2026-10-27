"""Demo core (Python, standard library only): named counters in memory, implementing the counter.v1.* contracts."""
import os, sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "adapters", "python"))
from platform_adapter import Core, CoreError

MAX = 1000
core = Core("counter-demo", token_env="CORE_TOKEN")
counters = {}

@core.capability("counter.v1.increment")
def increment(inp, meta):
    value = counters.get(inp["name"], 0) + inp.get("by", 1)
    if value > MAX:
        raise CoreError("limit_exceeded", "a counter cannot exceed %d" % MAX)
    counters[inp["name"]] = value
    return {"value": value}

@core.capability("counter.v1.get")
def get(inp, meta):
    return {"value": counters.get(inp["name"], 0)}

@core.capability("counter.v1.reset")
def reset(inp, meta):
    return {"removed": counters.pop(inp["name"], None) is not None}

if __name__ == "__main__":
    core.serve(host=os.environ.get("HOST", "127.0.0.1"), port=int(os.environ.get("PORT", "9001")))
