# Schema and mapping tooling

mockagen's config is written by hand. You decide the column list, you decide
each column's generator type, and nothing ties the config to the real database
table the data is destined for. That is fine for inventing data out of nothing,
and it is the wrong shape for the two jobs next to it: generating data that fits
a table that already exists, and loading a data file *into* that table.

This document designs three pieces that close that gap.

| Piece | What it is |
|---|---|
| `mockagen schema` | Introspects a live database table and writes a **schema file**: the table's real shape, plus a generator type chosen per column. A non-interactive command with a TUI over it |
| `mockagen map` | Takes a schema file and a sample data file and writes a **field map**: which source field feeds which target column, and what happens to the value on the way |
| `pkg/importer` | The library that performs the import, given a data file and a field map. Driven by `mockagen import` |

They are one document because they are one pipeline, and because the schema file
is the interface between them - described three times, it would drift twice.

## The pipeline

```
                      +------------------+
  live database ----->| mockagen schema  |---> users.schema.yaml ---+
                      +------------------+     (kind: Schema)       |
                                                                    v
                      +------------------+                  +---------------+
  legacy.csv -------->|  mockagen map    |<-----------------|    schema     |
                      +--------+---------+                  +---------------+
                               |
                               v
                       users.map.yaml                  +------------------+
                       (kind: FieldMap) -------------->| mockagen import  |
                                                       |  (pkg/importer)  |
  legacy.csv ------------------------------------------>+--------+---------+
                                                                 v
                                                      users.sql, or a bulk
                                                      CSV + loader script
```

