#!/usr/bin/env bash
# gRPC module tests (needs network to fetch google.golang.org on first run).
set -euo pipefail
cd "$(dirname "$0")/../engine/contrib/grpc"
go mod tidy
go vet ./...
go test -race -count=1 ./...
