#!/usr/bin/env python3
"""Independent check of every contract file using the REFERENCE JSON Schema
implementation (pip install jsonschema). Nothing here is Go.

1. each *.contract.json is valid against contracts/contract.schema.json
2. each embedded schema is itself a valid 2020-12 schema
3. each example validates against its own input/output/payload schema
"""
import glob
import json
import sys

from jsonschema import Draft202012Validator

root = sys.argv[1] if len(sys.argv) > 1 else "contracts"
meta_path = sys.argv[2] if len(sys.argv) > 2 else f"{root}/contract.schema.json"
meta = json.load(open(meta_path))
Draft202012Validator.check_schema(meta)
meta_v = Draft202012Validator(meta)

bad = 0
files = sorted(glob.glob(f"{root}/*/v*/*.contract.json"))
for path in files:
    c = json.load(open(path))
    problems = [e.message for e in meta_v.iter_errors(c)]
    for key in ("input", "output", "payload"):
        if key in c:
            try:
                Draft202012Validator.check_schema(c[key])
            except Exception as e:  # noqa
                problems.append(f"{key}: invalid schema: {e}")
    for ex in c.get("examples", []):
        for key in ("input", "output", "payload"):
            if key in ex and key in c:
                for e in Draft202012Validator(c[key]).iter_errors(ex[key]):
                    problems.append(f"example {ex['name']}/{key}: {e.message}")
        if "error" in ex and ex["error"] not in c.get("errors", []):
            problems.append(f"example {ex['name']}: undeclared error {ex['error']}")
    for neg_key, schema_key in (("invalid_inputs", "input"), ("invalid_payloads", "payload")):
        for ex in c.get(neg_key, []):
            if not list(Draft202012Validator(c[schema_key]).iter_errors(ex[schema_key])):
                problems.append(f"{neg_key}/{ex['name']}: is actually VALID (negative examples must violate the schema)")
    print(("FAIL " if problems else "ok   ") + path)
    for p in problems:
        print("     -", p)
    bad += bool(problems)

print(f"{len(files)} contract files, {bad} failing")
sys.exit(1 if bad else 0)
