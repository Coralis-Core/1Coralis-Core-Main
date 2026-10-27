#!/usr/bin/env bash
# Live end-to-end demo: platform + a Python (or Node) core. Usage: scripts/demo.sh [python|node]
set -euo pipefail
cd "$(dirname "$0")/.."
LANG_CORE=${1:-python}
export CORE_TOKEN=${CORE_TOKEN:-s3cret}
go build -o /tmp/platform ./engine/cmd/platform
if [ "$LANG_CORE" = node ]; then node examples/counter-demo-node/main.js & else python3 examples/counter-demo-python/main.py & fi
CORE=$!
sleep 1
/tmp/platform -config config/platform.json & PLAT=$!
trap 'kill $CORE $PLAT 2>/dev/null' EXIT
sleep 1.5
call(){ echo "> $1"; curl -s -X POST 127.0.0.1:8080/v1/call -d "$1"; echo; }
call '{"capability":"counter.v1.increment","input":{"name":"visits"}}'
call '{"capability":"counter.v1.increment","input":{"name":"visits","by":999}}'
call '{"capability":"counter.v1.get","input":{"name":"visits"}}'
call '{"capability":"counter.v1.reset","input":{"name":"visits"}}'
call '{"capability":"counter.v1.increment","input":{}}'
