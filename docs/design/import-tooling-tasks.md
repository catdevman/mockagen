# Import tooling: task breakdown

The design and its reasoning are in
[import-tooling.md](import-tooling.md). This file is the execution plan for it:
self-contained tasks, in order, each naming what it touches and what "done"
means. The house conventions every task is held to are in
[AGENTS.md](../../AGENTS.md).

Read the design section a task names before starting it. Every task also
inherits three acceptance criteria, which are not repeated below:

- `mise run lint` and `mise run test` pass.
- Every new declaration has a doc comment saying why it exists.
- New tests are stdlib, table-driven, with expected values inline.

One standing rule: a task that cannot be finished as written should stop and say
so rather than widening. Several tasks here deliberately leave adjacent problems
alone.

---

## Phase 0a - `-output`

Independent of everything else in this document. Worth doing first because it is
small and because every later command needs it.

### T0a.1 - make the output path configurable

Design: *CLI shape and three papercuts*.

Add an `-output` flag to the existing generate path and create the parent
directory before writing.

- Files: `cmd/mockagen/main.go`, `cmd/mockagen/main_test.go`.
- Done when:
  - `mockagen -config c.yaml -output /tmp/x/out.csv` writes exactly there,
    creating `/tmp/x` if absent.
  - With no `-output`, the path is still `./output/<name>.<ext>` with spaces in
    the name turned into dashes, and `./output/` is now created rather than
    required - a test pins both the unchanged default and the directory
    creation.
  - `fluent` still lands in a `.log` file.

---

## Phase 0b - foundations

Nothing here ships a feature. Every later phase depends on it, and every later
phase that reimplements one of these instead of using it is a divergence waiting
to be found by a failed load.

### T0b.1 - subcommand dispatch, `-version`, `.yml`

Design: *CLI shape and three papercuts*.

- Files: `cmd/mockagen/main.go`, `pkg/mockagen/load_config.go`, plus tests.
- Dispatch on a known-name set - `gen`, `schema`, `map`, `import`, `version`,
  `help` - and fall through to the legacy flag path for anything else. One
  `flag.NewFlagSet` per subcommand. Do not add a CLI framework.
- Declare `version`, `commit`, `date` in `package main` so the ldflags
  `.goreleaser.yaml` already injects stop being discarded.
- Accept `.yml` wherever `.yaml` is accepted.
- Done when:
  - `mockagen -config x.yaml`, `mockagen --config x.yaml`,
    `mockagen -config=x.yaml` and `mockagen gen -config x.yaml` all generate.
  - A bare `mockagen` prints usage and exits non-zero; an unknown subcommand
    does the same and names the valid ones.
  - `mockagen version` prints the three ldflags values, and a test asserts the
    variables exist (a build without ldflags prints a `dev` placeholder).
  - A table test covers `.yml` for all three encodings.

### T0b.2 - `pkg/mockagen/types`: the generator-type catalog

Design: *What has to change in mockagen first*.

Move `mapToFaker` out of `cmd/mockagen/main.go` and merge
`provider.TypeMap` into one catalog. Each entry carries the faker tag, the kind
of value it produces, which of `values`/`min`/`max`/`format` it requires, an
`SQLAffinity` (text/int/float/bool/time/binary), and name hints for inference.

- Files: new `pkg/mockagen/types/`, `cmd/mockagen/main.go`,
  `pkg/mockagen/provider/provider.go`.
- `provider.TypeMap` stays the single source of provider-backed tags and the
  catalog consumes it at init - do not copy it, or the two existing meta-tests
  start guarding different things.
- Done when:
  - `types.Lookup(name)` reports whether a type is known, and
    `types.Candidates(affinity)` returns types for a SQL affinity.
  - `generateFakes` produces byte-identical output to before for every config in
    `test_data/config/` - pin this with a test rather than by eye.
  - `TestBenchmarkSchemaTypesAreKnown` and `TestTypeMapRegistered` still pass
    unmodified, and a new meta-test asserts every catalog entry's tag is either
    a faker built-in or registered by `provider`.

### T0b.3 - `pkg/mockagen/sqlgen`: extract the dialects

