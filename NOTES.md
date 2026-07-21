# Mockagen — Code Notes

Observations from reading through the codebase (2026-07-20), on the
`feature/add-parquet` branch. Intended as a working reference for whoever
picks up parquet support or general hardening next — not a task list, just
what the code actually does today and where it disagrees with itself.

## How it fits together

- `cmd/mockagen/main.go` — entry point. Loads a config, builds a `[]reflect.StructField`
  from `config.Columns`, uses `go-faker` to fill a dynamically-built struct per
  record, then marshals the whole batch to one of: `yaml`, `json`, `fixed`
  (via `go-fixedwidth`), `parquet` (via `fraugster/parquet-go`).
- `pkg/mockagen/config.go` — `MockagenConfig` / `MockagenColumn`, modeled on
  Mockaroo's schema shape (see README: "goal is to make it compatible with
  Mockaroo's schema format").
- `pkg/mockagen/load_config.go` — reads `.yaml`/`.json` config files.
- `test_data/config/` — example schemas, including format-specific variants
  (`single.schema.json`, `single.schema.fixed.json`, `single.schema.parquet.json`).
  `Mockadoo.schema.sql` exists but is **empty** — looks like a placeholder for
  a future SQL output format that was never started.

## Bugs / sharp edges worth knowing about

1. ~~**Divide-by-zero on `num_rows: 0`.**~~ **Fixed** — `generateFakes` now
   returns an empty slice early when `config.NumberOfRecords <= 0`, before
   `numOfWorkers`/`recordsPerGo` are computed.

2. ~~**Column names with spaces/special characters will panic.**~~ **Fixed**
   — added `structFieldName(colName string, idx int) string`, which strips
   non-letter/digit runes, guarantees the result starts with a letter, and
   suffixes the column index for uniqueness. Used for `reflect.StructField.Name`
   only; the user-facing name (lowercased) still drives the `json`/`yaml`/`csv`/
   `fixed`/`parquet` tags, so output keys are unaffected.

3. ~~**Dead package-level `outputFile` var.**~~ **Fixed** — removed the
   unused package-level `var outputFile string`; `main()`'s local
   `outputFile := fmt.Sprintf(...)` is the only one now.

4. ~~**Known-bad integer division for record distribution across workers**~~
   **Fixed** — kept the even `recordsPerGo := config.NumberOfRecords / numOfWorkers`
   split, but now compute `remainder := config.NumberOfRecords % numOfWorkers`
   and give the first `remainder` workers one extra record each, so the total
   generated always equals `config.NumberOfRecords` exactly (verified with
   100 records / 48 workers, previously dropped 4).

## Config fields the schema declares but the generator ignores

`MockagenColumn` (pkg/mockagen/config.go) has a lot of Mockaroo-shaped fields
that `generateFakes` never reads:

- `NullPercentage`, `Blank` — no null/blank injection happens; parquet schema
  even hardcodes every column as `FieldRepetitionType_REQUIRED`
  (`main.go:121`), so nulls couldn't be written even if generated.
- `Formula` — present in every test config (e.g. `"formula": "upper(this)"`
  on the GUID column) but never evaluated.
- `Min`, `Max`, `Format` — set on `Datetime` columns in test data but not
  used; dates come from whatever `go-faker`'s default `date` provider does.
- `Decimals`, `Alignment`, `PadCharacter` — unused.
- `IncludeHeader` — read into the config struct but never referenced in
  `main.go`.

Worth keeping in mind when someone reports "the min/max on my date column
isn't respected" — it's not implemented, not broken.

## Parquet-specific gaps (relevant to this branch)

- `createParquetSchema` (`main.go:92-155`) handles `INT64`, `BYTE_ARRAY`, and
  `DOUBLE` column types — but `generateFakes` unconditionally builds every
  field as a Go `string` with the parquet tag hardcoded to
  `type=BYTE_ARRAY` (`main.go:167,172`). The `INT64`/`DOUBLE` branches in
  `createParquetSchema` are currently unreachable dead code; there's no path
  that produces a numeric-typed struct field.
- All fields are `FieldRepetitionType_REQUIRED` — no `OPTIONAL`/nullable
  columns, so `NullPercentage` couldn't map to parquet nulls without this
  changing too.
- `generateFakes` builds the **entire** `[]any` of faked records in memory
  before any writer touches them, then loops to write. `single.schema.parquet.json`
  is currently checked out with `num_rows: 10000000` — that's 10M records
  buffered in memory (as a slice of `any` wrapping reflect-built structs)
  before the parquet writer sees a single row. Might be fine, but worth a
  deliberate decision rather than an accident — streaming record generation
  straight into `writer.Write` would avoid the double buffering.
- No `csv` case in the `main()` switch even though every generated struct
  field carries a `csv:"..."` tag (`main.go:167`) — looks like csv output was
  planned/partially wired and then not finished.

## Testing

- Only test file is `cmd/mockagen/main_test.go`, and it's a benchmark only
  (`BenchmarkGenerateFakes`) — no assertions on output correctness, no tests
  for `pkg/mockagen/load_config.go`, and no coverage of `createParquetSchema`
  or the marshal/write paths in `main()`.
- The benchmark's own config uses column types (`"username"`, `"date"`,
  `"first_name"` lowercase, etc.) that don't match any key in `mapToFaker`
  (`main.go:26-34`, which only knows `"GUID"`, `"First Name"`, `"Last Name"`,
  `"Email Address"`, `"Gender"`, `"Datetime"`, `"Custom List"`). So the
  benchmark silently generates fields with an empty `faker:""` tag rather
  than exercising the intended mapping — it measures the reflect/goroutine
  machinery, not realistic faker output.

## Error handling style

- `pkg/mockagen/load_config.go` `panic()`s on bad extension, file-open
  failure, and decode failure instead of returning `error`. Same pattern in
  `main.go` for marshal/write failures. Fine for a CLI's `main()`, but it
  makes `pkg/mockagen` awkward to use as a library from other Go code, since
  callers can't recover from a bad config path without a top-level
  `recover()`.

## Dependencies

- `github.com/fraugster/parquet-go` (a community fork, not the more common
  `xitongsys`/`parquet-go` or `apache/arrow`-based lib) plus its transitive
  `apache/thrift v0.16.0` — both fairly old pins. Worth a version check
  before this ships, since thrift has had CVEs in older releases.

## Possible next steps (not decisions, just what falls out of the above)

- Guard `num_rows <= 0` in `LoadConfig` or `generateFakes`.
- Validate/sanitize column names (or map to a synthetic `Field0`, `Field1...`
  identifier and keep the user-facing name only in the tag) before calling
  `reflect.StructOf`.
- Decide whether `Formula`, `Min`/`Max`/`Format`, `NullPercentage` are in
  scope for the Mockaroo-compat goal, and either implement or drop them from
  the schema to stop implying support that doesn't exist.
- Make column type → Go type drive `createParquetSchema`'s branch selection
  (numbers should actually produce `INT64`/`DOUBLE`, not everything as
  `BYTE_ARRAY`) if parquet is meant to store typed columns rather than all
  strings.
- Consider streaming record generation into the writer instead of
  materializing all records in memory first, especially given the 10M-row
  test config now on this branch.
