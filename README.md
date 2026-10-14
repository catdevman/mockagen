# Mockagen
Generate massive amounts of local data blazingly fast.

## Compatability
My goal is to make it compatable with Mockaroo's schema format.  It does
not currently have all the same functionality.

## Install

### Using go install (recommended)

Requires Go 1.27 or later. This fetches, compiles, and installs the binary
into `$GOBIN` (default `$GOPATH/bin`, usually `~/go/bin`):

```
go install github.com/catdevman/mockagen/cmd/mockagen@latest
```

To install a specific version:

```
go install github.com/catdevman/mockagen/cmd/mockagen@v0.1.0
```

Make sure `$GOBIN` is on your `$PATH` so the `mockagen` command is available:

```
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Download a pre-built binary

Pre-built binaries for Linux, macOS, and Windows are available on the
[Releases](https://github.com/catdevman/mockagen/releases) page.

### Build from source

```
git clone https://github.com/catdevman/mockagen
cd mockagen
make
mv mockagen <somewhere_in_your_path>
```

## Usage
`mockagen -config <config_file_path>`

You can look at `test_data` directory for example configuration files.

## Config file formats

The `-config` flag accepts any of the following formats:

| Extension | Format |
|---|---|
| `.json` | JSON |
| `.yaml` | YAML |
| `.toml` | TOML |

## Output formats

The `file_format` field in a config selects how records are written to
`./output/<name>.<ext>`:

| `file_format` | Extension | Output |
|---|---|---|
| `json` | `.json` | One JSON array of record objects |
| `yaml` | `.yaml` | A YAML sequence, one item per record |
| `csv` | `.csv` | RFC 4180 rows, with an optional header |
| `fixed` | `.fixed` | Fixed-width lines (requires each column's `start_position`/`end_position`) |
| `parquet` | `.parquet` | Snappy-compressed Parquet |
| `fluent` | `.log` | Log lines in fluentd's `out_file` shape |
| `sql` | `.sql` | Multi-row `INSERT INTO` statements, or a bulk-load bundle |

### Fixed-width output (`fixed`)

Every column needs a `start_position` and an `end_position`, which are 1-based
and inclusive - a column at `1`-`10` occupies the line's first ten bytes. A
config that leaves them out, starts a column at `0`, or puts a start past its
end is rejected by name before anything is written, rather than panicking
inside the encoder or quietly dropping the column.

### CSV output (`csv`)

RFC 4180 rows, one per record. Set `include_header: true` for a leading
header row of column names:

```
id,full name,email
0dcfa976-4eff-4a24-b5de-f2e15f53a9ae,Ms. Lucy Hartmann,YQVcGFs@gUCfVCO.info
```

Headers use the same lowercased column names as the `json`, `yaml` and
`parquet` keys, so one config describes the same columns whichever format it
is written to.

Fields are quoted only where the format requires it - a comma, quote, CR or
LF anywhere in the value, or a leading space - which is exactly
`encoding/csv`'s rule; the output is byte-identical to what the standard
library's writer would produce. One asymmetry worth knowing: a value holding
a CRLF reads back with a bare LF, because `encoding/csv`'s *reader* collapses
it inside a quoted field. No encoding avoids that, and the generator does not
produce such values today.

This is the fastest of the output formats, and the smallest per record - it is
the one to reach for when loading a database in bulk, via `COPY FROM`
(postgres), `LOAD DATA INFILE` (mysql), `BULK INSERT` (mssql) or `.import`
(sqlite), all of which are far faster than the `INSERT` statements the `sql`
format writes.

### Log output (`fluent`)

Each record becomes a single line holding a timestamp, a tag, and the record
as a JSON object, separated by tabs - the format fluentd's `out_file` plugin
writes and its `out_file` parser reads back:

```
2026-09-20T20:38:09-04:00	mockagen.app.access	{"user":"rqCgpGK","level":"WARN"}
```

The timestamp is taken when the line is written, so records from the same run
commonly share one. Set the tag with a top-level `tag` field; it defaults to
`mockagen.<name>` when omitted. See
[test_data/config/logs.schema.yaml](test_data/config/logs.schema.yaml) for a
full example.

### SQL output (`sql`)

Records are written as multi-row `INSERT` statements, batched 1000 rows per
statement:

```sql
INSERT INTO "public"."users" ("id", "first_name", "email") VALUES
	('5dcc7536-3c67-483c-be5e-c3dc4e2d12e4', 'Macey', 'ujYtaeA@mLNUxoU.net'),
	('1dd2fece-a28b-4315-ad0e-0edf4bfc2f80', 'Audreanne', 'qlDTJjV@WIgWEmj.org');