Design: *Why the writers stay in `package main`*.

Move only the dialect machinery: `sqlDialect`, the dialect table, the aliases,
`lookupSQLDialect`, `appendIdent`, `appendQualifiedIdent`, `appendString`,
`identList`, `bulkLoadScript`, `maxRowsPerStatement`. **Leave every writer in
`package main`** - the reasons are in the design and are not re-litigable from
aesthetics.

- Files: new `pkg/mockagen/sqlgen/`, `cmd/mockagen/writer.go`,
  `cmd/mockagen/writer_test.go`.
- Leave `type sqlDialect = sqlgen.Dialect` behind so `writer.go`'s diff is
  small. Move only the focused dialect tests (`TestLookupSQLDialect`,
  `TestSQLIdentQuoting`, `TestBulkLoadScript*`, `TestSQLTable`).
- Add two fields: placeholder style (`$1` / `?` / `@p1`) and max bound
  parameters (65535 postgres, 2100 mssql).
- Add `AppendValue` for the importer - bare `NULL` for a null, unquoted digits
  for numbers, verbatim for raw, and an **error** on an embedded NUL. Do not
  change `appendString`, which drops NUL by design for the generator; making it
  generic over `~string | ~[]byte` is fine and is the point.
- Done when:
  - No benchmark changes package. `mise run bench` lists the same names as
    before.
  - `mise run test-integration` passes unchanged.
  - A table test covers `AppendValue` for all four dialects across null, int,
    float, raw, unicode, quote, backslash, CR and embedded-NUL inputs.

### T0b.4 - error-returning loader with `kind` dispatch

Design: *The two new file kinds*.

- Files: `pkg/mockagen/load_config.go`, plus tests.
- Two-pass: `os.ReadFile`, decode a `{version, kind}` envelope, then decode the
  same bytes into the concrete type. Streaming cannot sniff and then decode.
- Add `version` and `kind` fields to `MockagenConfig` so an envelope on an
  existing config is always valid in all three encoders.
- Reject a column `type` the catalog does not know.
- Done when:
  - `LoadConfig` keeps its exact current behaviour and its panic tests pass
    untouched; it delegates to the new function.
  - A kind-less file loads as a Spec; `kind: Spec` loads identically; an
    unknown kind is a named error.
  - An unknown column type is an error naming the column and the type.

---

## Phase 1 - the schema side

### T1.1 - `pkg/schema`: types, load, save, render

Design: *`kind: Schema`*.

- Files: new `pkg/schema/`.
- Go structs for the Schema kind with correct tags for YAML and JSON. Mirror the
  tag spellings of `MockagenColumn` exactly for the decision fields - note its
  JSON/YAML asymmetry on `start_position` (see AGENTS.md).
- `Render` turns a Schema into a `MockagenConfig`, enforcing the couplings:
  `nullable: false` forces `null_percentage` to 0; `identity: true` omits the
  column; an unresolved `type` refuses to render unless forced, and a foreign-key
  or unique column renders with a loud comment that its values will not satisfy
  the constraint.
- Done when a round-trip test loads each example in the design document, saves
  it, and gets byte-identical YAML back; and `Render` has a table test covering
  each coupling above.

### T1.2 - `internal/dbtest`: extract the container harness

Design: *Testing*.

- Files: new `internal/dbtest/`, `cmd/mockagen/integration_test.go`.
- Move `target`, `serverImage`, the lazy `container`, `execIn`, `copyIn` and the
  `TestMain` teardown. **No build tag on the package** - it is `internal` and
  never linked into the binary - with the `integration` tags staying on the test
  files that import it.
- Add `MappedPort` and DSN construction: introspection is the first thing here
  that needs a Go driver pointed at a container.
- Expose sqlite as a fourth target whose container step is a no-op and whose exec
  runs the local `sqlite3`, so per-dialect test tables stay uniform at four rows.
- Done when `mise run test-integration` passes with the existing assertions
  unchanged, and `mise run test` still starts no containers.

### T1.3 - `pkg/schema/introspect`: four dialects

Design: *`mockagen schema`*, and the facts list in *`kind: Schema`*.

