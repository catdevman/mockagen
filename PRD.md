# Mockagen — Product Requirements Document

## What Mockagen Is

Mockagen is a high-performance CLI tool for generating large volumes of mock/fake data locally. It is schema-compatible with [Mockaroo](https://mockaroo.com/), accepting JSON or YAML configuration files that describe columns and their types, then generating realistic fake data at high speed using Go's concurrency primitives.

**Current supported config input formats:** JSON, YAML, TOML

**Current supported output formats:** JSON, YAML, CSV, fixed-width, Parquet (broken — see below)

**Example usage:**
```bash
mockagen -config test_data/config/Mockadoo.schema.json
```

Generates output to `./output/<name>.<format>`.

---

## Current State Assessment

### What Works
- JSON and YAML config loading
- Core field types: GUID, First Name, Last Name, Email Address, Gender, Datetime, Custom List
- JSON, YAML, and fixed-width output
- Concurrent generation with a 48-worker goroutine pool
- Dynamic struct creation via reflection to map config columns to faker fields

### What Is Parsed But Not Implemented
| Field | Status |
|---|---|
| `null_percentage` | Parsed, never applied |
| `blank` | Parsed, never applied |
| `formula` | Parsed, never applied |
| `min` / `max` | Parsed, never applied |
| `decimals` | Parsed, never applied |
| `selectionStyle` / `distribution` | Parsed, never applied |
| Parquet output | References fixed-width marshal — broken |

---

## Proposed Improvements

### 1. Implement `null_percentage` and `blank` Handling

**What:** When `null_percentage: 30` is set on a column, 30% of generated values for that column should be `null` (or omitted). When `blank: 20` is set, 20% should be empty string.

**Why:** This is one of the most-used Mockaroo features for simulating real-world data quality issues — sparse columns, optional fields, missing data. Currently the fields are silently ignored, making configs that rely on them produce incorrect output without any warning.

**Scope:**
- Post-generation pass (or inline): for each record, per column, roll a random int; if below threshold, replace with nil/empty
- Needs to work across all output formats
- Applies to all column types

---

### 2. Expand Field Type Mappings

**What:** Add support for more Mockaroo-compatible field types that are currently unmapped and fall through to no-op generation. Priority types:

| Mockaroo Type | Faker mapping or implementation |
|---|---|
| `Number` | random int/float with optional `min`/`max`/`decimals` |
| `Boolean` | random true/false |
| `Phone` | `phone_number` faker tag |
| `URL` | `url` faker tag |
| `IPv4 Address` | `ipv4` faker tag |
| `Street Address` | `real_address` faker tag |
| `City` | `city` faker tag |
| `State` | `state` faker tag |
| `Country` | `country` faker tag |
| `Zip Code` | `zip_code` faker tag |
| `Company Name` | `name` faker tag |
| `Job Title` | `title_male` / `title_female` |
| `Paragraph` | `paragraph` faker tag |
| `Word` | `word` faker tag |
| `Color` | `color_name` |

**Why:** The current 7 supported types severely limits what kinds of schemas can be expressed. Most real-world Mockaroo schemas include types like Phone, Address, Boolean, or Number. Without them, mockagen is only useful for a narrow set of use cases.

---

### 3. Implement `min` / `max` Range Constraints

**What:** Apply `min` and `max` constraints during generation:
- **Datetime columns**: generate dates within the specified range (already parsed as strings, needs to be converted to time and bounded)
- **Number columns**: generate integers or floats within `[min, max]`
- **String columns with length constraints**: optionally bound string length

**Why:** Without range constraints, date fields generate arbitrary dates and number fields generate arbitrary values, making the data useless for time-bounded or value-bounded test scenarios (e.g., "orders from 2023", "ages 18–65").

**Scope:**
- Parse `min`/`max` strings to `time.Time` for date columns (support common formats)
- Use `rand.Int63n(max-min)+min` for integer ranges
- Use `rand.Float64()*(max-min)+min` for float ranges with `decimals` precision

---

### 4. Implement Formula Processing

**What:** Evaluate the `formula` field as a post-generation transformation on the value. Mockaroo supports formulas like:
- `upper(this)` — uppercase the value
- `lower(this)` — lowercase the value
- `trim(this)` — trim whitespace
- `"prefix_" + this` — string concatenation
- `this + "@example.com"` — append suffix

A minimal implementation can handle a fixed set of known formula patterns without a full expression parser.

**Why:** Formulas are widely used in Mockaroo schemas to derive or transform values — e.g., generating a username from a first name, or uppercasing a code field. Currently all formulas are silently dropped.

**Scope (MVP):**
- Parse simple function calls: `upper(this)`, `lower(this)`, `trim(this)`, `title(this)`
- Parse string concatenation: `"prefix" + this`, `this + "suffix"`
- Apply after value generation, before writing to output

---

### 5. Streaming Output (Memory Efficiency)

**What:** Instead of collecting all generated records into a `[]interface{}` slice in memory before marshaling, write records to the output file incrementally as they are generated.

**Why:** The current approach allocates the entire dataset in RAM before writing. Generating 1M+ records of wide schemas can consume gigabytes of memory. Streaming output would:
- Allow arbitrarily large datasets without OOM risk
- Start writing output immediately rather than after all generation completes
- Be more suitable for pipeline/pipe usage (`mockagen | kafka-producer`, etc.)

**Scope:**
- Replace the channel-collect-then-marshal pattern with a streaming encoder
- For JSON: use `json.Encoder` and write records one at a time inside the array
- For YAML: stream documents separated by `---`
- For fixed-width: each record is already independent, trivial to stream
- Worker pool stays the same; output writer becomes the consumer

---

## Status

| # | Improvement | Status |
|---|---|---|
| 1 | null_percentage / blank handling | **Completed** |
| 2 | Expand field type mappings | **Completed** |
| 3 | min/max range constraints | **Completed** |
| 4 | Formula processing | **Completed** |
| 5 | Streaming output | **Completed** |

---

## Implementation Plan

### Overall Sequencing

Implement in this order — each step builds on the previous:

1. **Improvement 5 (Streaming)** — restructures the main generation loop first, so improvements 1 and 3 slot cleanly into the new loop rather than requiring two separate refactors of the same code.
2. **Improvement 1 (null/blank)** — adds per-column post-generation mutation; needs the new streaming loop and introduces pointer types in the dynamic struct.
3. **Improvement 3 (min/max)** — builds on the pointer types from step 2 and the streaming loop from step 1.

### Files Changed

| File | Changes |
|---|---|
| `cmd/mockagen/main.go` | All three improvements; substantial rewrite of generation and output logic |
| `cmd/mockagen/main_test.go` | Update benchmark to call new `streamFakes(config, io.Discard)` signature |
| `pkg/mockagen/config.go` | No changes; fields already exist |

---

### Step 1 — Improvement 5: Streaming Output

**Goal:** Replace the in-memory `[]any` accumulation with a writer goroutine that drains `fakesCh` directly to disk.

**Changes to `main.go`:**

1. Add `bufio`, `encoding/json` to imports.
2. Rewrite `main()` to open the output file and pass an `io.Writer` down.
3. Replace `generateFakes(config) []any` with `streamFakes(config mockagen.MockagenConfig, w io.Writer)`.
4. Inside `streamFakes`, spawn a dedicated **writer goroutine** that reads from `fakesCh` and writes per format:
   - **JSON**: write `[`, then each record as JSON with `,` separator, then `]`
   - **YAML**: use `yaml.NewEncoder(w)` and call `enc.Encode(record)` per record
   - **Fixed-width**: call `fixed.Marshal([]any{record})` per record and write the bytes
5. Add a `writerDone chan struct{}` so `streamFakes` blocks until the writer goroutine finishes draining.
6. The `wg.Wait()` + `close(fakesCh)` pattern is unchanged; `<-writerDone` replaces the old collect loop.

**Changes to `main_test.go`:**

- Update benchmark calls from `generateFakes(config)` → `streamFakes(config, io.Discard)`.

**Risks:**
- `bufio.Writer.Flush()` must execute after `streamFakes` returns — ordering is correct since defers run on function return.
- Fixed-width: verify `go-fixedwidth` emits a trailing newline per record.
- Parquet: leave as-is or add explicit `panic("parquet not implemented")` — do not attempt to fix.

---

### Step 2 — Improvement 1: `null_percentage` and `blank`

**Goal:** After `faker.FakeData` populates a record, apply per-column null/blank overrides based on probability rolls.

**Changes to `main.go`:**

1. In the `structArr` build loop, use `*string` (pointer) for columns where `NullPercentage > 0` or `Blank > 0`:
   ```go
   if col.NullPercentage > 0 || col.Blank > 0 {
       fieldType = reflect.TypeOf((*string)(nil))  // *string → marshals as null
   } else {
       fieldType = reflect.TypeOf("")
   }
   ```
2. Build a `nullableCols []nullableCol` slice (struct index, null threshold, blank threshold) once before the worker pool starts.
3. Inside each worker goroutine, after `faker.FakeData`, iterate `nullableCols`:
   - Roll `rand.Int63n(100)`. If `< NullPercentage` → set field to `reflect.Zero(field.Type())` (nil pointer) and `continue`.
   - Roll again. If `< Blank` → set field to pointer to `""`.
4. `null_percentage` takes precedence over `blank` (null check first, with `continue`).

**Risks:**
- Verify faker v4 correctly populates `*string` fields (sets them to non-nil during `FakeData`). If not, generate with `string` fields first then swap — but faker v4 does handle pointers.
- Fixed-width + nil pointer: nil `*string` will render as empty/zero-width. Acceptable for fixed-width; document this behavior.
- `rand` is goroutine-safe in Go 1.20+ (this repo is Go 1.22). No mutex needed.

---

### Step 3 — Improvement 3: `min` / `max` Range Constraints

**Goal:** For Datetime columns with `min`/`max` set, generate dates bounded to that range. For Number columns, generate values in `[min, max]`.

**New helper functions in `main.go`:**

```go
// Convert strftime format string to Go time layout
func strftimeToGoLayout(format string) string

// Flexibly parse date strings (handles MM/DD/YYYY, YYYY-MM-DD, etc.)
func parseDateFlex(s string) (time.Time, error)

// Return a random time.Time between min and max (uniform)
func randTimeBetween(minT, maxT time.Time) time.Time
```

**Changes to `main.go`:**

1. In the `structArr` build loop, detect columns needing custom generation:
   - `type == "Datetime"` with non-empty `Min` or `Max`
   - `type == "Number"` (any)
   For these, set faker tag to `faker:"-"` (skip field) and record them in a `customCols []customCol` slice.
2. `customCol` struct holds: struct index, column type, parsed `minTime`/`maxTime`, go layout string, `minFloat`/`maxFloat`, `decimals`, `isFloat` flag.
3. Inside each worker goroutine, after the null/blank pass, iterate `customCols` and fill each field:
   - **Datetime**: `t := randTimeBetween(cc.minTime, cc.maxTime)` → `t.Format(cc.goLayout)`
   - **Number (int)**: `rand.Int63n(max-min) + min` → `strconv.FormatInt`
   - **Number (float)**: `rand.Float64()*(max-min) + min` → `fmt.Sprintf("%.*f", decimals, val)`

**Known quirk to handle:**
The existing test config `Mockadoo.schema.json` has `min: "12/24/2019"` (MM/DD/YYYY) but `format: "%Y/%m/%d"` (YYYY/MM/DD). The `parseDateFlex` multi-format fallback handles this — min/max are parsed flexibly, output is formatted using the column's `format` spec.

**Risks:**
- Verify faker v4 honors `faker:"-"` to skip a field. If not, use empty tag or separate struct.
- All times use UTC for consistency.
- If `min > max` in config, swap and log a warning rather than panic.
- `faker:"-"` will leave the field at its zero value (empty string / nil pointer), which is then overwritten by the custom generation pass — correct.

---

## Implementation Notes and Post-Implementation Issues

### What Was Completed

All three selected improvements were fully implemented in `cmd/mockagen/main.go` and covered by 24 passing unit and integration tests plus 2 benchmarks.

**New functions added:**
- `strftimeToGoLayout` — converts strftime format strings to Go time layout strings
- `parseDateFlex` — parses date strings in multiple common formats (handles Mockaroo's inconsistency between min/max format and the output `format` field)
- `randTimeBetween` — generates a uniform random time.Time between two bounds
- `buildStructFields` — encapsulates all struct-field, nullable-col, and custom-col construction
- `applyCustomCols` / `applyNullBlank` / `setStringField` — per-record mutation helpers
- `streamFakes` — replaces `generateFakes`; accepts `io.Writer` and streams records without buffering the full dataset in memory

### Deviations from the Plan

**YAML output format changed.** The original code produced a YAML array (`- field: value`). Streaming YAML uses `yaml.NewEncoder` which produces multi-document format (records separated by `---`). A true streaming YAML array would require manual YAML construction. The multi-document format is valid, more tool-friendly (e.g., piping to per-record processors), and is the accepted trade-off for streaming.

**Bonus fix: record count correctness.** The original code had a documented bug where integer division (`numberOfRecords / numWorkers`) silently truncated the total. For example, 500 records with 48 workers produced 480 (`10 × 48`). Fixed by distributing the remainder to the first N workers, so output always contains exactly `config.NumberOfRecords` records.

**Bonus fix: struct aliasing race.** The original code reused a single struct instance per worker and sent the same pointer to the channel repeatedly. Because the channel is unbuffered and the writer goroutine marshals asynchronously, this was a data race: the worker could overwrite the struct before marshaling finished. Fixed by allocating a new struct per record (`reflect.New(structType)` inside the loop). The struct type itself is computed once outside the loop for efficiency.

### Known Remaining Limitations

- **YAML output is multi-document, not array.** See deviation note above. If array format is required, a future improvement could stream a YAML sequence by manually prepending `- ` and indenting continuation lines.
- **Parquet output is still broken.** Falls through to the fixed-width `fixed.Marshal` path — unchanged from before. The streaming refactor makes the placeholder explicit.
- **Number type stores values as strings.** Because the dynamic struct uses `string` (or `*string`) for all fields, Number values are emitted as JSON strings (`"42"`) rather than JSON numbers (`42`). Supporting native JSON number types would require per-column type selection in the struct, which is a larger refactor.
- **Formula processing implemented** with `upper(this)`, `lower(this)`, `trim(this)`, `title(this)`, and string concatenation. Complex nested formulas (e.g. `upper("prefix_" + this)`) are not supported — only flat function calls or flat concat expressions.
- **Datetime without min/max uses faker's unbounded `date` tag.** These dates are not formatted using the column's `format` field; they use faker's default format. Only Datetime columns with min/max set go through the custom path with format support.

### New formats (completed 2026-03-16)

**TOML config input:**

`.toml` schema files are now accepted by `LoadConfig` alongside `.json` and `.yaml`. Uses `github.com/BurntSushi/toml`. TOML struct tags added to `MockagenConfig` and `MockagenColumn`. See `test_data/config/single.schema.toml` for an example.

**CSV data output:**

`file_format: "csv"` streams records as comma-separated values. When `include_header: true` is set, column names are written as the first row. Null fields (from `null_percentage`) are written as empty strings. Uses `encoding/csv` from the standard library — no new dependency.

---

### Improvements 2 and 4 (completed 2026-03-15)

**Improvement 2 — Expanded field types:**

14 new types added. Types with single-word faker tag values use `mapToFaker` directly. Types requiring multi-word values (Company Name, Job Title, Country) and address components (City, State, Zip Code, Street Address) use the `customCol` system.

| Type | Mechanism |
|---|---|
| Boolean, Phone, URL, IPv4/IPv6, Username, Domain Name, MAC Address, Word, Sentence, Paragraph, Color | `mapToFaker` faker tag |
| City, State, Zip Code, Street Address | `faker.GetRealAddress()` per record; City+State share the same generated address for consistency |
| Country, Company Name, Job Title | Go-side `listValues` slice (faker `oneof:` tag does not support spaces within values) |

**Improvement 4 — Formula processing:**

A post-generation `applyFormulas` pass runs after `applyCustomCols` and before `applyNullBlank`. Null fields skip formula application.

Supported formulas:
- `upper(this)`, `lower(this)`, `trim(this)`, `title(this)`
- `"literal" + this`, `this + "literal"`, `"prefix" + this + "suffix"`

**Known limitation discovered during implementation:**
- faker v4's `oneof:` tag does not support spaces within values (e.g. `oneof: Acme Corp,Globex Corp` panics at runtime). This was discovered during testing. The fix was to route those types through the custom `list` generation path instead.
