#!/usr/bin/env bash
# bv-apal.1 full paired latency matrix, run on hz3.
#
# Runs the identity-pinned harness unchanged:
#   scripts/benchmark.sh          sha256 896d7e2518217e484b41d53176e6fb4d354a0317840d812f26c0a9061c168f7b
#   tests/artifacts/perf/verify.sh sha256 939b66ccb210c92927044c05760fc2d206c4eeb170ce48debb6de17bda5b2199
#
# Baseline is the maintainer-approved decoder-repair comparator, staged inside
# the synced project tree so a worker migration cannot fail on an unreachable
# absolute path. GOMAXPROCS is left at the host's natural 64 threads, matching
# the earlier recorded cohort. All paths are literal.
set -uo pipefail

export GOTOOLCHAIN=go1.26.8
export GOFLAGS=-mod=vendor
export GOAMD64=v1
export CGO_ENABLED=1

OUT=/data/projects/beads_viewer/benchmarks/bb-p1-matrix-20260918
LOG=/data/projects/beads_viewer/benchmarks/bb-p1-matrix-20260918.log
SENTINEL=/data/projects/beads_viewer/benchmarks/bb-p1-matrix-20260918.done

{
  echo "=== bv-apal.1 matrix run ==="
  date -u '+start_utc=%Y-%m-%dT%H:%M:%SZ'
  echo "host=$(hostname) nproc=$(nproc) gomaxprocs_default=$(nproc)"
  free -g | sed -n '1,2p'
  cd /data/projects/beads_viewer || exit 2
  echo "head=$(git rev-parse HEAD)"
  echo "dirty_go=$(git diff --stat -- '*.go' go.mod go.sum | wc -l)"
  go version
  sha256sum scripts/benchmark.sh tests/artifacts/perf/verify.sh
  sha256sum benchmarks/bb-baseline-20260918/comparator-bv benchmarks/bb-baseline-20260918/comparator-ui.test
  echo "=== harness ==="
  bash scripts/benchmark.sh latency \
    /data/projects/beads_viewer/benchmarks/bb-baseline-20260918/comparator-bv \
    /data/projects/beads_viewer/benchmarks/bb-baseline-20260918/comparator-ui.test \
    "${OUT}"
  rc=$?
  echo "HARNESS_RC=${rc}"
  date -u '+end_utc=%Y-%m-%dT%H:%M:%SZ'
  echo "${rc}" > "${SENTINEL}"
} > "${LOG}" 2>&1