- Files: new `pkg/schema/introspect/`.
- Takes a one-method `Querier`, so the package imports no driver. Drivers are
  registered in `cmd/mockagen/drivers.go` only: pgx, go-sql-driver/mysql,
  go-mssqldb (root package only). **No sqlite driver** - drive the `sqlite3` CLI.
- **The queries are already written and verified**:
  [introspection-queries.md](introspection-queries.md) has the columns,
  constraints, enum and table-list query for each of the four dialects, each run
  against the same images the integration tests use, with its real output and
  the per-dialect traps. Use them rather than deriving your own; the
  divergences table at the end is the specification for normalising the four
  into one `schema.Column`.
- The hardest part is not the SQL: it is that `CHECK (x IN (...))` comes back
  rewritten differently by all four engines, and **mssql loses the declared
  order**. One small parser per dialect, each returning `(column, values, ok)`
  and bailing to `ok = false` on anything unrecognised. `ISJSON`,
  `JSON_PATH_EXISTS`, `JSON_SCHEMA_VALID`, and `jsonb_typeof` are
  unrecognised. They do not become `enum_values` or a key list.
- A `json` or `jsonb` column is that `sql_type` and nothing else. Postgres
  `format_type` and MySQL `COLUMN_TYPE` already return the spelling. The
  verified SQL Server image is 2022, which has no native `json` type, so a
  document there stays `nvarchar(max)`. SQL Server 2025's `json` is `ty.name`
  with no length suffix. Do not add a query for document shape, and do not
  sniff sample values to promote a text column.
- Done when, behind `//go:build integration`, a per-dialect table creates one
  fixture table covering a primary key, an identity column, `NOT NULL`,
  `UNIQUE`, a `DEFAULT`, `varchar(n)`, `numeric(p,s)`, an enum or CHECK and a
  foreign key, then asserts the marshalled schema YAML as an inline string **per
  dialect** - the `sql_type` spellings legitimately differ and that divergence is
  what the test is for.

### T1.4 - `infer.go`

Design: *Inference*.

- Files: `pkg/schema/infer.go`. Not a package.
- Ordered rules, key rules before name rules before type rules, each with the
  stable id that lands in `inferred_by`. The table in the design is the spec.
  `json` and `jsonb` match no rule, including the name rules, and stay
  unresolved. `nvarchar` still matches `type:text`.
- Done when a table test drives every rule from a synthetic column and asserts
  the chosen type *and* the id; plus a meta-test asserting every type any rule
  can emit exists in the catalog.

### T1.5 - `mockagen schema`, non-interactive

Design: *`mockagen schema`*.

- Files: `cmd/mockagen/schema.go`, `cmd/mockagen/drivers.go`.
- `-dialect`, `-dsn`, `-table`, `-o`, `-render`, `-update`. DSN also from
  `$MOCKAGEN_DSN` or `$DATABASE_URL`.
- `-update` refreshes facts, preserves decisions marked `user`, re-infers the
  rest, and reports drift on stderr.
- Done when an integration test introspects a fixture table, writes a schema
  file, re-runs `-update` against an altered table, and asserts that a
  hand-edited decision survived while a changed `sql_type` was refreshed and
  reported.

---

## Phase 2 - the import side

### T2.1 - `pkg/importer`: record, source, sink

Design: *`pkg/importer`*.

- Files: new `pkg/importer/`.
- `Kind`/`Value`/`Record` exactly as the design gives them: zero `Kind` is null,
  text in a reused arena located by int32 offsets, `Bytes` documented as
  aliasing until the next `Next` or `Reset`.
- CSV/TSV and JSON/JSONL sources. Sinks: SQL insert, SQL bulk pair, csv, json,
  and a discarding sink.
- Done when:
  - A `sliceSource` plus a recording sink exercise the pipeline with no files,
    including a table covering null versus empty string on every sink.
  - `BenchmarkPlanApply` and the per-sink import benchmarks report **0
    allocs/op** in steady state. This is the acceptance criterion the arena
    design exists for; if it does not hold, the design is not implemented.
  - The `INSERT` column list contains only columns the plan writes. Identity
    columns and every other omitted column are absent. A mapped null is an
    explicit NULL, including when the schema column has a database default.