The schema file has a second consumer. Rendered into a plain mockagen config, it
is a generation spec for the table it came from - point the tool at a table, get
fake data shaped like that table. That loop is why the schema file is worth
writing even if you never import anything, and it is also the loop with a
prerequisite; see [the generator gap](#the-generator-gap).

Three scope decisions are load-bearing, so they belong up front.

**A schema file describes one table.** mockagen's whole model is one config, one
flat column list, one output file; a schema file holding a table graph would have
no generation story and would drag foreign-key load ordering, cycles and
self-references into v1. Selecting four tables in the TUI writes four files.
Foreign keys are still captured - they are excellent inference signal - but
nothing orders loads for you.

**The importer writes files, not databases.** `INSERT` statements and the bulk
CSV/loader pair are what mockagen already emits, and what
[the integration tests](../../cmd/mockagen/integration_test.go) already prove
load into postgres, mysql, sqlite and SQL Server. Stopping at files means the
import path inherits a tested back end instead of growing an untested one, and
it keeps database drivers on the *read* side only: nothing in the write path
needs a connection, a transaction, or a conflict policy. A live `database/sql`
sink is ~80 lines behind the same `Sink` interface whenever it is wanted.

**Every TUI is a view over a finished non-interactive command.** `mockagen
schema -dsn ... -table users -o users.schema.yaml` ships before the schema TUI,
and the importer ships before the map TUI. That ordering is not politeness
toward scripting: it is what makes the work testable in this repo's style, and it
stops the interactive path from accumulating logic the command does not have.

## The two new file kinds

Both carry an envelope:

```yaml
version: mockagen/v1
kind: Schema        # or FieldMap
```

Today's configs have no `kind`, and a file without one is a `Spec`: the existing
`MockagenConfig`. So `mockagen -config test_data/config/users.schema.sql.yaml`
keeps working byte for byte, and `kind` is how the loader decides what it is
decoding rather than a migration anyone has to perform.

Two consequences that are not free, and are worth stating because "sniff the kind
and dispatch" reads like it is:

- **Decoding becomes two-pass.** [`LoadConfig`](../../pkg/mockagen/load_config.go)
  streams a `bufio.Reader` into a decoder, and you cannot read `kind` out of that
  stream and then decode the body from the same stream. `BurntSushi/toml` has no
  clean partial decode either. These are config-sized files, so the answer is
  `os.ReadFile`, decode a two-field envelope struct, then decode the same bytes
  into the concrete type.
- **`MockagenConfig` gains `version` and `kind` fields of its own.** Otherwise
  someone adding `kind: Spec` to an existing config hands the decoders a key
  nothing claims, and how that lands depends on which of the three it is.

Both kinds are YAML or JSON. TOML stays available for hand-written specs, but
these two are *generated* files, and the transform chain's one-key-per-map shape
spells badly as `[[fields.transforms]]`. One fewer decoder path to test is worth
more here than uniformity.

### `kind: Schema`

The central decision in the schema format: **each column separates introspected
facts from human decisions**, and the decision fields are spelled with the exact
field names of [`MockagenColumn`](../../pkg/mockagen/config.go), so rendering a
schema into a generation spec is a field copy rather than a translation layer.

```yaml
version: mockagen/v1
kind: Schema
name: users
source:                      # provenance: facts about where this came from
  dialect: postgres
  database: appdb
  table: public.users
  captured_at: 2026-10-12T14:03:11Z
columns:
  - name: id                 # --- facts: introspected, refreshable ---
    sql_type: uuid
    nullable: false
    primary_key: true
    default: gen_random_uuid()
    type: GUID               # --- decisions: chosen, preserved ---
    inferred_by: key:text-pk

  - name: email
    sql_type: varchar(255)
    max_length: 255
    nullable: false
    unique: true
    type: Email Address
    inferred_by: name:email

  - name: status
    sql_type: user_status
    nullable: false
    enum_values: [active, suspended, closed]
    type: Custom List
    values: [active, suspended, closed]
    inferred_by: rule:enum

  - name: login_count
    sql_type: integer
    nullable: true
    type: ""                 # UNRESOLVED - see the generator gap
```

The fact fields, in full: `sql_type` (verbatim, as the server spells it),
`nullable`, `primary_key`, `unique`, `default`, `identity`, `max_length`,
`numeric_precision`, `numeric_scale`, `enum_values`, `references` (as
`{table, column}`), `comment` and `ordinal`. The decision fields are whichever
`MockagenColumn` fields the chosen type needs: `type`, plus `values`, `min`,
`max`, `format`, `decimals`, `null_percentage`.

Facts and decisions are separate but not independent, and the render boundary
enforces the couplings the model would otherwise permit:

- `nullable: false` means `null_percentage` must be 0. The TUI makes it
  unselectable rather than reporting it afterwards.
- `identity: true` means the column is omitted from the generated spec entirely.
  A field map may not target it either: Propose places it in `omitted`, and the
  importer leaves it out of the `INSERT` column list. Postgres rejects an
  explicit value for `GENERATED ALWAYS`, and `BULK INSERT` maps fields by
  position - which the generated bulk script already warns about.
- `format` and `type` on a column are generation decisions. The field map does
  not read them. Coercion uses `sql_type` and the field map's own `format`.
- `references` and `unique` are facts nothing can honour yet, and the renderer
  must say so out loud rather than quietly emitting a `Word`. See the gap section.

`inferred_by` holds the id of the rule that chose the type, or the literal `user`
when a human did. That is what makes a second run safe:

```sh
mockagen schema -update users.schema.yaml
```

re-reads the table, refreshes every fact, keeps every decision marked `user`,
re-runs inference over the rest, and reports what moved - columns added, columns
dropped, a `varchar(50)` that is now a `varchar(100)`. Without the marker the
second run has two options and both are wrong: clobber the hand-tuning, or freeze
facts that are no longer true.

#### JSON columns

A JSON column is a type name. The catalogs do not store the keys inside the
document, so the schema file does not grow a key list, a JSON Schema, or a
place for `address.city`. Introspection copies the spelling the column
queries already return:

| Engine | What lands in `sql_type` |
|---|---|
| PostgreSQL | `json` or `jsonb`, from `format_type`. `jsonb[]` is an array and stays that spelling. It is not a document. |
| MySQL | `json`, from `COLUMN_TYPE`. |
| SQL Server 2025 and Azure SQL | `json`, from `sys.types`, with no length suffix. |
| SQL Server 2016 through 2022 | `nvarchar(max)`. The verified queries ran on `mcr.microsoft.com/mssql/server:2022-latest`, which has JSON functions and no `json` type. |
| SQLite | `TEXT`. JSON functions do not change the declared type. |

A check written with `ISJSON`, `JSON_PATH_EXISTS`, `JSON_SCHEMA_VALID`, or
`jsonb_typeof` is unrecognised. The per-dialect check parser returns no
values, the same way it bails on any expression that is not an `IN` list.
Sampling the first rows and promoting an `nvarchar` column because those
rows look like objects is the same guess, and it is not done.

`json` and `jsonb` match no inference rule. Name rules do not fire on them
either: a `jsonb` column called `email` is a document, not an email address,
so generation leaves it unresolved. `nvarchar`, including the `nvarchar(max)`
used as a JSON bag on SQL Server 2022, still matches `type:text`. The field
map reads `sql_type` either way. Building one document out of several source
columns is the field map's `object` origin, not a schema fact.

### `kind: FieldMap`

```yaml
version: mockagen/v1
kind: FieldMap
name: legacy-users-import
schema: ./users.schema.yaml      # relative to this file
source:
  format: csv
  has_header: true
  delimiter: ","
  encoding: utf-8
  fields: [USER_GUID, EMAIL_ADDR, STATUS_CODE, SIGNUP_DT, FIRST, LAST, NICKNAME]
fields:
  - target: id
    source: USER_GUID
    transforms:
      - trim: {}
      - case: lower

  - target: email
    source: EMAIL_ADDR
    transforms:
      - trim: {}
      - case: lower

  - target: status
    source: STATUS_CODE
    transforms:
      - trim: {}
      - case: lower
      - map: {a: active, s: suspended, c: closed}
    default: active

  - target: signed_up
    source: SIGNUP_DT
    format: "01/02/2006"         # parse layout; not the column's format
    null_if: ["", "N/A", "NULL"]

  - target: full_name
    sources: [FIRST, LAST]
    join: " "

  - target: first_name
    coalesce: [NICKNAME, FIRST]

  - target: imported_batch
    constant: "2026-10-legacy"
omitted:
  - login_count                  # nullable, defaulted, or identity
unmapped:
  source: ignore                 # ignore | error
on_error: abort                  # abort | skip_row | null_field
```

`schema` is a path relative to the map file, not the process's working
directory. An absolute path is used as written. `source.fields` is the header
Propose saw, in order. A headerless source stores `indexes` instead, and omits
`fields`. `import` compares a headed file's header to `source.fields` before
reading a row, and refuses a swap, a rename, or a missing name with both lists
in the error. A headerless file is checked by width: every stored index must
fall inside the row.

A field has one origin: a single `source`, `sources` plus `join`, a `coalesce`
list, a `constant`, or an `object`. `sources` without `join` is a compile
error, and so is a field that sets more than one origin. `join` concatenates
the non-empty parts with the separator, then one chain runs on the result.
`coalesce` walks the names in order and keeps the first candidate that is
still non-null after the string chain and `null_if`. There is no `required`
key. Whether an empty result may be stored is the schema column's
`nullable`, `default`, and `identity`.

`object` is not implemented. [`Unmarshal`](../../pkg/importer/fieldmap.go)
rejects the key. It is how several cells become one JSON document, which the
schema cannot describe. One source cell that is already a whole document
stays a normal `source`. [`classify`](../../pkg/importer/compile.go) keeps
`json` and `jsonb` as text, so that cell is stored as the string the chain
produced and the database checks it on insert.

```yaml
- target: profile
  object:
    city:
      source: city
      transforms:
        - trim: {}
    email:
      source: email
    status:
      constant: active
```

Key order in the file is the order emitted. A null leaf is omitted, so a
blank cell means the key is absent. Each leaf is the normal string chain,
including `null_if`. The assembled text is parsed once. A constant that is
not valid JSON fails at compile time, and a row that does not assemble fails
as a row error. Nested objects use the same shape. The origin is legal on a
text column too, which is how a pre-2025 SQL Server `nvarchar` column
receives a document without the schema pretending the type is JSON. The
mapping screen offers the editor for a `json` or `jsonb` column, and the
keys are typed, because the schema has none. Keys parsed from a sample cell
are hints, not facts. Arrays, and a JSON Schema on the column, wait until a
real file needs them. `object` is an origin, not a seventh transform.
`expr:` stays reserved.

What happens to the value is five steps, and only the first is a chain. The
split is the same one Embulk makes. The order is the order a legacy extract
needs, because the cell is text until it has been cleaned:

1. **String munging**, the ordered chain. Trim before lookup, replace before
   regex. Each step declares the kind it accepts and the kind it produces, so
   the chain is checkable before any row is read.
2. **`null_if`**, matched against that text. `"N/A"` becomes null before a
   date parse can reject it.
3. **Coercion**, from the target column's facts: `sql_type`, and `format` on
   the field as the parse layout for a time. Kind comes from a classifier over
   `sql_type` (integer, numeric scale, boolean, timestamp, bytea, and text as
   the remainder). It does not come from the generator type or from
   `types.SQLAffinity`, and Propose does not copy the column's `type` or
   `format`. A `format` on a non-time column is a compile error. A `constant`
   that cannot be coerced is a compile error too.
4. **`default`**, only when the value is null. A null written for a mapped
   nullable column stays NULL, which overrides a database default. Omitting the
   column is how the database default fires.
5. **Schema checks**, per row: `max_length`, numeric precision and scale, and
   membership of `enum_values`. A miss is a row error. The same checks on a
   `constant` run at compile time.

That leaves six transforms, and the set is closed:

| Transform | Argument | Notes |
|---|---|---|
| `trim` | `{}`, or `{cutset}` | |
| `case` | `upper`, `lower`, `title` | |
| `replace` | `{from, to}` | literal |
| `regex_replace` | `{pattern, to}` | compiled once, at load |
| `map` | inline pairs, with `default` | inline only - an external lookup table would need a file format of its own |
| `pad_left`, `pad_right` | `{to, with}` | only earns its place while fixed-width input exists |

A transform is encoded as a **single-key map**: the key names the operation, the
value carries its argument. That shape is chosen for the TUI rather than for the
file. A chain is a list, so the editor is a list editor - reorder, insert, delete -
and each step has exactly one argument slot to prompt for. Each transform also
declares what it accepts and produces, so a chain is checkable without reading a
row, which is what the mapping TUI does as you build it.

When the target column carries `enum_values`, Propose seeds the lookup with the
legal outputs and no incoming keys:

```yaml
- map:
    to: [active, suspended, closed]
```

`to` with no pairs is an unfinished lookup. The column counts as mapped, so it
is not a blocker, and the file can be saved. `import` refuses to run it.
Validate reports every non-null source value as unmapped. Replacing `to` with
pairs finishes it. The row-time `enum_values` check still applies to whatever
the lookup emits.

Every schema column is in exactly one of three places:

| Place | Meaning |
|---|---|
| `fields` | Mapped. One origin feeds this column |
| `omitted` | Left out of the `INSERT`. Identity, a database default, or nullable |
| unseen | In the schema, and in neither list |

A column in neither list is unseen. A `NOT NULL` column with no default and no
identity is a blocker there. Any other unseen column means the schema grew and
nobody has classified it yet. A name in `fields` or `omitted` that the schema
no longer has is **stale**. Compile reports every stale name and every blocker
in one pass. `mockagen map`
still writes the file and exits 1, because the file is what the next edit
fixes. `mockagen import` refuses a plan that is not clean. Re-running with
`-map` keeps every existing field entry and the `omitted` list, proposes only
for unseen columns, and does not rewrite a chain a person already edited.

#### Why not an expression language

The honest contender is [`expr-lang/expr`](https://github.com/expr-lang/expr),
because unlike Go templates or starlark it has a real type checker -
`expr.Compile(src, expr.Env(row), expr.AsKind(reflect.String))` gives exactly the
load-time validation this design wants. CEL is out on dependency weight alone
(protobuf, against a project whose entire non-test dependency list is six
modules). It is still the wrong default here, for two reasons that are about the
TUI rather than the language:

- **A TUI cannot build or edit an expression.** It can only offer a text box,
  at which point the mapping TUI is `$EDITOR` with a validate button and the
  bubbletea dependency bought nothing.
- **An expression is opaque in the other direction.** "Which source fields does
  this mapping read?" becomes an AST walk, and that question is what
  `unmapped.source` and the mapping board's left pane are built on. The answer
  has to stay a list of names in `source`, `sources`, and `coalesce`.

So: ship the narrow declarative form, and reserve `expr:` as a
documented-but-unimplemented key. If real mappings hit the wall, it goes in
behind that one key with `expr-lang/expr` and the analysable path stays the
default. The alternative - putting an expression in a v1 file format - is a
one-way door.

#### Prior art

The shape is borrowed, not invented:

- **Embulk** declares typed columns (`{name, type, format}`) and keeps a short
  ordered `filters` list separately. That split is the one above: a chain, then
  a type. The order here puts the chain first because the cell arrives as text.
- **pgloader** declares casts by source type, one block for "trim every
  varchar". This format does not take that block. A chain lives on the field,
  so the editor stays a list, and Propose repeats `trim` on a text target
  rather than hiding a transform in a second place.
- **Benthos / Redpanda Connect** is the Go precedent for ordered lists of
  single-key YAML maps, including the awkward part: a single-key map needs a
  custom `UnmarshalYAML` and round-trips badly through `map[string]any`, so
  decode the one key explicitly off a `yaml.Node`.
- **Singer and Meltano** do no transformation at all - taps load, dbt transforms
  afterwards. Worth internalising as the bound on ambition here: a transform list
  competes with SQL, and SQL wins.

### Reading what mockagen writes

Six of the seven output formats invert cleanly. The seventh, `sql`, does not:
reading it back means parsing `INSERT` statements, which is a SQL parser's job.
Bulk mode's output is a CSV, so that half round-trips anyway.

No reader adds a dependency. `yaml.v3` and `parquet-go` are already in the
module; fixed-width positions are sliced in the importer rather than decoded
with `go-fixedwidth`. That library ignores a tag it cannot parse, which is the
failure the writer already guards against, and the map's positions are data,
not struct tags. `sql` stays write-only: reading it back is a SQL parser.

| Format | Reader |
|---|---|
| csv / tsv, json / jsonl | v1. Text cells. JSON numbers keep their literal spelling |
| yaml | One sequence of mappings, or a stream of mapping documents. A scalar's `yaml.Node.Value` is the cell, so `01` is not rewritten before coercion |
| fluent | `timestamp`, tag, JSON object, separated by tabs. The object is the row. The prefix is not a column |
| fixed | `source.fixed` is `{name, start, end}`, 1-based and inclusive, the same numbers as the writer's positions and a different shape. A line whose length is not the layout width is a row error. The reader does not strip pad |
| parquet | Physical boolean, integer, float, and timestamp values are stored as that `Kind`. Apply copies a value whose kind already matches the column and does not parse it. A text chain on that field is a row error. UTF-8 and decimal columns stay text, because a float would drop scale. A date is civil text (`2006-01-02`), not an instant |

## What has to change in mockagen first

Most of what the three pieces need already exists, locked inside `package main`
under `cmd/mockagen`. This is the unglamorous part, and it comes first because
everything else leans on it.

| Change | Why |
|---|---|
| **`pkg/mockagen/types`**: promote the type catalog | Both TUIs have to offer the valid generator types and reject a typo. Half the list is `mapToFaker` in [`main.go`](../../cmd/mockagen/main.go), which nothing can import, and an unknown type does not error - it silently generates a random string, which matters tenfold once a *tool* writes the spec. The catalog carries more than a faker tag: what kind of value the type produces, which of `values`/`min`/`max`/`format` it needs, and an **`SQLAffinity`** (text/int/float/bool/time/binary) plus name hints - because inference needs the *reverse* mapping, sql type → candidate types, and without it the rules hardcode a second parallel table that will drift |
| **`pkg/mockagen/sqlgen`**: extract the dialect, *not* the writers | The importer needs the four dialects' hard-won correctness - MySQL's backslash and CR handling, mssql's `N''` prefix, the per-dialect bulk loaders. That is ~250 lines: `sqlDialect`, the alias table, `lookupSQLDialect`, `appendIdent`/`appendQualifiedIdent`/`appendString`/`identList`, `bulkLoadScript`, `maxRowsPerStatement`. The writers stay put; see below |
| **Readers on `Source`** | The inverse of the writers, in `pkg/importer` behind `OpenSource`. See *Reading what mockagen writes* |
| **An error-returning loader with `kind` dispatch** | [`LoadConfig`](../../pkg/mockagen/load_config.go) panics on every failure path and returns no error. A panic inside a bubbletea update loop takes the terminal's cooked mode with it. The existing function keeps its behaviour - tests pin the panics - and delegates to one that returns an error, a kind, and rejects a type the catalog does not know |
| **Subcommand CLI** | Four commands where there is one flag today |

### Why the writers stay in `package main`

Moving all 1154 lines of `writer.go` is the obvious-looking version of this
refactor and it costs more than it buys:

- `cmd/mockagen/integration_test.go:161,450` uses `csvTricky` and
  `csvRecordType`, which are defined in `cmd/mockagen/writer_test.go:604,561`,
  which uses `structFieldName` from `cmd/mockagen/main.go:111` - a *generator*
  concern. Test files are not importable, so the move forces those helpers into
  real packages, across the testcontainers migration that is in flight right now.
- `benchstat` matches rows by benchmark name, and a benchmark's name includes its
  package path. `BenchmarkWriteRecords` (`cmd/mockagen/bench_test.go:185`) calls
  `newRecordWriter` directly, so moving the package turns that commit's CI
  comparison into removed-on-base/added-on-head rows instead of a delta - for
  exactly the change most likely to regress write performance.

Extracting the dialect has neither problem: `writer.go` keeps
`type sqlDialect = sqlgen.Dialect`, its diff is a handful of lines, no benchmark
moves, and only the focused dialect tests (`TestLookupSQLDialect`,
`TestSQLIdentQuoting`, `TestBulkLoadScript*`, `TestSQLTable` - about 180 lines)
travel with it.

Two fields to add while extracting, both belonging with the four-way
disagreements the type already encodes: **placeholder style** (`$1` / `?` /
`@p1`) and **max bound parameters** - 65535 on postgres, 2100 on SQL Server, so
a batching sink's usable batch size is a function of column count, not a
constant.

And one trap to document rather than rediscover: `appendString` **drops NUL
bytes** and can never emit `NULL`. Both are right for a generator that produces
only non-NUL strings and has no null support, and both are wrong for an importer,
where dropping a NUL is silent data loss. So the importer gets a new
`AppendValue` - bare `NULL` for a null, unquoted digits for numbers, verbatim for
a raw expression, and an **error** on an embedded NUL - rather than overloading
`appendString`. Making `appendString` generic over `~string | ~[]byte` while it
moves lets one escaping implementation serve the writer's string fields and the
importer's byte arena with no conversion.

### CLI shape and three papercuts

Dispatch keys off a known-name set - `gen`, `schema`, `map`, `import`,
`version`, `help` - and anything else runs the legacy flag path over the whole
argument list. That handles `-config`, `--config`, `-config=x` and a bare
`mockagen` without reasoning about hyphens, and `mockagen -config x.yaml` keeps
working unchanged. Four subcommands is a `switch` and one `flag.NewFlagSet`
each; adding a CLI framework would make it the largest dependency in the repo.

Three papercuts are in the new commands' way:

- **`-output` does not exist.** The path is hardcoded to `./output/<name>.<ext>`
  and nothing calls `MkdirAll`, so a run outside a directory containing
  `output/` fails on `os.Create`. Ten lines, independent of everything else in
  this document, and worth shipping first and alone.
- **`-version` does not exist.** `.goreleaser.yaml:27` injects
  `-X main.version -X main.commit -X main.date` into variables that are not
  declared anywhere, so every release has been quietly discarding them.
- **`.yml` is rejected** - `allowedExt` lists `.yaml` only, and the mismatch
  panics.

## Package layout

```
pkg/mockagen/types       generator-type catalog, with SQLAffinity
pkg/mockagen/sqlgen      the four SQL dialects, identifier and value escaping
pkg/schema               the Schema kind: types, load/save, render, infer.go
pkg/schema/introspect    one Querier interface, per-dialect catalog queries
                         (written and verified: introspection-queries.md)
pkg/importer             Source, Record, Plan, Sink, Report. Readers are
                         Sources behind OpenSource. A separate reader package
                         would split that dispatch, so there is not one.
internal/dbtest          the container harness, extracted from integration_test
internal/tui/schemaui    bubbletea app for `mockagen schema`
internal/tui/mapui       bubbletea app for `mockagen map`
cmd/mockagen             subcommand dispatch; drivers.go registers the drivers
```

Inference is `infer.go` inside `pkg/schema`, not a package: twenty rules and a
lookup table do not need one. The TUIs are `internal/` because they are
applications, not API - their shape can change without a compatibility story,
while `pkg/schema` and `pkg/importer`, the parts another program would want, are
the public surface.

## `mockagen schema`

The command first:

```sh
mockagen schema -dialect postgres -dsn "$DATABASE_URL" -table public.users \
    -o users.schema.yaml [-render users.spec.yaml] [-update users.schema.yaml]
```

That is what CI, scripts and the introspection integration tests use, and it is
what the TUI calls.

The TUI is five screens over it, started with `-tui`. Without that flag the
command is unchanged, so a script never opens a screen.

**Connect.** Dialect, then a typed DSN, `$MOCKAGEN_DSN`, `$DATABASE_URL`, or a
saved connection. The file is `~/.config/mockagen/connections.yaml` and the path
is injected so a test never reads the real home directory:

```yaml
connections:
  - name: local
    dialect: postgres
    dsn: postgres://app@localhost:5432/app
    password_env: MOCKAGEN_DB_PASSWORD
```

`password_env` is the variable's name. The password is read only inside the
connect command and spliced into the DSN for that dial. Saving refuses a DSN
that already has a password (`user:pass@` or a `password=` parameter). A
`password` key in the file fails to load. SQLite has no password.

**Pick a table.** `introspect.Tables` already returns the list and the
approximate count. The filter is local. Space multi-selects. One file is
written per selected table.

**Map columns.** One row per column. Unresolved columns use the bad style.
`e` opens the catalog picker: `types.Candidates` for the column's affinity,
then the rest of `types.All`. Prompts for `values`, `min`, `max`, and `format`
come from those fields on `types.Type`, not from the type's name. A picked
type sets `inferred_by: user`. The live sample is a `tea.Cmd` that returns
rows; the screen does not infer types from them.

**Preview.** Another command, injected by `cmd/mockagen`, draws a few rows
from `generateFakes`. An unresolved column is shown as `UNRESOLVED` and is not
passed to the generator, which would otherwise emit a random string and look
successful. The generator stays in `package main`.

**Save.** Schema path, and an optional rendered spec. The write is a command.

No query, dial, or file read runs in `Update`. Model tests feed messages.
`TestMain` pins an Ascii color profile; the first `Update` in a view test is
a `tea.WindowSizeMsg`, because the model does not exist during `TestMain`.

### Inference

`infer.go` is an ordered rule list. Name rules run before type rules, because
a `varchar(255)` called `email` is an email address and a `varchar(255)` is
nothing in particular. `json` and `jsonb` are the exception, described under
*JSON columns*: the type is a document, so name rules do not fire and the
column stays unresolved. Key rules run before both, because uniqueness
constrains harder than naming. Each rule carries a stable id, which is what
lands in `inferred_by`:

| Rule id | Fires on | Chooses |
|---|---|---|
| `key:int-pk` | integer primary key | a row-number type (see the gap) |
| `key:text-pk` | text primary key, or `unique` text | GUID - nothing else guarantees uniqueness, and the TUI says so |
| `rule:enum` | an enum type, or a single-column `IN (...)` check | Custom List + `values` |
| `name:email` | `email`, `email_address`, `*_email` | Email Address |
| `name:first_name` | `first_name`, `fname`, `given_name` | First Name |
| `name:*_at` | `created_at`, `signed_up_at` | Datetime |
| `type:uuid` | `uuid`, `uniqueidentifier` | GUID |
| `type:bool` | `boolean`, `bit`, `tinyint(1)` | Boolean |
| `type:timestamp` | `timestamp`, `timestamptz`, `datetime`, `date` | Datetime |
| `type:numeric` | `numeric(p,s)` | Number, `decimals` from the scale and `min`/`max` from the precision |
| `type:text` | `text`, `varchar`, `char`, `nvarchar` | Sentence, capped by `max_length` |
| — | `json`, `jsonb` | unresolved. Name rules do not fire. `jsonb[]` is an array, not this row |

`rule:enum` is where introspection beats any amount of guessing: a column
constrained to three values gets exactly those three, and the generated data
satisfies the constraint by construction. `type:numeric` is where the
facts/decisions split pays off - precision and scale are facts, and they
pre-fill the decision.

A nullable column gets a starting `null_percentage`; a column with a database
default is left alone, because the default is the better value.

## `mockagen map`

```sh
mockagen map -schema users.schema.yaml -data legacy.csv \
    [-map existing.map.yaml] [-out users.map.yaml] [-sample 1000]
```

`-schema` and `-data` are required without `-tui`. `-out` defaults to stdout.
`-sample` is how many leading rows to profile and, unless a later validate
says otherwise, how many to push through the plan. The default is 1000. Exit 2
on a flag error, exit 1 when the written map is not clean, exit 0 when it is.
A partial map is still written.

`mockagen map -tui` asks for each file flag that was left off, with the bubbles
file picker. `-schema` and `-data` have to be chosen. `-map` is offered and
can be skipped. `-out` stays stdout unless the flag was passed, because the
picker can only select a file that already exists. A file that does not load
stays on that picker with the error. `q` cancels and writes nothing. Without
`-tui`, a missing required flag is still exit 2, so a script never opens a
screen.

**Source setup.** The board already shows the sniffed format, delimiter,
encoding, and field names. That is the setup screen. A second control for
delimiter, quote, or encoding is not added: the sample is one read, and
re-parsing under the screen is the second pass this command avoids. Fixed-width
positions are `source.fixed` in the map. Sampling a `.fixed` file without them
fails with that reason instead of guessing columns.

**The mapping board.** Source fields on the left, each with a profile computed
in one bounded pass over the sample: inferred type, null rate, a capped distinct
count, min and max length, a few distinct values. Target columns on the right
with their facts. Auto-match stops at the first pass that yields a single
winner: exact
name, then a normalised name (case-fold, spaces and hyphens to `_`, other
punctuation dropped), then a fuzzy match only when one target is strictly
nearer than every other. An ambiguous pair stays unmatched. A text target
starts with `trim`. A time or numeric target starts with an empty chain and no
invented `format`. An enum target starts with `trim` and the unfinished `to:`
lookup. Identity columns go straight to `omitted`. An unmatched column with a
database default, and an unmatched nullable column, go to `omitted`. An
unmatched `NOT NULL` column with no default and no identity stays unseen, and
the board renders it as a blocker.

`space` links the selected pair, `x` ignores a source field, `c` makes a target
constant, `o` moves a legal target to `omitted`, `t` opens the transform editor.
`s` writes the map and leaves the screen up. `q` is the only way out, and it
asks when the map differs from the copy at open or at the last `s`. Saving is
not how the screen closes, so a long map can be checkpointed in place.
Re-opening with `-map` after `mockagen schema -update` is the drift view: a new
column is unseen, a renamed column is a stale target, and `omitted` survives.
Propose fills only the unseen columns.

**The transform editor.** The chain for one field, then `null_if`, coercion,
`default`, and the schema checks. `importer.Stages` runs those steps with the
same functions as `Apply` and returns each one, so the screen can show the
sample beside them without a second mapper. Errors sit on the step that
failed. The chain is not checked against a separate accepts/produces table:
the only typed values that skip it are parquet physical types, and a chain on
one of those is already a row error.

**Validate.** When `Compile` returns no diagnostics, the check screen and the
board's preview are one `Run` of the in-memory sample through a discarding
sink, with `Limit` and `SampleRows` set to that sample. Errors are counted by
field and reason, with the line numbers `Report` already carries. A plan with
a diagnostic is not run, which is the same rule as `mockagen import`. The
editor can still show `Stages` for the one field being edited. The sample is
not re-read from disk: a second pass could observe a file that changed.

That is also why the importer is built first. These two panes are not a
reimplementation of the import logic - they are `Options.Limit` and
`Options.SampleRows` with a different renderer, which is why both are library
options rather than something the TUI loops over itself. The TUI calls
`Propose`, `Compile`, and `Run`. It does not grow a second mapper.

## `pkg/importer`

mockagen's writers are built so that "a run never has to hold more than one
record in memory", and CI benchmarks every commit against its predecessor. So the
row contract is pull-based and destination-filling, and the record type is built
for zero steady-state allocation.

Two decisions drive the rest. **A zero `Value` is NULL**, so a short CSV row and
an absent JSON key are NULL by construction rather than by convention. An
omitted target is not a value in that record: the sink's column list skips it.
And **text lives in a per-record arena, with values holding offsets into it** -
a `[]byte` field would be invalidated by the very append that grows the arena,
and `any` would heap-allocate on every int64, which at thirty columns is thirty
allocations per row before any work happens.

```go
// Kind is the SQL-shaped type of a Value. KindNull is the zero value so that a
// freshly reset Record is all-NULL: a short row and a missing JSON key then
// need no special case. Omitted targets are left off the record.
type Kind uint8

const (
	KindNull Kind = iota
	KindText
	KindInt
	KindFloat
	KindBool
	KindTime
	// KindRaw is text a sink must emit verbatim rather than quote - a DEFAULT,
	// a nextval(), a function call a constant asked for. Separate from
	// KindText because a sink that quoted it would insert the literal
	// characters "now()".
	KindRaw
)

// Value is one field of one row. Text is not stored here: it lives in the
// owning Record's arena, located by off and ln, because a slice into that
// arena would be invalidated by the append that grows it.
type Value struct {
	Kind    Kind
	num     int64   // KindInt, KindBool (0/1), KindTime (Unix nanos, UTC)
	flt     float64 // KindFloat
	off, ln int32   // KindText, KindRaw: index into Record.arena
}

// Record is one row: a fixed-width slice of Values plus the byte arena their
// text points into. Both are reused across every row of a run, so reading a
// row costs no allocations in steady state.
type Record struct {
	Values []Value
	// Line is the 1-based position of this row in its source, carried so an
	// error or a sample can name where it came from.
	Line int64

	arena []byte
}

// Reset prepares r to hold n all-NULL fields, reusing both backing arrays.
func (r *Record) Reset(n int)

// SetText copies b into the arena and points field i at it. The copy is the
// point: b is normally a window into the Source's read buffer, which the next
// read overwrites.
func (r *Record) SetText(i int, b []byte)

// Bytes returns field i's text. The result aliases the arena and is valid only
// until the next Next or Reset - the same contract sql.RawBytes and
// bufio.Scanner.Bytes carry, for the same reason.
func (r *Record) Bytes(i int) []byte

// Arg converts field i to something database/sql accepts. The one place a text
// field allocates, because database/sql copies its arguments anyway; a file
// sink never calls it.
func (r *Record) Arg(i int) any
```

```go
// Source yields the rows of one input file. Destination-filling rather than
// returning a Record, so a run allocates one row's worth of memory however
// many rows it reads - a channel of records, which is what generateFakes
// uses, is affordable when generating and not when loading a file.
type Source interface {
	// Fields names the source's columns in positional order, so a FieldMap is
	// resolved by name once rather than per row. Nil for a headerless source;
	// that file is addressed by the indexes stored on the map.
	Fields() []string
	// Next fills dst with the next row, reporting false at end of input. dst
	// is Reset first, so a short row leaves its tail NULL.
	Next(dst *Record) (bool, error)
	Close() error
}

// Column is one target column the sink will write. Kind is classified from
// SQLType, never from the schema's generator type. Omitted columns are absent
// from this list, so a database default fires and a nullable omission becomes
// NULL without an explicit NULL overriding a default.
type Column struct {
	Name       string
	Kind       Kind
	SQLType    string // the introspected spelling, for error messages
	Nullable   bool
	Identity   bool
	HasDefault bool
	MaxLength  int64 // 0 means the schema named no limit
	Precision  int64
	Scale      int64 // -1 means the schema named no scale
	EnumValues []string
}

// Sink consumes target-ordered rows. One interface serves a .sql file, a
// CSV+bulk-script bundle, a discarding validator and (later) a live
// database/sql connection, so a dry run differs from a real import only in
// which Sink is constructed.
type Sink interface {
	Open(ctx context.Context, t Target) error
	// Write takes a Record whose Values are in Target.Columns order. The
	// Record is reused by the caller, so a sink retaining a row must copy it.
	Write(ctx context.Context, r *Record) error
	Close(ctx context.Context) error
}

// Execer is the whole of *sql.DB a database sink would need. Declaring it here
// rather than taking *sql.DB keeps this package free of any driver import, so
// the caller chooses what to link and a test can substitute a recorder.
type Execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}
```

```go
// Plan is a compiled FieldMap: for each written column, where its value comes
// from and what happens on the way. Compiled once, so a bad regex, an unknown
// transform, a blocker, a stale name, an unfinished lookup, or an uncoercible
// constant is an error before the first row is read rather than on row 900,000.
type Plan struct{ /* ... */ }

// Apply maps src (source-ordered) into dst (write-list order). dst is Reset,
// so a mapped cell that produces null stays NULL. Omitted columns are not in
// dst; the sink's column list skips them.
func (p *Plan) Apply(src, dst *Record) error

type Options struct {
	OnError   ErrorPolicy // ErrorAbort is the zero value: a half-loaded
	                      // table is worse than a failed load
	MaxErrors int
	// Limit stops after n source rows and SampleRows keeps n mapped rows
	// verbatim. The map TUI's preview pane is these two plus a discarding
	// sink, which is why they live here and not in the TUI.
	Limit, SampleRows int
	// BatchSize is rows per statement. Zero lets the sink decide, which it
	// must: the usable size is a function of the column count against the
	// dialect's bound-parameter cap.
	BatchSize int
}

// RowError locates one failure precisely enough to fix the FieldMap.
type RowError struct {
	Line   int64
	Target string // target column; empty for a whole-row failure
	Err    error
}

type Report struct {
	Read, Written, Skipped int64
	Errors                 []RowError
	Samples                []Record // deep copies: samples outlive the row
	Duration               time.Duration
}

func Run(ctx context.Context, src Source, p *Plan, dst Sink, opts Options) (*Report, error)
```

A transform operates in place with a caller-owned scratch buffer -
`func(r *Record, i int, scratch *[]byte) error`. Transforms that shrink text
rewrite offsets without touching the arena; transforms that grow it append to the
arena's tail, which is safe because the arena only grows and offsets are indices.

The CLI:

```sh
mockagen import -map users.map.yaml -data legacy.csv \
    [-out out.sql] [-rejects rejects.csv] [-limit N] [-dry-run]
```

The schema path is read from the map and resolved against the map file's
directory. Before the first row, the source header is compared to
`source.fields` (or the row width to `indexes`). A mismatch exits 1 and names
both sides. A plan with a blocker, a stale name, or an unfinished lookup does
not run.

One detail deserves space, because it is otherwise learned the hard way. The bulk
CSV writer quotes *every* field, and the reason is NULL: postgres `COPY` and
mssql `BULK INSERT` read an unquoted empty field as NULL and a quoted one as the
empty string, while mysql `LOAD DATA` reads an unquoted `NULL` as NULL and a
quoted one as four characters. That was found by an integration test loading data
which had passed every golden-output test. An importer whose values carry
nullness therefore gets correct NULL handling out of the existing writer - and
closes the README's standing "nothing is ever written as NULL" caveat on the
import path, while the generator still has it.

## The generator gap

This is the part not to bury. A real table is full of `bigint`,
`numeric(12,2)`, `varchar(20)` and address columns, and three separate things are
missing before a schema-derived spec generates data that will load.

**1. There is no `Number` type, and `min`, `max`, `decimals` and
`null_percentage` are parsed and ignored - and they cannot be fixed by adding
catalog entries.** `pkg/mockagen/provider`'s own package doc says why:
`faker.AddProvider` registers a `func(reflect.Value) (any, error)` with "no way
to receive per-column config (min/max, a regex pattern, weights, etc.)". And
`generateFakes` hardcodes `t := reflect.TypeOf("")` for every field
(`cmd/mockagen/main.go:140`). So per-column parameters are blocked on a generator
mechanism.

The mechanism is small: a **per-column decorator pass**. Build a `[]decorator`
alongside `structArr`, nil for every column that needs no parameters, and apply
the non-nil ones in the worker loop after `faker.FakeData`. Every field is a
string, so a decorator is one `SetString`; the cost when unused is a nil check
per record. That single mechanism delivers `Number` with real min/max/decimals,
`null_percentage`, and the fix in the next point. Nullness also needs a
representation the all-strings record cannot express - the same problem this
document's `Kind` solves on the import side - which in its minimal form is a
null mask carried beside the record and consulted by the SQL and CSV writers.

**2. `Custom List` is already broken for the values the schema path will feed
it.** `mapToFaker["Custom List"]` is `"oneof:"` and the values are appended by
string concatenation (`cmd/mockagen/main.go:133-137`). faker splits that tag on
`:` and requires exactly one part (`faker.go:1341-1345`), then splits the values
on `,` and rejects a `,,` (`:1346-1352`). So an enum label containing a comma
silently becomes two choices, one containing a colon errors, and an empty label
errors. `rule:enum` copying `enum_values` straight into `values` walks into all
three. A decorator picking from `col.Values` directly removes the bug and a
string concatenation per column.

**3. Primary keys and uniqueness have no answer at all, and this is the biggest
hole of the three.** faker collides on everything except `uuid_hyphenated`, and
an integer primary key has no representable type. Every introspected table has a
primary key, so this is unavoidable the moment the schema TUI exists. A
row-number type closes it, with one caveat to write down rather than discover:
generation runs on up to four workers over a buffered channel
(`cmd/mockagen/main.go:155-180`), so records arrive out of order and an atomic
counter yields unique-but-shuffled numbers. For a surrogate key uniqueness is all
that is required; a monotone sequence would require a single worker.

And two facts the renderer simply cannot honour. There is no cross-table value
registry, so a spec rendered for a table with a **foreign key** produces values
that will fail the constraint - the renderer must emit a loud comment and the TUI
must show the column as unresolved rather than quietly rendering a `Word`. The
same applies to a `unique` column with no uniqueness-guaranteeing type.

### Where this work lives

Not in this document's scope. `PRD.md` on the `origin/features` branch already
specifies `Number` with min/max/decimals, `null_percentage`, `blank`, `formula`
and an expanded type list (Street Address, City, State, Country, Zip Code). That
branch is not an ancestor of `main` and `main` implements none of it. What the
PRD does *not* specify is the decorator mechanism, the null representation, the
`Custom List` bug or the primary-key hole - so this section names them, with
evidence, as what that work has to cover for this one to pay off.

Meanwhile the schema TUI does not guess. A column it cannot honestly type gets
`type: ""`, shows as `UNRESOLVED`, and the renderer refuses to write a spec while
any column is unresolved unless forced. Candidates are offered ranked by
`SQLAffinity` and then by name, so `numeric(10,2)` offers Number with the scale
and precision pre-filled, and `varchar(255)` named `street_address` honestly
offers nothing. The file is still complete and still useful: it is the mapping
TUI's input, and that path needs no generation at all. The gap blocks one of the
schema file's two consumers, not the work.

Street Address, City, State, Zip and Lat/Long are five parameter-free `provider`
entries that fit the existing `register(tag, func() string)` pattern and are
independent of all of the above - add them whenever. `Regex` is the one that
would honour CHECK constraints, and it needs a per-column parameter, i.e. another
decorator; defer it and have the TUI surface the constraint as a comment.

## Dependencies

bubbletea, bubbles and lipgloss for the TUIs. Database drivers for
**introspection only** - nothing on the import path needs one.

`.goreleaser.yaml:15` builds with `CGO_ENABLED=0`, so every driver must be pure
Go, and they are not equal in weight:

| Dialect | Driver | Weight |
|---|---|---|
| postgres | `jackc/pgx/v5` via `stdlib` | moderate; `x/crypto` and `x/text` are already in the graph |
| mysql | `go-sql-driver/mysql` | light - essentially one dependency |
| mssql | `microsoft/go-mssqldb`, **root package only** | moderate; the `azuread` subpackage pulls in the whole Azure identity SDK |
| sqlite | none - drive the `sqlite3` CLI | `modernc.org/sqlite` is a transpiled libsqlite3 plus `modernc.org/libc`, and its contribution alone is comparable to the entire current 21.5 MB binary |

Driving the CLI for sqlite is not a workaround, it is this repo's existing
committed position: `writer_test.go:354` execs `sqlite3`, and the sqlite bulk
script emits CLI dot-commands precisely because `.import` is not SQL.
`PRAGMA table_info`, `PRAGMA foreign_key_list` and `sqlite_master` through
`sqlite3 -json` is a dozen lines and costs no bytes. An in-process driver can sit
behind a build tag for anyone willing to pay for it.

The containment is an interface rather than a dependency:
`pkg/schema/introspect` takes a one-method `Querier` - the mirror of the
importer's `Execer` - so neither package imports a driver, a test can answer from
a canned result set, and the drivers are registered in exactly one file,
`cmd/mockagen/drivers.go`. Only linked packages cost binary bytes, so a program
importing `pkg/importer` pays nothing.

One honest note about build tags: a tag removes bytes from a binary, not a
requirement from `go.mod`, so a library consumer still downloads pgx and
mssqldb. The only real fix is a second module, and that means two `go.mod`s, a
`replace` during development, lockstep release tagging and `go mod tidy`
landmines - not worth it for a download-size concern nobody has raised. Single
module, import edge confined to `cmd/`, revisit if someone complains.

## Testing

House style throughout: stdlib `testing`, table-driven with inline expected
strings, no testify, no golden files on disk, a doc comment on every declaration
saying why it exists.

- **Transforms.** A table of input, spec, want / wantErr per transform. The
  cheapest tests here and the ones that will catch the most.
- **The importer, with no files at all.** A `sliceSource` over a `[][]string` and
  a recording sink make the whole pipeline a pure function, which is where the
  NULL-versus-empty-string table belongs. `go test ./...` stays container-free,
  as the `test` mise task promises.
- **Readers.** Round-trip each format through the writer and back, plus a
  meta-test that every `file_format` either has a reader or appears on an
  explicit write-only list - so adding an eighth format forces a decision instead
  of silently having no reader.
- **Inference.** A meta-test in the spirit of `TestBenchmarkSchemaTypesAreKnown`
  (`cmd/mockagen/main_test.go:55`): every type any rule can emit must exist in
  the catalog. A typo there does not fail, it silently generates random strings,
  which is the exact class of bug that test was written to catch.
- **Introspection**, behind `//go:build integration`. Extract the container
  harness from `cmd/mockagen/integration_test.go` into **`internal/dbtest`** with
  **no build tag on the package** - it is `internal`, nothing outside tests
  imports it, so it is never linked into the shipped binary, and testcontainers
  is already a direct dependency - keeping the `integration` tags on the test
  files that import it. Expose sqlite as a fourth target whose container step is
  a no-op and whose exec runs the local `sqlite3`, so the per-dialect table stays
  uniform at four rows. Then: inline DDL per dialect covering a primary key, an
  identity column, `NOT NULL`, `UNIQUE`, a `DEFAULT`, `varchar(n)`,
  `numeric(p,s)`, an enum or CHECK and a foreign key; assert the marshalled
  schema YAML as an inline string, per dialect, because the `sql_type` spellings
  legitimately differ (postgres `character varying`, mysql `varchar`). That 4×
  duplication is the feature - it pins exactly how the four disagree, the same
  thing `TestSQLWriterDialects` does for output.
- **Round-trip integration.** Generate from a rendered spec, import the result
  back through the map that describes it, and compare byte for byte. The
  strongest single check that the two halves agree - and the test that will fail
  until the generator gap closes, which is the point.
- **The TUIs**, without `teatest` - it lives under `charmbracelet/x/exp`, and an
  `exp` dependency for test infrastructure is a bad trade. Two rules make a
  bubbletea model testable instead: **no I/O in `Update`**, so every query, file
  read and import run is a `tea.Cmd` returning a message and a model test feeds
  `tea.KeyMsg`/`schemaLoadedMsg`/`importErrMsg` and asserts on state; and a fixed
  color profile plus a leading `tea.WindowSizeMsg` in the package's `TestMain`, so
  `View()` can be asserted against inline golden strings the way
  `TestBulkLoadScript` already does. Assert a command by executing it and
  switching on the message type.
- **Benchmarks** in the new packages, so existing benchmark names stay stable for
  CI's A/B: the import pipeline per sink, and a `BenchmarkPlanApply` that pins the
  arena design - `benchstat` reports `allocs/op`, so a regression from zero
  allocations per row shows up as an obvious multiple.

## Build order

| Phase | Contents |
|---|---|
| 0a | `-output` and `MkdirAll`, alone. Ten lines, independent, the sharpest existing papercut |
| 0b | Subcommand dispatch, `-version`, `.yml`; `pkg/mockagen/types` with `SQLAffinity`; `pkg/mockagen/sqlgen` extraction plus `AppendValue`; the error-returning loader with `kind` dispatch |
| 1 | `pkg/schema` + `introspect` for four dialects + `infer.go`, and `mockagen schema` as a non-interactive command, with the four-dialect integration tests |
| 2 | `pkg/importer` with the csv and json readers and the file sinks, plus `mockagen import` and non-interactive `mockagen map` |
| 3 | The remaining readers; then both TUIs, last, by which point the panes' requirements are known |
| — | In parallel, outside this document's scope: the generator work above. It blocks schema-derived generation and nothing else |

Phase 0 ships no feature and is the phase most worth not skipping. Every later
phase that reimplements something instead of reusing it is a divergence waiting
to be found by a failed load.

The per-task breakdown of these phases, with acceptance criteria for each, is in
[import-tooling-tasks.md](import-tooling-tasks.md). House conventions every task
is held to are in [AGENTS.md](../../AGENTS.md).

## Open questions

- **Transform extensibility.** The set is closed, which is what makes static
  checking and a list-editor TUI possible. If an escape hatch is wanted later,
  does it borrow whatever syntax `MockagenColumn.Formula` eventually gets rather
  than inventing a second one?
- **Multi-table imports.** One schema file is one table, so a parent and a child
  are two runs in the right order, by hand. The natural later addition is a
  `kind: ImportPlan` listing field maps in dependency order - worth knowing
  whether that is on the horizon before these formats harden.
- **Source profiling cost.** v1 reads the first N rows (`-sample`, default
  1000). For a 10 GB CSV, is that head enough, or does a later revision need a
  reservoir over the whole file?
