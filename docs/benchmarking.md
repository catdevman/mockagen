# Benchmarking

`go test -bench` numbers from GitHub-hosted runners are not comparable across
runs. The runners are shared VMs on mixed hardware, so the same code can differ
by 10-30% between two runs on different machines. Any pipeline that stores a
number today and compares it to a number from last week will mostly report
noise.

This setup works around that in two parts.

## 1. Same-job A/B (the trustworthy signal)

`.github/workflows/benchmark.yml` checks out both the new commit and its
baseline, then `scripts/bench-ab.sh` runs them **interleaved on one runner**:

- 8 rounds, each running base once and head once
- the order flips on alternate rounds, so neither side systematically gets the
  colder machine
- a discarded warm-up round first, and both sides compiled before the loop
- `GOMAXPROCS=2` and `GOTOOLCHAIN=local` pinned, so parallelism and compiler are
  identical on both sides and benchmark names stay stable over time

Thermal drift and noisy neighbours then hit both sides roughly equally, and the
delta means something even though the absolute numbers don't.

`benchstat` summarises the 8 samples per side and `scripts/bench-report.go`
applies the thresholds.

`bench-report.go` is a standalone script carrying `//go:build ignore`, so it
stays out of `go build ./...` and `go test ./...` and pulls in nothing beyond
the standard library.

## 2. Historical trend (directional only)

Every push to `main` also publishes the head medians to the `gh-pages` branch
via `benchmark-action/github-action-benchmark`, giving charts at
`https://catdevman.github.io/mockagen/dev/bench/`.

That action's own alerting is deliberately disabled: it compares against the
previously *stored* run, which is a cross-machine comparison and therefore the
noisy kind. Alerting comes from the A/B step instead.

## Thresholds

| metric | threshold | significance gate |
|---|---|---|
| `sec/op` | +10% | yes - benchstat must not call it noise (`~`) |
| `B/op` | +1% | no |
| `allocs/op` | +1% | no |

Wall-clock is noisy even with A/B, so it needs a wide margin *and* a p-value.
Allocation counters are deterministic - running identical code on both sides
produces 0.00% drift there while time still wanders a few percent - so they get
a tight threshold and no significance gate. In practice `allocs/op` is the
metric that actually catches regressions early.

## How you find out

- **Pull requests** get a single sticky comment, rewritten in place on each push.
- **`main` regressions** open (or comment on) an issue labelled
  `benchmark-regression`, assigned to the repo owner and @-mentioning them, which
  is what generates the email. Reuses one open issue rather than filing a new one
  per commit.
- Every run writes the full table to the **job summary** and uploads
  `base.txt`, `head.txt`, and `compare.csv` as an artifact for 90 days.

The workflow does not fail on regression - it reports.

## One-time setup

1. Push this to `main`. The first run creates the `gh-pages` branch.
2. Repo **Settings → Pages → Source: Deploy from a branch → `gh-pages` / root**.
3. Repo **Settings → Actions → General → Workflow permissions**: ensure
   *Read and write permissions* is enabled so the workflow can push results and
   open issues.

## Running it locally

```sh
make bench                        # one pass over the working tree
make bench-compare BASE=main    # same interleaved A/B as CI, then the report
ROUNDS=10 make bench-compare BASE=HEAD~1
```

`make bench-compare BASE=HEAD` is a useful control: identical code on both sides
should report no regressions, and shows you how much noise your machine has.
