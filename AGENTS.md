# Working in this repo

mockagen generates large volumes of fake data locally from a Mockaroo-style
config, and writes it in one of seven formats. It is a small, deliberately lean
Go program: one binary, six non-test dependencies, no framework anywhere.

This file is the house style. It exists because the conventions below are real
and consistently applied, but they live in code comments rather than any
contributing guide, so they are easy to miss and easy to break.

## Commands

Tasks are bash scripts in [.config/mise/tasks/](.config/mise/tasks); `mise tasks`
lists them with descriptions.

| Command | What it does |
|---|---|
| `mise run test` | Unit tests. Must stay fast and must touch no containers |
| `mise run lint` | `gofmt -l ./cmd ./pkg ./scripts`, then `go vet ./...` **and** `go vet -tags=integration ./...` |
| `mise run build` | Builds `./mockagen` |
| `mise run test-integration` | SQL integration tests; starts and removes its own database containers |
| `mise run bench` | Benchmarks against the working tree |
| `mise run bench-compare [base] [rounds]` | Interleaved A/B against another commit, then benchstat |

`mise run lint` and `mise run test` are the gate before any commit. CI uses none
of mise - the workflows call `go` directly and take the toolchain from `go.mod` -
so tasks can change without touching them.

Go is pinned to 1.27 in both `go.mod` and `.config/mise/config.toml`; keep them
in step. `encoding/json/v2` is in use, which is why.

## Code conventions

**Every non-test declaration carries a doc comment, and the comment says *why*,
not what.** This is the strongest convention in the repo and the one most worth
matching. Look at `sqlDialect`'s fields in `cmd/mockagen/writer.go` or
`generateFakes`'s worker-count constant in `cmd/mockagen/main.go`: each comment
explains the constraint that forced the code to be this way, usually naming the
bug or the measurement behind it. A comment that restates the identifier is worse
than none.

Tests get the same treatment when the name alone does not carry it - say which
bug the test guards.

Other rules:

- Wrap comments and prose at 79 columns.
- `gofmt` is the only formatter and `go vet` the only linter. There is no
  golangci-lint config; do not add one without asking.
- Dependencies are a decision, not a detail. The non-test list is six modules.
  Weigh any addition, and prefer the standard library.
- Errors a user can act on go through `fatal(err)` in `cmd/mockagen` - a message
  on stderr and exit 1, no stack trace. Reserve panics for programmer errors.
  (`pkg/mockagen.LoadConfig` panics on bad input; that is legacy behaviour its
  tests pin, not a pattern to copy.)
- Memory stays bounded regardless of row count. The writers hold one record at a
  time by design; anything new on a per-row path should do the same.

## Testing

- **Standard library `testing` only.** No testify, no mocks, no fixture
  framework. testify appears in `go.sum` only as a testcontainers dependency.
- **Table-driven, with expected values inline.** There are no golden files on
  disk; "golden output" here means an exact expected string in the test case.
  See `TestSQLWriterDialects` in `cmd/mockagen/writer_test.go` for the canonical
  shape.
- `t.Run` per case, `t.Helper()` in helpers, `t.TempDir()` for files,
  `t.Errorf("got %q, want %q", ...)` for comparisons.
- Anything needing an external tool degrades with `t.Skip` after
  `exec.LookPath`, rather than failing.
- **Anything needing docker goes behind `//go:build integration`** and runs only
  from `mise run test-integration`.
- **Write meta-tests that guard the fixtures.** `TestBenchmarkSchemaTypesAreKnown`
  (`cmd/mockagen/main_test.go`) exists because an unknown column type does not
  error - it silently generates a random string - so a typo in a schema would
  otherwise pass every test. Any new lookup table deserves the same guard.

## Performance

Every push to `main` runs the benchmark suite against the previous commit,
interleaved on one runner, and posts a sticky PR comment; results are published
to `dev/bench` on GitHub Pages. See [docs/benchmarking.md](docs/benchmarking.md).

Two consequences:

- Anything on a hot path needs a benchmark, or the CI gate is blind to it.
- `benchstat` matches rows by benchmark name, and the name includes the package
  path. Renaming or moving a benchmark turns that commit's comparison into
  removed/added rows instead of a delta, so do it deliberately and say so.

## Things that will trip you up

- **`MockagenColumn`'s tags are not uniform.** `start_position`/`end_position` in
  YAML and TOML are `startPosition`/`endPosition` in **JSON**, and
  `selectionStyle` is camelCase in all three. See `pkg/mockagen/config.go`.
- **Most `MockagenColumn` fields are parsed and then ignored** -
  `null_percentage`, `blank`, `formula`, `min`, `max`, `format`, `decimals`,
  `selectionStyle`, `distribution`, `alignment`, `pad_character`. Do not assume a
  field works because it decodes.
- **An unrecognised column `type` is not an error.** It silently produces a
  random string.
- **Config loading accepts `.yaml` but not `.yml`,** and panics on every failure
  path.
- **The output path is hardcoded** to `./output/<name>.<ext>` and nothing calls
  `MkdirAll`, so runs outside a directory containing `output/` fail.
- **`main.version`/`main.commit`/`main.date` do not exist** even though
  `.goreleaser.yaml` injects them.
- Example configs live in `test_data/`, which is *not* Go's ignored `testdata/`.
- `PRD.md` on the `origin/features` branch is aspirational and that branch is not
  an ancestor of `main`. Do not implement from it without being asked.

## Commits

One-line conventional-commit subject, no body:

```
feat: add csv and sql output formats
```

The verbose multi-paragraph bodies in older history are the previous style; the
recent commits are the current one. `.goreleaser.yaml` excludes `docs:`, `test:`
and `chore:` from release changelogs, so the prefix decides visibility.

Commit or push only when asked.

## Design documents

[docs/design/](docs/design) holds designs for work not yet built;
[docs/](docs) holds operational topics. Both follow the README's voice:
rationale first, pipe tables for anything enumerable, prose rather than bullet
soup, and a link into the source for every claim about current behaviour. If a
design says a key is reserved but unimplemented, it means it - do not implement
it to be helpful.
