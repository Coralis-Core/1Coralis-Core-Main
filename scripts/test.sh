#!/usr/bin/env bash
# Everything verifiable offline: Go (vet + race tests), Python adapter, Node adapter.
set -euo pipefail
cd "$(dirname "$0")/.."
go vet ./... && go test -race ./...
(cd adapters/python && python3 -m unittest -v test_platform_adapter 2>&1 | tail -4)
(cd adapters/node && node --test 2>&1 | grep -E "^# (tests|pass|fail)")
