#!/usr/bin/env python3
"""Independent (non-Go) checks of the structural files with the REFERENCE JSON Schema
implementation (pip install jsonschema):
  1. every *.schema.json under contracts/ is a valid draft 2020-12 schema
  2. the config corpus behaves as its file-name prefix says:
       valid-*             -> accepted by the config schema
       invalid-schema-*    -> rejected by the config schema
       invalid-semantic-*  -> accepted by the schema (the Go parser rejects it: cross-references)
usage: validate_structure.py <contracts-dir> <config-corpus-dir>
"""
import glob, json, os, sys
from jsonschema import Draft202012Validator

contracts, corpus = sys.argv[1], sys.argv[2]
bad = 0
for p in sorted(glob.glob(f"{contracts}/*/v*/*.schema.json")):
    try:
        Draft202012Validator.check_schema(json.load(open(p)))
        print("ok   schema", p)
    except Exception as e:  # noqa
        print("FAIL schema", p, e); bad += 1

schema = json.load(open(f"{contracts}/config/v1/platform.config.schema.json"))
v = Draft202012Validator(schema)
files = sorted(glob.glob(f"{corpus}/*.json"))
for p in files:
    name = os.path.basename(p)
    errs = list(v.iter_errors(json.load(open(p))))
    if name.startswith("valid-") or name.startswith("invalid-semantic-"):
        okk = not errs
    elif name.startswith("invalid-schema-"):
        okk = bool(errs)
    else:
        okk = False
    print(("ok   " if okk else "FAIL ") + "config", name)
    bad += 0 if okk else 1
print(f"{len(files)} config files, {bad} problems in total")
sys.exit(1 if bad else 0)
