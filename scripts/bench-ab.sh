#!/usr/bin/env bash
#
# Run the Go benchmarks alternately in two working trees.
#
# GitHub-hosted runners are shared VMs: absolute ns/op drifts 10-30% between
# runs on different hardware. Interleaving base and head on the *same* runner in
# the *same* job means thermal drift and noisy neighbours hit both sides
# equally, so the A/B delta stays meaningful even when the absolute numbers are
# not comparable across runs.
#
# Usage: BASE_DIR=... HEAD_DIR=... scripts/bench-ab.sh
#
set -euo pipefail

BASE_DIR=${BASE_DIR:?set BASE_DIR to the checkout of the baseline commit}
HEAD_DIR=${HEAD_DIR:?set HEAD_DIR to the checkout of the new commit}
OUT_DIR=${OUT_DIR:-bench-results}
ROUNDS=${ROUNDS:-8}
BENCH=${BENCH:-.}
PKG=${PKG:-./cmd/mockagen}

# Pin the parallelism so benchmark names (which carry a -N suffix) stay stable
# even if GitHub changes the default runner size, and so the historical series
# on gh-pages compares like with like.
export GOMAXPROCS=${GOMAXPROCS:-2}
# Never let a `toolchain` directive pull a different compiler for one side.
export GOTOOLCHAIN=${GOTOOLCHAIN:-local}

mkdir -p "$OUT_DIR"
OUT_DIR=$(cd "$OUT_DIR" && pwd)
: >"$OUT_DIR/base.txt"
: >"$OUT_DIR/head.txt"

# PASS_TIMEOUT caps a single benchmark pass. A benchmark that hangs - e.g. one
# that leaves a worker pool blocked on an undrained channel, so b.N climbs
# without bound - would otherwise burn the entire job timeout on one side. This
# is a real hazard for the baseline side, which runs whatever code was already
# committed.
PASS_TIMEOUT=${PASS_TIMEOUT:-600}

run() { # run <dir> <outfile>
  if ! (cd "$1" && timeout --kill-after=30s "$PASS_TIMEOUT" \
        go test "$PKG" -run='^$' -bench="$BENCH" -benchmem -count=1) >>"$2"; then
    echo "ERROR: benchmark pass in $1 failed or exceeded ${PASS_TIMEOUT}s" >&2
    return 1
  fi
}

echo "==> compiling both sides (keeps build time out of the measured loop)"
(cd "$HEAD_DIR" && go test "$PKG" -run='^$' -bench=. -benchtime=1x >/dev/null)
(cd "$BASE_DIR" && go test "$PKG" -run='^$' -bench=. -benchtime=1x >/dev/null) || true

# The head side must work - that is the code under test.
echo "==> warm-up: head (discarded)"
run "$HEAD_DIR" /dev/null

# The baseline is whatever was already committed, so it may not build, may have
# no benchmarks, or may hang. None of those should sink the run: an empty
# base.txt makes the report fall back to head-only ("new, no baseline"), which
# is still worth publishing.
echo "==> warm-up: base (discarded)"
BASE_OK=1
if ! run "$BASE_DIR" /dev/null; then
  echo "WARNING: baseline benchmarks unusable (build failure, hang, or none present)." >&2
  echo "WARNING: continuing with head-only results; no A/B comparison this run." >&2
  BASE_OK=0
fi

for i in $(seq 1 "$ROUNDS"); do
  echo "==> round $i/$ROUNDS"
  # Swap which side goes first on alternate rounds so that neither one
  # systematically inherits a colder or warmer machine.
  if ((BASE_OK == 0)); then
    run "$HEAD_DIR" "$OUT_DIR/head.txt"
  elif ((i % 2 == 1)); then
    run "$BASE_DIR" "$OUT_DIR/base.txt"
    run "$HEAD_DIR" "$OUT_DIR/head.txt"
  else
    run "$HEAD_DIR" "$OUT_DIR/head.txt"
    run "$BASE_DIR" "$OUT_DIR/base.txt"
  fi
done

echo "==> done: $OUT_DIR/base.txt $OUT_DIR/head.txt"
