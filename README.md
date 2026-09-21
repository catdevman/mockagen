# Mockagen
Generate massive amounts of local data blazingly fast.

## Compatability
My goal is to make it compatable with Mockaroo's schema format.  It does
not currently have all the same functionality.

## Install

### Using go install (recommended)

Requires Go 1.26 or later. This fetches, compiles, and installs the binary
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
| `fixed` | `.fixed` | Fixed-width lines (uses each column's `start_position`/`end_position`) |
| `parquet` | `.parquet` | Snappy-compressed Parquet |
| `fluent` | `.log` | Log lines in fluentd's `out_file` shape |

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

## Benchmarks

Performance over time is tracked at
**[catdevman.github.io/mockagen/dev/bench](https://catdevman.github.io/mockagen/dev/bench/)**.

Every push to `main` runs the benchmark suite against the previous commit,
interleaved on a single runner, and publishes the result. See
[docs/benchmarking.md](docs/benchmarking.md) for how the comparison works and
how to run it locally.
