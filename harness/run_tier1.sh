#!/bin/sh
# Tier 1 for mcpm (architecture.md 11.1). Pinned with harness/.
#
# 1. Unit tests in cmd/ and internal/ (agent-writable source paths) run first,
#    without CRUCIBLE_RESULTS and CRUCIBLE_RESULTS_NONCE in their environment,
#    so their test binaries never get the result file path or the nonce.
#    Any failure stops here with a non-zero exit.
# 2. The contract tests in harness/ then run with both variables and write the
#    result file. They run last, so their write is the final one.
#
# A unit test still runs as the same OS user and could read a parent process's
# environment; that residual is recorded under ## Gaps in contracts/SPEC.md.
set -eu

cd "$(dirname "$0")/.."

(
	unset CRUCIBLE_RESULTS CRUCIBLE_RESULTS_NONCE
	exec go test -count=1 -v ./cmd/... ./internal/...
)

exec go test -count=1 -run '^TestCrucible_INV_' ./harness/...
