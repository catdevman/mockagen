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

run() { # run <dir> <outfile>
  (cd "$1" && go test "$PKG" -run='^$' -bench="$BENCH" -benchmem -count=1) >>"$2"
}

echo "==> compiling both sides (keeps build time out of the measured loop)"
(cd "$BASE_DIR" && go test "$PKG" -run='^$' -bench=. -benchtime=1x >/dev/null)
(cd "$HEAD_DIR" && go test "$PKG" -run='^$' -bench=. -benchtime=1x >/dev/null)

echo "==> warm-up round (discarded)"
run "$BASE_DIR" /dev/null
run "$HEAD_DIR" /dev/null

for i in $(seq 1 "$ROUNDS"); do
  echo "==> round $i/$ROUNDS"
  # Swap which side goes first on alternate rounds so that neither one
  # systematically inherits a colder or warmer machine.
  if ((i % 2 == 1)); then
    run "$BASE_DIR" "$OUT_DIR/base.txt"
    run "$HEAD_DIR" "$OUT_DIR/head.txt"
  else
    run "$HEAD_DIR" "$OUT_DIR/head.txt"
    run "$BASE_DIR" "$OUT_DIR/base.txt"
  fi
done

echo "==> done: $OUT_DIR/base.txt $OUT_DIR/head.txt"
