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

## Benchmarks

Performance over time is tracked at
**[catdevman.github.io/mockagen/dev/bench](https://catdevman.github.io/mockagen/dev/bench/)**.

Every push to `main` runs the benchmark suite against the previous commit,
interleaved on a single runner, and publishes the result. See
[docs/benchmarking.md](docs/benchmarking.md) for how the comparison works and
how to run it locally.