```

Three top-level fields configure it:

| Field | Default | Meaning |
|---|---|---|
| `dialect` | `postgres` | Which database's spelling to write |
| `mode` | `insert` | `insert` for statements, `bulk` for a load bundle (below) |
| `table` | the config `name`, spaces to underscores | Table to insert into, optionally schema-qualified (`dbo.users`) |

`dialect` accepts `postgres`, `mysql`, `sqlite` and `mssql`, plus the usual
aliases (`postgresql`, `pg`, `mariadb`, `sqlite3`, `sqlserver`, `tsql`). An
unrecognised one is an error rather than a fallback. The dialects differ in
three places:

- **Identifier quoting** - `"col"` for postgres and sqlite, `` `col` `` for
  mysql, `[col]` for mssql. Every identifier is quoted, so a column named
  `order` needs no special handling.
- **Backslashes** - mysql reads backslash as an escape character by default,
  so each one is doubled; the other three take it literally.
- **Unicode literals** - mssql gets an `N'...'` prefix on any value holding
  non-ASCII text, without which the literal is `varchar` and anything outside
  the server's code page is replaced with `?` on insert.

Single quotes are escaped by doubling in all four. NUL bytes are dropped,
since no dialect's literal syntax can carry one portably.

The output is `INSERT` statements only - no `CREATE TABLE`, no transaction
wrapper - so it composes with whatever schema and load script you already
have. Nothing is ever written as `NULL`: the generator has no null support,
so a blank column arrives as an empty string and is inserted as one. See
[test_data/config/users.schema.sql.yaml](test_data/config/users.schema.sql.yaml)
for a full example.

#### Bulk loading (`mode: bulk`)

`INSERT` statements are the portable way in, not the fast one. With
`mode: bulk` the same config writes a **pair** of files instead:

```
output/users-bulk.csv        the data
output/users-bulk.load.sql   the command that loads it
```

The CSV is exactly what `file_format: csv` produces; what this adds is the
loader, which is the part that differs per database and is easy to get subtly
wrong:

| dialect | command | reads the file |
|---|---|---|
| `postgres` | `\copy` | client-side, relative to psql's directory |
| `mysql` | `LOAD DATA LOCAL INFILE` | client-side, needs `local_infile=1` |
| `sqlite` | `.mode csv` + `.import` | client-side (CLI dot-commands, not SQL) |
| `mssql` | `BULK INSERT` | **server-side** - see below |

The loader names the CSV by bare file name, so the two files are a movable
pair: zip them together, unpack them on the database host, and run the script
from that directory. Each generated script opens with the exact invocation and
the caveats that apply to it.

Two things to know:

- **`mssql` is the exception.** `BULK INSERT` is executed by the SQL Server
  process, so the path resolves on the server and relative to *its* working
  directory. After unpacking on the server you have to substitute an absolute
  path, or use the `bcp` command the script also prints, which does honour a
  relative name. `BULK INSERT` also takes no column list and maps fields by
  position, so the table's column order must match the CSV's - the script
  prints that order.
- **Empty fields load as empty strings, not `NULL`.** Every dialect can
  coerce them (`NULL ''`, `NULLIF`), but only for all columns at once, which
  would rewrite legitimately empty text. Each script carries the one-line
  change as a comment rather than applying it.

Two dialect limitations the integration tests pin down:

- **mysql** needs `--default-character-set=utf8mb4` on the client. mockagen
  writes UTF-8; without the flag the client announces latin1 and the server
  stores the mojibake re-encoding of every non-ASCII value. The generated
  script's invocation includes it.
- **mssql bulk cannot load non-ASCII on SQL Server for Linux.** `BULK INSERT`
  reads the file in the server's default codepage; `CODEPAGE = '65001'` is the
  documented fix and that platform rejects it outright, while neither a UTF-8
  collation on the target column nor `DATAFILETYPE` is a way round it. Use
  `mode: insert` there - its `N''` literals carry unicode correctly. On Windows
  SQL Server, add `CODEPAGE = '65001'` to the generated `WITH` list.

`mode: bulk` with `dialect: sqlite` rejects a schema-qualified `table`:
sqlite's `.import` takes a bare table argument and would otherwise create a
table whose name contains the dot, having exited successfully. See
[test_data/config/users.schema.bulk.yaml](test_data/config/users.schema.bulk.yaml)
for a full example.

## Developer tasks

Tasks are scripts in [.config/mise/tasks/](.config/mise/tasks). `mise tasks`
lists them:

```
$ mise tasks
bench             Run the benchmarks once against the working tree
bench-compare     A/B the working tree against another commit, interleaved, then benchstat
build             Build the mockagen binary into ./mockagen
clean             Remove the built binary
lint              Check formatting and run go vet, including the integration build tag
test              Run the unit tests (no containers needed)
test-integration  Run the SQL integration tests (starts and removes its own servers)
```

Run one with `mise run <name>`, and pass `--help` to any that take arguments:

```sh
mise run bench-compare --help     # [base] (default main), [rounds] (default 6)
mise run bench-compare HEAD~1 10
```

`mise install` provisions the Go toolchain pinned in
[.config/mise/config.toml](.config/mise/config.toml) to match go.mod. The CI
workflows use none of this - they call `go` directly and take their toolchain
from go.mod - so tasks can change freely without touching them.

## Integration tests

The unit tests pin what each dialect's bytes look like. The integration tests
check those bytes actually load, by running them through real postgres, mysql
and sqlite servers (and SQL Server), then reading every value back and
comparing it byte for byte:

```sh
mise run test-integration
```

That is the whole workflow. The servers are declared in
[internal/dbtest](internal/dbtest/dbtest.go), which is the shared harness the
integration tests across every package use, and started with testcontainers: each comes up the first time a test needs it - so running one
dialect's subtest never starts SQL Server - and all of them are removed when
the package's tests finish. testcontainers' reaper removes them even if the
run panics or is killed, so a cancelled run cannot leave three database
servers behind.

The tests are behind an `integration` build tag, so `mise run test` stays fast
and touches no containers, and they skip rather than fail where docker is not
installed. Each one drives its server's own CLI client rather than a Go SQL
driver, because that is what the bulk scripts are written for - psql's
`\copy` and mysql's `LOAD DATA LOCAL INFILE` are client features a driver
never exercises; testcontainers is here for the lifecycle and for
`Exec`/`CopyFileToContainer`, not for a connection string.

Readiness is checked by running each server's own client inside its container
(`psql -c "SELECT 1"`, `mysqladmin ping`, `sqlcmd -Q "SELECT 1"`) rather than
by probing a port, because all three of these images accept a connection
before they will answer a query - and postgres in particular reports ready via
`pg_isready` while it is still replaying its init scripts, so the probe has to
be a query.

They earn their keep: the quote-everything rule in bulk mode, mysql's
`ESCAPED BY ''`, the CR escaping in mysql literals, and both mssql
limitations above were all found this way, by loading data that had passed
every golden-output test.

## Benchmarks

Performance over time is tracked at
**[catdevman.github.io/mockagen/dev/bench](https://catdevman.github.io/mockagen/dev/bench/)**.

Every push to `main` runs the benchmark suite against the previous commit,
interleaved on a single runner, and publishes the result. See
[docs/benchmarking.md](docs/benchmarking.md) for how the comparison works and
how to run it locally.
