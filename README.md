# Mockagen
Generate massive amounts of local data blazingly fast.

## Compatibility
My goal is to make it compatible with Mockaroo's schema format.  It does
not currently have all the same functionality.

## Install
- `git clone https://github.com/catdevman/mockagen`
- `make` (in the root of the directory)
- `mv mockagen <somewhere_in_your_path>`

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

Set `file_format` in the config to control the output written to `./output/<name>.<format>`:

| `file_format` | Description |
|---|---|
| `json` | JSON array of objects |
| `yaml` | YAML multi-document stream (records separated by `---`) |
| `csv` | Comma-separated values; set `include_header: true` for a header row |
| `fixed` | Fixed-width columns (requires `start_position`/`end_position` per column) |