### T2.2 - FieldMap: types, compile, transforms

Design: *`kind: FieldMap`*.

- Files: `pkg/importer/fieldmap.go`, `pkg/importer/transform.go`.
- Six transforms and no more: `trim`, `case`, `replace`, `regex_replace`, `map`,
  `pad_left`/`pad_right`. The apply order is the string chain, then `null_if`,
  then coercion from `sql_type` plus the field's `format`, then `default`, then
  the schema checks (`max_length`, precision, scale, `enum_values`). Do not
  read the column's generator `type` or `format`. **`expr:` is reserved and
  unimplemented** - leave it that way. There is no `required` key.
- A field has one origin: `source`, `sources` plus `join`, `coalesce`, or
  `constant`. `sources` without `join` does not coalesce. `object` is
  specified under *`kind: FieldMap`* and is [T2.6](#t26---object-origin),
  not this task.
- `schema:` resolves relative to the map file. `source.fields` pins a header;
  headerless input uses `indexes`.
- Single-key maps need a custom `UnmarshalYAML` reading one key off a
  `yaml.Node`. An enum seed is `map.to` with no pairs, and it is unfinished.
- Compile reports, before any row: unknown transform, bad regex, a chain whose
  kinds do not line up, two origins on one field, a `format` on a non-time
  column, an uncoercible constant, a target that is `identity`, a blocker
  (`NOT NULL`, no default, not identity, not mapped), a stale name, an
  unfinished lookup, and an `omitted` entry that is none of identity, defaulted,
  or nullable.
- `Propose` matches exact, then normalised, then one unambiguous fuzzy winner.
  It seeds text with `trim`, enums with `trim` plus `map.to`, and puts identity,
  unmatched defaults, and unmatched nullables into `omitted`. Re-proposing onto
  an existing map fills unseen columns only and does not rewrite edited chains.
- Done when a table test covers each transform including edge cases, a second
  table asserts each compile diagnostic by its error, and a third pins Propose:
  the three match passes, an ambiguous fuzzy pair left unmatched, an enum seed,
  and a re-run that preserves an edited chain.

### T2.3 - `mockagen import`

Design: *`pkg/importer`*.

- Files: `cmd/mockagen/import.go`.
- `-map`, `-data`, `-out`, `-rejects`, `-limit`, `-dry-run`.
- Load the schema from the map's `schema` path, relative to the map file.
  Compare the data file's header to `source.fields`, or its width to `indexes`,
  before the first row. Refuse a plan that still has a blocker, a stale name,
  or an unfinished lookup.
- `Report` counts and the rejects file carrying each bad row with its source line
  number and reason. A schema-check miss is a row error, same as a bad date.
- Done when a golden test imports a small CSV against a schema and pins the
  emitted SQL byte for byte, the rejects file for a file with three bad rows, and
  the report counts. A swapped header fails with both names and writes no output.

### T2.4 - round-trip integration

Design: *Testing*.

- Generate from a rendered spec, import the output back through the field map
  describing it, and compare byte for byte, per dialect.
- Expect this to fail until the generator work in *The generator gap* lands.
  That is the point of the test; skip it with a clear message naming the gap
  rather than weakening it.

### T2.5 - `mockagen map`, non-interactive

Design: *`mockagen map`*.

- Files: `cmd/mockagen/map.go`. The stub in `main.go` dispatches here.
- `-schema` and `-data` are required. `-map` loads an existing file. `-out`
  defaults to stdout. `-sample` defaults to 1000 leading rows. `-tui` is the
  exception: a missing `-schema`, `-data`, or `-map` is collected with a file
  picker instead of exit 2. `-map` can be skipped. `-out` is not prompted.
- Exit 2 on a flag error. Exit 1 when the written map still has a diagnostic.
  Exit 0 when it is clean. Write the file in every case where it encodes.
- Done when a test proposes a map from a schema and a CSV header, pins the
  YAML, and shows a second run with `-map` adding only an unseen column. A
  header that disagrees with the stored `source.fields` is reported and does
  not rebind existing entries.

### T2.6 - `object` origin

Design: *JSON columns* and *`kind: FieldMap`*.

Not started. T2.2's four origins stay as they are until this task.

- One field, one column. `object` is an origin, not a seventh transform, and
  it cannot share the field with `source`, `join`, `coalesce`, or `constant`.
- The shape in the design is the shape. Key order in the file is the order
  emitted. A null leaf is omitted. Each leaf is the normal string chain.
  Nested objects use the same shape. Arrays and a JSON Schema on the column
  are out of scope.
- The assembled text is parsed once. A constant that is not valid JSON fails
  compile. A row that does not assemble is a row error. `json` and `jsonb`
  stay text at coercion, so a single source cell that is already a document
  stays a normal `source`. `object` is also legal on a text column.
- The mapping screen offers the editor when `sql_type` is `json` or `jsonb`.
  Keys are typed. Keys parsed from a sample cell are hints, not facts.
- Done when a table test pins the emitted document, a null leaf's absence,
  a bad constant at compile time, and a bad row. A second test shows a
  `jsonb` column staying unresolved through inference, with `sql_type`
  still `jsonb`.

---

## Phase 3 - the rest

Decisions that replace the earlier sketch are in
[import-tooling.md](import-tooling.md) under *Reading what mockagen writes*,
*`mockagen schema`*, and *`mockagen map`*. The short form:

- Readers stay in `pkg/importer` behind `OpenSource`. There is no
  `pkg/mockagen/reader`.
- `sql` is the only write-only `file_format`. The list lives next to the
  readers. `newRecordWriter` rejects a name that is not on it, so a new
  format has to be added there and then given a reader or marked write-only.
- A typed parquet value whose kind matches the column is copied. It is not
  parsed, and `cell` must not treat it as NULL. A chain on that field is a
  row error. String, decimal, and civil-date columns stay on the text path.
- Fixed positions are `source.fixed` (`name`, `start`, `end`), 1-based and
  inclusive. The importer slices the line. It does not call `go-fixedwidth`.
- The schema screen gets an injected preview func. Unresolved columns are not
  handed to the generator. Connections store `password_env`, never a password,
  and a DSN that already contains one is refused.
- The map check screen runs the sample through `Run` and a discarding sink
  when compile is clean, and does not run when it is not. The editor shows
  `importer.Stages`. The sniffed source line on the board is the source
  setup; there is no second delimiter pass.

### T3.1 - remaining readers

yaml, fixed, fluent, and parquet sources, plus the meta-test. Parquet's doc
comment says which values arrive typed and bypass coercion.

Done when a round trip through the writer matches for yaml, fixed, fluent,
and parquet; a typed parquet integer is not parsed and a `trim` on it fails;
a fixed line of the wrong width is a row error; `sql` is rejected by
`OpenSource` and accepted by the writer; every other `file_format` is accepted
by both.

### T3.2 - `internal/tui/schemaui`

Design: *`mockagen schema`*.

Five screens: connect, pick a table, map columns, preview, save. **No I/O in
`Update`.** Every dial, query, and write is a `tea.Cmd`. A saved connection
stores the name of the environment variable holding the password.

Done when model tests feed message sequences and assert state with no
terminal, database, or file; `View()` is asserted against inline strings under
an Ascii profile set in `TestMain`, with a leading `tea.WindowSizeMsg`. One
command test may use a temp file to prove a saved connection has no password
in it. Do not add `teatest`.

### T3.3 - `internal/tui/mapui`

Design: *`mockagen map`*.

The preview and the check screen are `Options.Limit` and `Options.SampleRows`
against a discarding sink when the plan is clean. They call `Propose`,
`Compile`, and `Run`. A `NOT NULL` target with no default, no identity, and no
mapping renders as a blocker. `o` moves a legal target to `omitted`. The
transform editor shows the chain and then `null_if`, coercion, `default`, and
the schema checks, with the sample beside them, via `Stages`. `s` writes and
stays. `q` quits, and asks when the map is dirty since the last save. A
missing file flag opens the bubbles file picker before the board. A bad file
stays on the picker. `s` there skips an existing map. The object editor does
not grow a key list. `boardHelpKeys` stays as it is.

Tested as T3.2 is.
