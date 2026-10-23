#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
go test ./test/api -run TestPhase2 -count=1 -v
