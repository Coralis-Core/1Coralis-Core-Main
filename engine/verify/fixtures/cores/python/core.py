#!/usr/bin/env python3
"""A core written in Python. It knows nothing about Go or this engine: it only speaks
the wire envelope (one JSON object per line on stdin/stdout) and the contracts.

request : {"capability": "identity.v1.user.get", "input": {"id": 7}}
success : {"output": {...}}
failure : {"error": {"code": "not_found", "message": "..."}}
"""
import json
import sys

USERS = {
    7: {"id": 7, "username": "sara", "name": "Sara", "language": "fa"},
    8: {"id": 8},
    5000000000: {"id": 5000000000, "username": "big"},
}
CHATS = {
    1: {"id": 1, "name": "Team", "language": "fa"},
    2: {"id": 2},
    -1001234567890: {"id": -1001234567890, "name": "Supergroup"},
}


def valid_id_input(inp):
    """identity.v1 input: an object with exactly one integer field "id"."""
    return (
        isinstance(inp, dict)
        and set(inp) == {"id"}
        and isinstance(inp["id"], int)
        and not isinstance(inp["id"], bool)
    )


CAPABILITIES = ["identity.v1.user.get", "identity.v1.chat.get"]


def handle(req):
    cap, inp = req.get("capability"), req.get("input", {})
    if cap in ("core.v1.describe", "core.v1.health"):
        if inp != {}:  # both contracts: input is exactly an empty object
            return {"error": {"code": "invalid_input", "message": "input must be {}"}}
        if cap == "core.v1.health":
            return {"output": {"status": "ok"}}
        return {"output": {"name": "python-fixture", "version": "1.0.0", "capabilities": CAPABILITIES,
                           "publishes": [], "subscribes": []}}
    if cap in ("identity.v1.user.get", "identity.v1.chat.get") and not valid_id_input(inp):
        return {"error": {"code": "invalid_input", "message": "input must be {\"id\": <integer>}"}}
    if cap == "identity.v1.user.get":
        found = USERS.get(inp.get("id"))
    elif cap == "identity.v1.chat.get":
        found = CHATS.get(inp.get("id"))
    else:
        return {"error": {"code": "unavailable", "message": "unknown capability " + str(cap)}}
    if found is None:
        return {"error": {"code": "not_found", "message": "no such id"}}
    return {"output": found}


if __name__ == "__main__":
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        print(json.dumps(handle(json.loads(line))), flush=True)
