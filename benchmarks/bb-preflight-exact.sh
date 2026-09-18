#!/usr/bin/env bash
# bv-apal.1 preflight: run only the fixed-clock exact-result stage of the
# latency harness against the maintainer-approved decoder-repair comparator
# baseline, to confirm the /version exclusion clears the 72/72 mismatch before
# committing a worker to the full multi-hour matrix. All paths are literal.
set -euo pipefail

export GOTOOLCHAIN=go1.26.8
export GOFLAGS=-mod=vendor
export GOAMD64=v1
export CGO_ENABLED=1
export GOMAXPROCS=8

test "$(go env GOVERSION)" = go1.26.8
go version

mkdir -p /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918
mkdir -p /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/cli-exact

cd /data/projects/beads_viewer
go build -o /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/current-bv ./cmd/bv
go test -c -o /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/current-e2e.test ./tests/e2e

sha256sum \
  /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/current-bv \
  /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/current-e2e.test \
  /data/projects/beads_viewer/benchmarks/bb-baseline-20260918/comparator-bv \
  > /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/sha256.txt
cat /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/sha256.txt

cd /data/projects/beads_viewer/tests/e2e
set +e
BV_PERF_DIR=/data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/cli-exact \
BV_PERF_BASELINE_BINARY=/data/projects/beads_viewer/benchmarks/bb-baseline-20260918/comparator-bv \
BV_PERF_CURRENT_BINARY=/data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/current-bv \
  /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/current-e2e.test \
  -test.run '^TestPerformanceCLIExactCohorts$' -test.timeout 30m -test.v \
  > /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/cli-exact/run.log 2>&1
rc=$?
set -e
echo "EXACT_RC=${rc}"
grep -cE '^=== RUN   TestPerformanceCLIExactCohorts/' /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/cli-exact/run.log || true
tail -20 /data/projects/beads_viewer/benchmarks/bb-exact-preflight-20260918/cli-exact/run.log
exit "${rc}"
