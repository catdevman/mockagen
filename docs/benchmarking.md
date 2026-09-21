# Benchmarking

## What the suite covers

All benchmarks live in `./cmd/mockagen` and run under `make bench`.

| Benchmark | Measures |
|---|---|
| `BenchmarkGenerateFakes/generate_fake_<n>` | Generation alone, across record counts |
| `BenchmarkGenerateRecords` | Generation alone, on the mixed-type schema the two below share |
| `BenchmarkWriteRecords/<format>/<n>` | The writer alone, over records generated up front |
| `BenchmarkPipeline/<format>` | Generation and writing together, the way `main` runs them |

The last three share one schema (`benchConfig` in `bench_test.go`) so they
decompose cleanly: `Pipeline` minus `WriteRecords` is the generation share,
and `WriteRecords` is what a format costs on top of it.

One op is a whole batch of records, not a single record, so the measurement
includes opening the file and `Close()` - parquet's footer and row-group
flush are a real per-file cost that a per-record loop would amortise away.
Each op rewrites the same path, so the file never grows past one batch no
matter how high `b.N` climbs.

### Metrics the format benchmarks add

`WriteRecords` and `Pipeline` report two metrics beyond the usual three:

- **`B/s`** - throughput, from `b.SetBytes` over the size of the output file.
  Formats legitimately produce very different amounts of output for the same
  records (parquet compresses, fixed-width pads), so wall-clock alone would
  credit a format for writing less. Throughput normalises that away.
- **`B/rec`** - bytes of output per record, so the suite records what each
  format costs on disk instead of it having to be measured by hand.

Both are measured from a priming batch written outside the timed region.
Note that `b.ResetTimer` deletes user-reported metrics, so `B/rec` is
reported after the timed loop rather than next to `SetBytes`.

### What they currently say

Writing a 1,000-record batch of the benchmark schema, against ~10.7 ms to
generate it:

| format | write | throughput | output |
|---|---:|---:|---:|
| `fluent` | 0.76 ms | 382 MB/s | 291 B/rec |
| `json` | 0.73 ms | 350 MB/s | 250 B/rec |
| `fixed` | 1.88 ms | 273 MB/s | 513 B/rec |
| `parquet` | 1.06 ms | 94 MB/s | 99 B/rec |
| `yaml` | 11.4 ms | 21 MB/s | 241 B/rec |

Generation dominates: writing costs 7-18% of generating for everything except
`yaml`, which is slightly *more* expensive than generation itself (73k allocs
per batch against json's 1k, since `yaml.v3` builds a node tree per record).
So a writer change is rarely where a run's wall-clock goes -
`BenchmarkGenerateRecords` is the number to beat.

Throughput reorders the picture against raw time: `fixed` looks slow at 1.88
ms but is pushing twice the bytes, and `parquet` is the slowest per byte
while producing by far the smallest file - a per-file footer and row-group
flush it only amortises at size (~6x slower than json at 10 records, ~1.4x at
10,000).

Numbers above are from a dev laptop; absolute values are not comparable
across machines, which is the point of the next section.

## Why the numbers are compared, not stored

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
- `GOMAXPROCS=4` and `GOTOOLCHAIN=local` pinned, so parallelism and compiler are
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

The published series carries `sec/op`, `B/op` and `allocs/op` only; `B/s` and
`B/rec` are reported in the A/B comparison but not charted, since the
action's Go parser reads just those three.

That action's own alerting is deliberately disabled: it compares against the
previously *stored* run, which is a cross-machine comparison and therefore the
noisy kind. Alerting comes from the A/B step instead.

## Thresholds

| metric | threshold | significance gate |
|---|---|---|
| `sec/op` | +10% | yes - benchstat must not call it noise (`~`) |
| `B/s` | -10% | yes - same, and inverted: a *drop* in throughput is the regression |
| `B/op` | +1% | no |
| `allocs/op` | +1% | no |
| `B/rec` | +1% | no |

Wall-clock is noisy even with A/B, so it needs a wide margin *and* a p-value;
`B/s` is derived from wall-clock and gets the same treatment, with the
direction flipped since higher is better there.
`B/rec` is deterministic for a fixed schema - a shift means a writer changed
what it emits - so it joins the counters on the tight, ungated threshold.
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
