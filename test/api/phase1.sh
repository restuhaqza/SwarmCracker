#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
go test ./test/api -run TestPhase1 -count=1 -v
