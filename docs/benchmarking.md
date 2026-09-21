# Benchmarking

## What the suite covers

All benchmarks live in `./cmd/mockagen` and run under `make bench`.

| Benchmark | Measures |
|---|---|
| `BenchmarkGenerateFakes/generate_fake_<n>` | Generation alone, across record counts, on a small 5-column schema |
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

Writing a 1,000-record batch of the benchmark schema, against ~3.6 ms to
generate it:

| format | write | throughput | output | allocs |
|---|---:|---:|---:|---:|
| `yaml` | 0.46 ms | 535 MB/s | 248 B/rec | 20 |
| `json` | 0.62 ms | 398 MB/s | 250 B/rec | 6 |
| `fluent` | 0.66 ms | 437 MB/s | 291 B/rec | 10 |
| `parquet` | 1.06 ms | 93 MB/s | 99 B/rec | 1,293 |
| `fixed` | 1.84 ms | 283 MB/s | 513 B/rec | 4,009 |

`yaml`, `json` and `fluent` allocate a flat handful per *batch* rather than
per record, so their allocation counts do not grow with the row count the way
`fixed` and `parquet` still do.

Generation still dominates, though less than it did: writing a batch costs
13-50% of generating it, against 4-17% before the generator was fixed (see
below). `BenchmarkGenerateRecords` remains the number to beat.

### A note on the `GenerateFakes` series

Its schema used to carry go-faker *tag* names (`username`, `first_name`,
`date`) where `mapToFaker` is keyed on Mockaroo *type* names (`Username`,
`First Name`, `Datetime`). An unknown type is not an error anywhere in
mockagen: `mapToFaker` returns `""`, the struct field gets an empty faker
tag, and faker fills it with a plain random string. The benchmark ran fine
and measured the wrong thing.

Fixing the type names made it 2.2x *faster* (`generate_fake_1000`: 5.69 ms to
2.64 ms), because faker's random-string fallback builds a string a character
at a time through the locked global RNG - more expensive than the real
generators it stood in for. The gh-pages series for `GenerateFakes/*`
therefore has a one-off discontinuity at that commit which is not a
performance change. `TestBenchmarkSchemaTypesAreKnown` now fails the build if
any benchmark schema drifts back.

The same silent fallback applies to real user configs: a typo in a `type`
field yields random strings rather than an error. Worth fixing separately.

### Generation

Generation was ~10.7 ms per 1,000 records and is now ~3.6 ms. A CPU profile
found the cost was not spread across the generators at all: 39% of all
samples sat in go-faker's `Lorem.sentence`, which calls
`RandomInt(0, len(wordList)-1, 6)` - and that runs `rand.Perm` over the
**entire 250-word list** to choose 6 words. Every sentence therefore cost 250
calls to faker's mutex-guarded global RNG plus a 250-int allocation, then ran
the first word through `golang.org/x/text`'s Unicode title caser.

`provider` now registers its own `Word`, `Sentence` and `Paragraph`,
overriding faker's tags rather than filling a gap as the rest of the package
does. They index into the word list directly through `math/rand/v2`, whose
top-level functions use per-P state and take no lock. That alone was **2.9x**
on this schema - more than the 39% the profile attributed to it, because
removing 250 lock acquisitions per record also relieves contention for every
other column.

What is left is faker's tag machinery: `decodeTags` is now ~47% of the
profile, re-parsing struct tags (string map lookups, `strings.ToLower`) for
every field of every record, and faker's locked global RNG is still the
ceiling on parallelism. The worker cap was re-measured at volume:

| workers | 1,000 rec | 100,000 rec | 1,000,000 rec |
|---|---:|---:|---:|
| 4 | 6,919 ns/rec | 3,876 ns/rec | 3,950 ns/rec |
| 8 | 4,497 ns/rec | 3,878 ns/rec | 3,927 ns/rec |
| 16 | 5,096 ns/rec | 4,377 ns/rec | 4,317 ns/rec |

Four workers and eight are indistinguishable once a run is long enough to
amortise goroutine start-up, and sixteen is ~11% *worse*. A profile of a
100,000-record run shows 425% CPU on a 16-thread machine: adding workers no
longer slows generation down the way it did before the cap was introduced,
but it does not speed it up either, because the lock is saturated. So the cap
stays at 4 - it reaches the same throughput on a quarter of the cores.

Going further means not calling `faker.FakeData` per record: building one
generator func per column at config time would remove the tag decoding and,
for any type `provider` implements, the lock with it. Only then does raising
the worker cap become worthwhile.

### Hand-rolled emitters

`yaml` and the two JSON writers do not call a marshaller per record. Every
column is a string and the schema is fixed, so each writer renders its keys
once at construction and then appends values into a reused buffer. For `yaml`
that replaced a `yaml.Marshal` per record (a node tree, formatted from
scratch, then split into lines and re-indented) and took the writer from
11.9 ms per 1,000 records to 0.46 ms - **26x** - with allocations flat at 20
per batch instead of 73,500 per 1,000 records.

The catch is quoting, which the marshaller used to handle. `appendYAMLScalar`
emits a plain scalar only for a narrow, obviously-safe shape - leading ASCII
letter, body from a small alphabet, not one of YAML's reserved words - and
double-quotes everything else, borrowing `jsontext.AppendQuote` since a JSON
string literal is also a valid YAML 1.2 double-quoted scalar. That is stricter
than `yaml.v3`, so slightly more values get quoted than strictly need to be
(output grew ~3%, 240 to 248 B/rec); it is never wrong, which a looser rule
would risk being. `TestYAMLSeqWriterRoundTrip` pins this down over 45
adversarial values - `true`, `~`, `0x1f`, `2019-12-24`, `a: b`, embedded
newlines and NULs - all of which must survive a parse back to the exact input
string. A record type the fast path cannot describe (a non-string column)
falls back to `yaml.Marshal`.

That yaml is now the *fastest* writer, ahead of the two reflection-based JSON
ones, sizes the remaining headroom in the json path: a measured ~2x.

### encoding/json/v2

Go 1.27 enables the `jsonv2` experiment by default, which reimplements the v1
`encoding/json` on top of `encoding/json/v2`. That is not a free speed-up: on
this workload the v1 API got **~17% slower** under it (measurable with
`GOEXPERIMENT=nojsonv2`, which restores the old implementation), at identical
allocation counts.

The win is in the v2 API rather than the v1 shim. `json.MarshalWrite` streams
a value straight into an `io.Writer`, so the `jsonArrayWriter` and
`fluentWriter` no longer allocate an intermediate `[]byte` per record: a
10,000-record batch went from 2.48 MiB and 10,008 allocations to **4.25 KiB
and 6**, and got ~11% faster in the process. Raw wall-clock is still a few
percent behind the pre-1.27 implementation; the allocation collapse is the
reason to prefer it, and it is why `go.mod` requires 1.27.

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
