package main

import (
	"bufio"
	// json/v2 rather than encoding/json: its MarshalWrite streams a value
	// straight into an io.Writer, so a record costs no intermediate []byte
	// at all. That matters here because the v1 API got measurably slower in
	// Go 1.27 - v1 is now implemented on top of v2, which added ~17% to a
	// batch write for this workload with identical allocation counts.
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/catdevman/mockagen/pkg/mockagen"
	fixed "github.com/ianlopshire/go-fixedwidth"
	"github.com/parquet-go/parquet-go"
	yaml "gopkg.in/yaml.v3"
)

// recordWriter incrementally writes generated records to an output file, so
// a run never has to hold more than one record in memory at a time.
type recordWriter interface {
	WriteRecord(rec any) error
	Close() error
}

// newRecordWriter builds the recordWriter for config.FileFormat. Every format
// but parquet shares one opened file; parquet manages its own internally.
func newRecordWriter(config mockagen.MockagenConfig, structArr []reflect.StructField, outputFile string) (recordWriter, error) {
	switch config.FileFormat {
	case "json":
		f, err := os.Create(outputFile)
		if err != nil {
			return nil, err
		}
		return newJSONArrayWriter(f), nil
	case "yaml":
		f, err := os.Create(outputFile)
		if err != nil {
			return nil, err
		}
		return newYAMLSeqWriter(f, structArr), nil
	case "fixed":
		if err := validateFixedWidth(config.Columns); err != nil {
			return nil, err
		}
		f, err := os.Create(outputFile)
		if err != nil {
			return nil, err
		}
		return newFixedWidthWriter(f), nil
	case "fluent":
		f, err := os.Create(outputFile)
		if err != nil {
			return nil, err
		}
		return newFluentWriter(f, fluentTag(config)), nil
	case "csv":
		f, err := os.Create(outputFile)
		if err != nil {
			return nil, err
		}
		return newCSVWriter(f, structArr, config.IncludeHeader), nil
	case "sql":
		d, err := lookupSQLDialect(config.Dialect)
		if err != nil {
			return nil, err
		}
		mode, err := lookupSQLMode(config.Mode)
		if err != nil {
			return nil, err
		}
		if mode == sqlModeBulk {
			return newSQLBulkWriter(outputFile, d, sqlTable(config), structArr, config.IncludeHeader)
		}
		f, err := os.Create(outputFile)
		if err != nil {
			return nil, err
		}
		return newSQLWriter(f, d, sqlTable(config), structArr), nil
	case "parquet":
		return newParquetWriter(outputFile, structArr)
	default:
		return nil, fmt.Errorf("unsupported file_format %q", config.FileFormat)
	}
}

// fluentTag picks the tag the "fluent" format stamps on every line:
// config.Tag when set, otherwise one derived from the config name. Fluentd
// routes events purely on their tag, so an empty one would leave the output
// unmatchable by any <match> block downstream.
func fluentTag(config mockagen.MockagenConfig) string {
	if tag := strings.TrimSpace(config.Tag); tag != "" {
		return tag
	}
	name := strings.ToLower(strings.Join(strings.Fields(config.Name), "."))
	if name == "" {
		return "mockagen"
	}
	return "mockagen." + name
}

// fluentWriter writes one line per record in the shape fluentd's out_file
// plugin uses: a timestamp, the tag, and the record as a JSON object,
// separated by tabs. Every field a log consumer needs sits on a single
// line, so the output can be tailed, split, or replayed record by record
// without parsing the file as a whole - unlike the json/yaml writers,
// whose output is only valid once the closing delimiter is written.
type fluentWriter struct {
	f   *os.File
	w   *bufio.Writer
	tag string
	// prefix is the "<timestamp>\t<tag>\t" every record in the current tick
	// shares, rebuilt only when the clock is resampled; written counts
	// records since that last happened.
	prefix  []byte
	written int
}

// fluentClockTick is how many consecutive records share one clock reading.
// time.Now() is a cheap vDSO read on a healthy host, but falls back to a
// real syscall where the vDSO clock is unavailable - measured at ~3.8us on
// one such machine, which by itself made this writer 5x slower per record
// than the json one. Sampling the clock once per tick caps that at a few
// nanoseconds per record while still letting the timestamp advance through
// a long file.
const fluentClockTick = 1024

func newFluentWriter(f *os.File, tag string) *fluentWriter {
	return &fluentWriter{f: f, w: bufio.NewWriter(f), tag: tag}
}

// WriteRecord timestamps the record at write time, to the resolution of
// fluentClockTick. Consecutive lines therefore share a timestamp - which a
// run would produce anyway, since a whole batch is generated inside the same
// second and fluentd's time field only carries second resolution.
func (w *fluentWriter) WriteRecord(rec any) error {
	if w.written%fluentClockTick == 0 {
		w.prefix = time.Now().AppendFormat(w.prefix[:0], time.RFC3339)
		w.prefix = append(w.prefix, '\t')
		w.prefix = append(w.prefix, w.tag...)
		w.prefix = append(w.prefix, '\t')
	}
	w.written++
	if _, err := w.w.Write(w.prefix); err != nil {
		return err
	}
	if err := json.MarshalWrite(w.w, rec); err != nil {
		return err
	}
	return w.w.WriteByte('\n')
}

func (w *fluentWriter) Close() error {
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

type jsonArrayWriter struct {
	f     *os.File
	w     *bufio.Writer
	first bool
}

func newJSONArrayWriter(f *os.File) *jsonArrayWriter {
	w := &jsonArrayWriter{f: f, w: bufio.NewWriter(f), first: true}
	w.w.WriteByte('[')
	return w
}

func (w *jsonArrayWriter) WriteRecord(rec any) error {
	if !w.first {
		if err := w.w.WriteByte(','); err != nil {
			return err
		}
	}
	w.first = false
	return json.MarshalWrite(w.w, rec)
}

func (w *jsonArrayWriter) Close() error {
	if err := w.w.WriteByte(']'); err != nil {
		return err
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

type yamlSeqWriter struct {
	f *os.File
	w *bufio.Writer
	// keys holds the pre-rendered "<key>:" for each struct field, in field
	// order. It is nil when the record type is not a flat struct of strings,
	// which sends WriteRecord down the general yaml.Marshal path instead.
	keys [][]byte
	buf  []byte
}

// newYAMLSeqWriter pre-renders the mapping keys from structArr, which never
// change across records. The whole point is to avoid yaml.Marshal per record:
// it builds a node tree and formats it from scratch every time, which made
// this writer cost more than *generating* the data it was writing (see
// docs/benchmarking.md).
func newYAMLSeqWriter(f *os.File, structArr []reflect.StructField) *yamlSeqWriter {
	w := &yamlSeqWriter{f: f, w: bufio.NewWriter(f)}
	keys := make([][]byte, 0, len(structArr))
	for _, field := range structArr {
		if field.Type.Kind() != reflect.String {
			// A non-string column would need real type-aware emission;
			// hand the whole job back to yaml.Marshal rather than guess.
			return w
		}
		name := field.Tag.Get("yaml")
		if name == "" {
			return w
		}
		key := appendYAMLScalar(nil, name)
		keys = append(keys, append(key, ':'))
	}
	w.keys = keys
	return w
}

// WriteRecord emits rec as one item of a YAML sequence: the first line
// carries the "- " marker and the rest are indented two spaces to stay
// aligned under it.
func (w *yamlSeqWriter) WriteRecord(rec any) error {
	if w.keys == nil {
		return w.writeMarshaled(rec)
	}
	v := reflect.ValueOf(rec)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || v.NumField() != len(w.keys) {
		return w.writeMarshaled(rec)
	}
	w.buf = w.buf[:0]
	for i, key := range w.keys {
		if i == 0 {
			w.buf = append(w.buf, '-', ' ')
		} else {
			w.buf = append(w.buf, ' ', ' ')
		}
		w.buf = append(w.buf, key...)
		w.buf = append(w.buf, ' ')
		w.buf = appendYAMLScalar(w.buf, v.Field(i).String())
		w.buf = append(w.buf, '\n')
	}
	_, err := w.w.Write(w.buf)
	return err
}

// writeMarshaled is the general path for record types the fast path cannot
// describe: marshal the record on its own, then shift every line right by
// two spaces so it sits under the "- " marker. Shifting uniformly preserves
// any indentation already inside the record's YAML.
func (w *yamlSeqWriter) writeMarshaled(rec any) error {
	b, err := yaml.Marshal(rec)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for i, line := range lines {
		prefix := "  "
		if i == 0 {
			prefix = "- "
		}
		if _, err := w.w.WriteString(prefix); err != nil {
			return err
		}
		if _, err := w.w.WriteString(line); err != nil {
			return err
		}
		if err := w.w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return nil
}

// yamlReservedWords are the words a plain scalar must not be: YAML resolves
// each of them to a boolean or null rather than to the string it looks like.
// The list covers YAML 1.1's spellings, which yaml.v3 still recognises on
// read, not just YAML 1.2's true/false/null.
var yamlReservedWords = [...]string{
	"y", "n", "yes", "no", "on", "off", "true", "false", "null", "~",
}

// yamlReserved reports whether s is one of those words, ignoring case. A
// linear scan of ten short strings beats a map keyed on strings.ToLower(s),
// which allocated a lowercased copy for every capitalised value that got
// this far - 4 allocations per record on the benchmark schema.
func yamlReserved(s string) bool {
	if len(s) > 5 {
		return false
	}
	for _, word := range yamlReservedWords {
		if strings.EqualFold(s, word) {
			return true
		}
	}
	return false
}

// appendYAMLScalar appends s to dst as a YAML scalar that reads back as
// exactly s.
//
// The plain (unquoted) test is deliberately narrow rather than a full
// implementation of YAML's plain-scalar grammar: requiring a leading ASCII
// letter and a body drawn from a small alphabet rules out every indicator
// character, every numeric form, and any occurrence of ": " or " #" in one
// pass, leaving only the reserved words to check. Anything else is
// double-quoted, which is always safe.
func appendYAMLScalar(dst []byte, s string) []byte {
	if yamlPlainSafe(s) {
		return append(dst, s...)
	}
	// A JSON string literal is also a valid YAML 1.2 double-quoted scalar -
	// YAML 1.2 is a JSON superset - so this borrows jsontext's escaping
	// rather than reimplementing it.
	quoted, err := jsontext.AppendQuote(dst, s)
	if err != nil {
		// Only invalid UTF-8 can land here; fall back to the escaping
		// jsontext would apply after replacing the bad bytes.
		return strconv.AppendQuote(dst, strings.ToValidUTF8(s, "\uFFFD"))
	}
	return quoted
}

func yamlPlainSafe(s string) bool {
	if s == "" {
		return false
	}
	if c := s[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '-', c == '.', c == '@', c == '/', c == '+':
		default:
			return false
		}
	}
	return !yamlReserved(s)
}

func (w *yamlSeqWriter) Close() error {
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

// validateFixedWidth checks that every column declares a usable position
// range, before any output file is created.
//
// go-fixedwidth reads 1-based inclusive positions from each field's `fixed`
// tag and silently ignores a tag it cannot parse, which turns three separate
// config mistakes into failures a long way from their cause:
//
//   - With no usable positions anywhere, the computed line length stays 0 and
//     the encoder's line buffer panics writing its first fill byte:
//     "index out of range [0] with length 0".
//   - A start_position of 0 makes it index the line at -1 and panic:
//     "slice bounds out of range [-1:]".
//   - A start_position past its end_position is dropped without a word, so
//     that column is simply absent from every record - the quiet failure of
//     the three, and the reason this checks the range rather than only
//     guarding the panic.
//
// Every offending column is named in one error, since a config that got one
// range wrong usually got several wrong.
func validateFixedWidth(cols []mockagen.MockagenColumn) error {
	if len(cols) == 0 {
		return fmt.Errorf(`file_format "fixed" needs at least one column`)
	}
	var problems []string
	for _, col := range cols {
		if col.StartPosition < 1 || col.EndPosition < col.StartPosition {
			problems = append(problems, fmt.Sprintf("%q (start_position %d, end_position %d)",
				col.Name, col.StartPosition, col.EndPosition))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf(`file_format "fixed" needs 1-based start_position and end_position on `+
		"every column, with start_position <= end_position; these do not: %s",
		strings.Join(problems, ", "))
}

type fixedWidthWriter struct {
	f     *os.File
	w     *bufio.Writer
	enc   *fixed.Encoder
	first bool
}

func newFixedWidthWriter(f *os.File) *fixedWidthWriter {
	w := bufio.NewWriter(f)
	return &fixedWidthWriter{f: f, w: w, enc: fixed.NewEncoder(w), first: true}
}

// WriteRecord encodes rec as a single line. fixed.Encoder.Encode only
// inserts a line terminator *between* elements of a slice passed in one
// call - encoding one record per call (what streaming requires) never adds
// one, so we add it ourselves between records.
func (w *fixedWidthWriter) WriteRecord(rec any) error {
	if !w.first {
		if _, err := w.w.WriteString("\n"); err != nil {
			return err
		}
	}
	w.first = false
	return w.enc.Encode(rec)
}

func (w *fixedWidthWriter) Close() error {
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

// maxRowsPerRowGroup bounds how many rows parquetWriter buffers before
// flushing a row group to disk. parquet-go defaults this to math.MaxInt64
// (one row group for the whole file), which would scale memory with the
// total row count - exactly what streaming is meant to avoid.
const maxRowsPerRowGroup = 100_000

// parquetWriter wraps parquet-go's GenericWriter[any], the library's
// documented replacement for the deprecated non-generic Writer when the
// schema isn't known until runtime (our records come from reflect.StructOf,
// not a compile-time Go type). parquet.SchemaOf infers the schema from a
// zero-value sample built from structArr, reading each field's `parquet`
// struct tag for its column name - no manual schema construction needed,
// unlike the fraugster/parquet-go writer this replaced.
type parquetWriter struct {
	f *os.File
	w *parquet.GenericWriter[any]
}

func newParquetWriter(outputPath string, structArr []reflect.StructField) (*parquetWriter, error) {
	f, err := os.Create(outputPath)
	if err != nil {
		return nil, err
	}
	sample := reflect.New(reflect.StructOf(structArr)).Interface()
	schema := parquet.SchemaOf(sample)
	w := parquet.NewGenericWriter[any](f, schema,
		parquet.Compression(&parquet.Snappy),
		parquet.MaxRowsPerRowGroup(maxRowsPerRowGroup),
	)
	return &parquetWriter{f: f, w: w}, nil
}

func (w *parquetWriter) WriteRecord(rec any) error {
	_, err := w.w.Write([]any{rec})
	return err
}

func (w *parquetWriter) Close() error {
	if err := w.w.Close(); err != nil {
		return err
	}
	return w.f.Close()
}

// maxRowsPerStatement bounds how many row tuples go into a single INSERT.
// SQL Server rejects a table value constructor holding more than 1000 rows
// ("the number of row value expressions in the INSERT statement exceeds the
// maximum allowed number of 1000 row values"), the lowest hard cap of the
// four dialects. The others cap a statement by size rather than row count -
// MySQL's max_allowed_packet, Postgres' 1GB message limit - and 1000 rows of
// generated data stays well inside both, so one limit serves all of them.
const maxRowsPerStatement = 1000

// sqlDialect describes the handful of places the four supported databases
// disagree about how an INSERT is spelled. The statement shape itself -
// INSERT INTO t (cols) VALUES (...),(...); - is common to all of them.
type sqlDialect struct {
	name string
	// identOpen/identClose bracket a quoted identifier. All four dialects
	// escape a literal identClose inside the identifier by doubling it.
	// Identifiers are always quoted, so a column named "order" or "select"
	// needs no special handling - and the generator cannot produce one that
	// would break out of the quotes.
	identOpen, identClose byte
	// escapeBackslash says backslash is an escape character inside a string
	// literal, which MySQL alone treats it as by default (NO_BACKSLASH_ESCAPES
	// off). That means a lone backslash would swallow the character after it,
	// so each is doubled - and it also means CR and LF can be written as the
	// escapes \r and \n rather than as raw bytes. The other three take
	// backslash literally, where doubling would insert two and \r would read
	// as the letter r.
	escapeBackslash bool
	// unicodePrefix is written before a literal containing non-ASCII text.
	// SQL Server needs N'...' to make it an nvarchar literal - an unprefixed
	// one is varchar, and anything outside the server's code page is replaced
	// with "?" on the way in. The other three store the file's UTF-8 as-is.
	unicodePrefix string
}

var sqlDialects = map[string]sqlDialect{
	"postgres": {name: "postgres", identOpen: '"', identClose: '"'},
	"sqlite":   {name: "sqlite", identOpen: '"', identClose: '"'},
	"mysql":    {name: "mysql", identOpen: '`', identClose: '`', escapeBackslash: true},
	"mssql":    {name: "mssql", identOpen: '[', identClose: ']', unicodePrefix: "N"},
}

// sqlDialectAliases maps the other names these databases go by onto the
// canonical keys of sqlDialects, so a config saying "postgresql" or
// "sqlserver" works without the user having to guess our spelling.
var sqlDialectAliases = map[string]string{
	"postgresql": "postgres",
	"pgsql":      "postgres",
	"pg":         "postgres",
	"sqlite3":    "sqlite",
	"mariadb":    "mysql",
	"sqlserver":  "mssql",
	"tsql":       "mssql",
}

// defaultSQLDialect is what an empty dialect field means. Postgres' spelling
// is the standard one - double-quoted identifiers, no backslash escapes - so
// its output is also the most portable of the four.
const defaultSQLDialect = "postgres"

// lookupSQLDialect resolves a config's dialect field. An unrecognised name is
// an error rather than a fallback: silently writing a different dialect's
// escaping would produce a file that only fails later, inside the database.
func lookupSQLDialect(name string) (sqlDialect, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = defaultSQLDialect
	}
	if canonical, ok := sqlDialectAliases[key]; ok {
		key = canonical
	}
	d, ok := sqlDialects[key]
	if !ok {
		valid := slices.Sorted(maps.Keys(sqlDialects))
		return sqlDialect{}, fmt.Errorf("unsupported sql dialect %q: want one of %s", name, strings.Join(valid, ", "))
	}
	return d, nil
}

// appendIdent appends ident as a quoted identifier.
func (d sqlDialect) appendIdent(dst []byte, ident string) []byte {
	dst = append(dst, d.identOpen)
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == d.identClose {
			dst = append(dst, c)
		}
		dst = append(dst, c)
	}
	return append(dst, d.identClose)
}

// appendQualifiedIdent appends a possibly schema-qualified table name,
// quoting each dotted part separately. Quoting "dbo.users" whole would name a
// single table whose name contains a dot, which is not what anyone writing
// that means.
func (d sqlDialect) appendQualifiedIdent(dst []byte, name string) []byte {
	for i, part := range strings.Split(name, ".") {
		if i > 0 {
			dst = append(dst, '.')
		}
		dst = d.appendIdent(dst, part)
	}
	return dst
}

// appendString appends s as a string literal that reads back as exactly s.
//
// The loop is bytewise rather than over runes because every byte of a
// multi-byte UTF-8 sequence has its high bit set, and so can never equal one
// of the ASCII bytes being escaped - the non-ASCII bytes simply pass through.
//
// A CR or LF is written as an escape where the dialect supports one; see the
// case below for why that is not cosmetic.
//
// NUL is dropped. No dialect here can carry one in a literal: Postgres
// rejects it in text outright, SQLite's own quoting truncates at it, and only
// MySQL's \0 escape would survive - so there is no byte sequence that means
// "a NUL" in all four, and dropping it keeps every file loadable.
func (d sqlDialect) appendString(dst []byte, s string) []byte {
	if d.unicodePrefix != "" && !isASCII(s) {
		dst = append(dst, d.unicodePrefix...)
	}
	dst = append(dst, '\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			// Doubling is valid in all four; MySQL's \' alternative is not.
			dst = append(dst, '\'', '\'')
		case '\\':
			if d.escapeBackslash {
				dst = append(dst, '\\')
			}
			dst = append(dst, '\\')
		case '\r', '\n':
			// Only where backslash escapes are available. A raw CR is the
			// problem case: the mysql client strips it from the end of an
			// input line so it tolerates CRLF-terminated scripts, and does
			// so even inside a quoted literal, which silently turns a
			// generated CRLF into a bare LF. Escaping both keeps every
			// literal on one line and immune to any line-based handling.
			if !d.escapeBackslash {
				dst = append(dst, c)
				break
			}
			if c == '\r' {
				dst = append(dst, '\\', 'r')
			} else {
				dst = append(dst, '\\', 'n')
			}
		case 0:
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '\'')
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// sqlTable picks the table the "sql" format inserts into: config.Table when
// set, otherwise one derived from the config name. The derived form joins the
// name's words with underscores, since an unquoted "web logs" would have to
// be written as a quoted identifier forever after.
func sqlTable(config mockagen.MockagenConfig) string {
	if table := strings.TrimSpace(config.Table); table != "" {
		return table
	}
	name := strings.ToLower(strings.Join(strings.Fields(config.Name), "_"))
	if name == "" {
		return "mockagen"
	}
	return name
}

// sqlColumnName is the column a struct field is inserted into: its `sql` tag
// (what the generator writes), else its `json` tag, else the field name
// lowercased so a hand-built record type still produces usable SQL.
func sqlColumnName(field reflect.StructField) string {
	if name := field.Tag.Get("sql"); name != "" {
		return name
	}
	if name := field.Tag.Get("json"); name != "" {
		return name
	}
	return strings.ToLower(field.Name)
}

// sqlWriter writes records as multi-row INSERT statements. Rows are batched
// maxRowsPerStatement at a time because one statement per record costs the
// database a round trip and a plan each - the same file loads an order of
// magnitude faster in batches - while a single statement for the whole run
// would exceed every dialect's limit on a large one.
type sqlWriter struct {
	f *os.File
	w *bufio.Writer
	d sqlDialect
	// prefix is the "INSERT INTO t (cols) VALUES" that opens each statement,
	// rendered once since the table and columns never change across records.
	prefix  []byte
	numCols int
	buf     []byte
	// rows counts tuples written into the statement currently open; 0 means
	// the next record starts a new one.
	rows int
}

func newSQLWriter(f *os.File, d sqlDialect, table string, structArr []reflect.StructField) *sqlWriter {
	w := &sqlWriter{f: f, w: bufio.NewWriter(f), d: d, numCols: len(structArr)}
	w.prefix = append(w.prefix, "INSERT INTO "...)
	w.prefix = d.appendQualifiedIdent(w.prefix, table)
	w.prefix = append(w.prefix, " ("...)
	for i, field := range structArr {
		if i > 0 {
			w.prefix = append(w.prefix, ", "...)
		}
		w.prefix = d.appendIdent(w.prefix, sqlColumnName(field))
	}
	w.prefix = append(w.prefix, ") VALUES"...)
	return w
}

// WriteRecord appends one row tuple to the open statement, terminating it
// once it holds maxRowsPerStatement rows.
//
// Every generated column is a Go string (see generateFakes), so every value
// is written as a string literal; a column typed otherwise is formatted and
// quoted the same way, which the four dialects all resolve against the target
// column's real type. Nothing is ever written as NULL - the generator has no
// null support, so a blank column arrives as an empty string and is inserted
// as one.
func (w *sqlWriter) WriteRecord(rec any) error {
	v := reflect.ValueOf(rec)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("sql writer: record is %s, want a struct", v.Kind())
	}
	if v.NumField() != w.numCols {
		return fmt.Errorf("sql writer: record has %d fields, want %d", v.NumField(), w.numCols)
	}

	if w.rows == 0 {
		w.buf = append(w.buf[:0], w.prefix...)
	} else {
		w.buf = append(w.buf[:0], ',')
	}
	w.buf = append(w.buf, '\n', '\t', '(')
	for i := 0; i < v.NumField(); i++ {
		if i > 0 {
			w.buf = append(w.buf, ", "...)
		}
		field := v.Field(i)
		if field.Kind() == reflect.String {
			w.buf = w.d.appendString(w.buf, field.String())
		} else {
			w.buf = w.d.appendString(w.buf, fmt.Sprint(field.Interface()))
		}
	}
	w.buf = append(w.buf, ')')

	w.rows++
	if w.rows == maxRowsPerStatement {
		w.buf = append(w.buf, ';', '\n')
		w.rows = 0
	}
	_, err := w.w.Write(w.buf)
	return err
}

// Close terminates a partially filled statement, so the file is valid SQL
// whatever the record count happened to be.
func (w *sqlWriter) Close() error {
	if w.rows > 0 {
		if _, err := w.w.WriteString(";\n"); err != nil {
			return err
		}
		w.rows = 0
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

// csvColumnName is the header a struct field is written under: its `csv` tag
// (what the generator writes), else its `json` tag, else the field name
// lowercased so a hand-built record type still produces a usable header.
func csvColumnName(field reflect.StructField) string {
	if name := field.Tag.Get("csv"); name != "" {
		return name
	}
	if name := field.Tag.Get("json"); name != "" {
		return name
	}
	return strings.ToLower(field.Name)
}

// csvNeedsQuotes reports whether a field has to be quoted, mirroring
// encoding/csv's Writer.fieldNeedsQuotes for a comma delimiter. Matching the
// standard library exactly - rather than quoting on some looser rule of our
// own - means encoding/csv's Reader is guaranteed to read back what this
// writes, and lets the tests assert byte equality against it.
//
// Note that only the *first* rune's space-ness forces quotes: that is the
// stdlib's rule, because a leading space is what some readers would strip,
// and it is deliberately not symmetric.
func csvNeedsQuotes(s string) bool {
	if s == "" {
		return false
	}
	// A lone `\.` is quoted because an unquoted one terminates input in
	// Postgres' COPY and in some MySQL dumps.
	if s == `\.` {
		return true
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n', '\r', '"', ',':
			return true
		}
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

// appendCSVField appends s as one field. With always set it is quoted
// regardless; otherwise it is quoted only where the format requires, which is
// what encoding/csv does. Inside quotes the single transformation is doubling
// a quote: CR and LF are legal verbatim in a quoted field, so passing them
// through keeps the value byte-identical through a round trip.
//
// The loop is bytewise because a quote cannot occur inside a multi-byte UTF-8
// sequence, so non-ASCII simply passes through.
func appendCSVField(dst []byte, s string, always bool) []byte {
	if !always && !csvNeedsQuotes(s) {
		return append(dst, s...)
	}
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' {
			dst = append(dst, '"', '"')
		} else {
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// csvWriter writes records as RFC 4180 rows, with an optional header taken
// from the column names.
//
// It renders rows itself rather than calling encoding/csv, whose Writer.Write
// takes a []string - which would mean building and filling a slice of
// per-field strings for every record, the one allocation the other writers
// here go out of their way to avoid. On a five-column record that measured
// 174ns and 1 alloc per record against 129ns and none for the version below,
// for output that is byte-identical either way (see
// TestCSVWriterMatchesStdlib).
type csvWriter struct {
	f       *os.File
	w       *bufio.Writer
	numCols int
	buf     []byte
	// quoteAll quotes every field rather than only those the format
	// requires. Bulk loading needs it; see newBulkCSVWriter.
	quoteAll bool
}

// newCSVWriter writes the header row immediately when one is wanted, so the
// file is well-formed even for a run that produces no records at all. Like
// the json writer's opening bracket, the write lands in the bufio.Writer and
// any error surfaces from Close's Flush.
func newCSVWriter(f *os.File, structArr []reflect.StructField, includeHeader bool) *csvWriter {
	return newCSVWriterOpts(f, structArr, includeHeader, false)
}

// newBulkCSVWriter is the variant the sql format's bulk mode uses: identical
// except that every field is quoted.
//
// Quoting is not cosmetic there. All three bulk loaders tell NULL apart from
// text by whether the field was quoted, so a field this writer would
// otherwise leave bare gets reinterpreted on the way in:
//
//   - postgres COPY and mssql BULK INSERT read an unquoted empty field as
//     NULL, and a quoted one as the empty string;
//   - mysql LOAD DATA, with FIELDS ENCLOSED BY set, reads the unquoted word
//     NULL as NULL and the quoted one as those four characters.
//
// Both were caught by TestIntegrationBulk loading data this writer produced.
// The plain csv format keeps the narrow rule, where staying byte-identical to
// encoding/csv is worth more than disambiguating for a loader.
func newBulkCSVWriter(f *os.File, structArr []reflect.StructField, includeHeader bool) *csvWriter {
	return newCSVWriterOpts(f, structArr, includeHeader, true)
}

func newCSVWriterOpts(f *os.File, structArr []reflect.StructField, includeHeader, quoteAll bool) *csvWriter {
	w := &csvWriter{f: f, w: bufio.NewWriter(f), numCols: len(structArr), quoteAll: quoteAll}
	if !includeHeader {
		return w
	}
	for i, field := range structArr {
		if i > 0 {
			w.buf = append(w.buf, ',')
		}
		w.buf = appendCSVField(w.buf, csvColumnName(field), quoteAll)
	}
	w.buf = append(w.buf, '\n')
	w.w.Write(w.buf)
	return w
}

// WriteRecord writes rec as one row. Every generated column is a Go string
// (see generateFakes); a column typed otherwise is formatted with fmt.Sprint
// and quoted by the same rules.
func (w *csvWriter) WriteRecord(rec any) error {
	v := reflect.ValueOf(rec)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("csv writer: record is %s, want a struct", v.Kind())
	}
	if v.NumField() != w.numCols {
		return fmt.Errorf("csv writer: record has %d fields, want %d", v.NumField(), w.numCols)
	}

	w.buf = w.buf[:0]
	for i := 0; i < v.NumField(); i++ {
		if i > 0 {
			w.buf = append(w.buf, ',')
		}
		field := v.Field(i)
		if field.Kind() == reflect.String {
			w.buf = appendCSVField(w.buf, field.String(), w.quoteAll)
		} else {
			w.buf = appendCSVField(w.buf, fmt.Sprint(field.Interface()), w.quoteAll)
		}
	}
	w.buf = append(w.buf, '\n')
	_, err := w.w.Write(w.buf)
	return err
}

func (w *csvWriter) Close() error {
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

// sqlMode selects what the "sql" file format writes: INSERT statements, or a
// CSV data file plus a script that bulk-loads it.
type sqlMode int

const (
	sqlModeInsert sqlMode = iota
	sqlModeBulk
)

// lookupSQLMode resolves a config's mode field, accepting the words a user
// might reasonably reach for. Like the dialect, an unrecognised one is an
// error rather than a fallback to the default.
func lookupSQLMode(name string) (sqlMode, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "insert", "inserts", "statements":
		return sqlModeInsert, nil
	case "bulk", "bulk_import", "copy", "load":
		return sqlModeBulk, nil
	}
	return 0, fmt.Errorf("unsupported sql mode %q: want insert or bulk", name)
}

// identList renders cols as a quoted, comma-separated column list.
func (d sqlDialect) identList(cols []string) string {
	var b []byte
	for i, col := range cols {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = d.appendIdent(b, col)
	}
	return string(b)
}

// sqlBulkWriter writes the records as CSV and, on Close, a script that loads
// that CSV into the target table.
//
// Bulk loading is the reason this mode exists: every one of the four
// databases can ingest a CSV far faster than it can execute the equivalent
// INSERT statements, but each spells the command differently enough - and
// with enough non-obvious caveats about where the file is read from - that
// generating it is most of the value. The CSV itself is exactly what the
// "csv" file format produces; what this adds is the command.
type sqlBulkWriter struct {
	csv        *csvWriter
	scriptPath string
	script     []byte
}

// newSQLBulkWriter derives both output paths from the one the caller computed
// for the format: <name>.sql becomes <name>.csv for the data and
// <name>.load.sql for the script.
func newSQLBulkWriter(outputFile string, d sqlDialect, table string, structArr []reflect.StructField, includeHeader bool) (*sqlBulkWriter, error) {
	// sqlite's .import takes a bare table argument: given a dotted name it
	// creates a table whose name contains the dot, and exits 0 having loaded
	// every row into the wrong place. The qualified form stays legal in
	// insert mode, where it addresses an ATTACHed database as sqlite
	// intends, so only this path has to refuse it.
	if d.name == "sqlite" && strings.Contains(table, ".") {
		return nil, fmt.Errorf("sql dialect sqlite with mode bulk cannot load into qualified table %q: "+
			"sqlite's .import would create a table of that literal name - use the bare table name", table)
	}
	base := strings.TrimSuffix(outputFile, filepath.Ext(outputFile))
	csvPath := base + ".csv"
	f, err := os.Create(csvPath)
	if err != nil {
		return nil, err
	}
	cols := make([]string, len(structArr))
	for i, field := range structArr {
		cols[i] = csvColumnName(field)
	}
	return &sqlBulkWriter{
		csv:        newBulkCSVWriter(f, structArr, includeHeader),
		scriptPath: base + ".load.sql",
		// The script names the CSV by base name only, so it resolves when the
		// script is run from the directory holding both files.
		script: bulkLoadScript(d, table, cols, filepath.Base(csvPath), includeHeader),
	}, nil
}

func (w *sqlBulkWriter) WriteRecord(rec any) error {
	return w.csv.WriteRecord(rec)
}

// Close finishes the CSV first, so the script is only written once the data
// file it points at is complete.
func (w *sqlBulkWriter) Close() error {
	if err := w.csv.Close(); err != nil {
		return err
	}
	return os.WriteFile(w.scriptPath, w.script, 0o644)
}

// bulkLoadScript renders the dialect's bulk-load command for csvName, which
// is a bare file name resolved relative to wherever the script is run.
//
// The four differ in more than syntax, and the comments in each generated
// script say so, because the differences decide whether the command works at
// all: postgres and mysql are given their client-side forms (\copy, LOAD DATA
// LOCAL INFILE) since only those resolve a relative path against the CWD of
// the process running the script; mssql's BULK INSERT has no client-side form
// at all and reads the path on the server; and sqlite's .import is a CLI
// dot-command rather than SQL.
//
// Null handling is deliberately left off. Each dialect can turn empty fields
// into NULL (postgres' NULL ”, mysql's NULLIF), but only for every column at
// once - which would silently rewrite a legitimately empty text value - so
// the scripts carry it as a comment instead.
// bulkScriptPreamble opens every dialect's script: what it loads, and that
// the script and its CSV are a movable pair. Data is rarely generated on the
// database host, so the two files are written to be zipped up and unpacked
// together wherever the load actually runs - which is why the command inside
// names the CSV by bare name rather than by any path from this machine.
func bulkScriptPreamble(b *strings.Builder, quotedTable, csvName string) {
	fmt.Fprintf(b, "-- Bulk load for %s, generated by mockagen.\n", quotedTable)
	fmt.Fprintf(b, "-- This script and %s are a pair: keep them in the same directory - zip\n", csvName)
	b.WriteString("-- them together to move them to the database host - and run this from there.\n--\n")
}

func bulkLoadScript(d sqlDialect, table string, cols []string, csvName string, includeHeader bool) []byte {
	var b strings.Builder
	quotedTable := string(d.appendQualifiedIdent(nil, table))
	colList := d.identList(cols)

	switch d.name {
	case "postgres":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- Run with:\n--   psql -d <database> -f <this file>\n--\n")
		b.WriteString("-- \\copy is a psql client-side meta-command: it reads the file on the\n")
		b.WriteString("-- machine running psql, relative to that process's working directory, and\n")
		b.WriteString("-- has to stay on a single line. The server-side COPY is faster but needs an\n")
		b.WriteString("-- absolute path the database server itself can see, plus pg_read_server_files.\n--\n")
		b.WriteString("-- To load empty fields as NULL rather than empty strings, add NULL '' to the\n")
		b.WriteString("-- WITH list below. It applies to every column, so it is left off here.\n")
		header := ""
		if includeHeader {
			header = ", HEADER"
		}
		fmt.Fprintf(&b, "\\copy %s (%s) FROM '%s' WITH (FORMAT csv%s)\n", quotedTable, colList, csvName, header)

	case "mysql":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- Run with:\n--   mysql --local-infile=1 -D <database> < <this file>\n--\n")
		b.WriteString("-- LOCAL reads the file on the client, relative to its working directory, and\n")
		b.WriteString("-- needs local_infile enabled on both client and server. Dropping LOCAL makes\n")
		b.WriteString("-- the server read the path instead, which secure_file_priv usually restricts.\n--\n")
		b.WriteString("-- To load empty fields as NULL, read the columns into variables and SET each\n")
		b.WriteString("-- one to NULLIF(@col, '') in place of the plain column list below.\n")
		fmt.Fprintf(&b, "LOAD DATA LOCAL INFILE '%s' INTO TABLE %s\n", csvName, quotedTable)
		// ESCAPED BY '' is not optional. LOAD DATA defaults to treating
		// backslash as an escape character, so a backslash in the data -
		// which the CSV carries literally, RFC 4180 having no escape
		// character but the doubled quote - would be swallowed along with
		// whatever followed it. Caught by TestIntegrationBulk, where "\\."
		// arrived as ".".
		b.WriteString("\tFIELDS TERMINATED BY ',' ENCLOSED BY '\"' ESCAPED BY ''\n")
		b.WriteString("\tLINES TERMINATED BY '\\n'\n")
		if includeHeader {
			b.WriteString("\tIGNORE 1 LINES\n")
		}
		fmt.Fprintf(&b, "\t(%s);\n", colList)

	case "mssql":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- Run with:\n--   sqlcmd -d <database> -i <this file>\n--\n")
		fmt.Fprintf(&b, "-- WARNING: BULK INSERT is executed by the SQL Server process, so '%s' is\n", csvName)
		b.WriteString("-- resolved on the SERVER - and relative to that process's working directory,\n")
		b.WriteString("-- not the one you run sqlcmd from. Having unpacked this pair on the server,\n")
		b.WriteString("-- substitute the absolute path of the directory holding them. To load from\n")
		b.WriteString("-- the client instead, bcp does resolve a relative name against your own\n")
		b.WriteString("-- working directory:\n")
		fmt.Fprintf(&b, "--   bcp %s in %s -c -t, -S <server> -d <database> -T\n--\n", quotedTable, csvName)
		b.WriteString("-- BULK INSERT takes no column list: it maps CSV fields to table columns by\n")
		b.WriteString("-- POSITION, so the table's column order must match the CSV's, which is:\n")
		fmt.Fprintf(&b, "--   %s\n--\n", strings.Join(cols, ", "))
		b.WriteString("-- FORMAT = 'CSV' needs SQL Server 2017 or later.\n--\n")
		b.WriteString("-- NON-ASCII DATA: the CSV is UTF-8, and BULK INSERT otherwise reads it in\n")
		b.WriteString("-- the server's default codepage, silently storing mojibake. On Windows,\n")
		b.WriteString("-- add CODEPAGE = '65001' to the WITH list to fix that. On SQL Server for\n")
		b.WriteString("-- Linux that option is rejected outright (\"Keyword or statement option\n")
		b.WriteString("-- 'CODEPAGE' is not supported on the 'Linux' platform\"), and neither a\n")
		b.WriteString("-- UTF-8 collation on the target column nor DATAFILETYPE helps - the\n")
		b.WriteString("-- latter cannot be combined with FORMAT = 'CSV' at all. If your data has\n")
		b.WriteString("-- non-ASCII values and your server runs on Linux, use mode: insert\n")
		b.WriteString("-- instead: its N'' literals carry unicode correctly.\n--\n")
		b.WriteString("-- One quirk to know: BULK INSERT loads an empty field as NULL rather than\n")
		b.WriteString("-- as an empty string when the target column is nullable, even though the\n")
		b.WriteString("-- CSV quotes it. Declare the column NOT NULL if you need '' preserved.\n")
		fmt.Fprintf(&b, "BULK INSERT %s\n\tFROM '%s'\n", quotedTable, csvName)
		firstRow := 1
		if includeHeader {
			firstRow = 2
		}
		fmt.Fprintf(&b, "\tWITH (FORMAT = 'CSV', FIRSTROW = %d, FIELDTERMINATOR = ',', ROWTERMINATOR = '0x0a', FIELDQUOTE = '\"');\n", firstRow)

	case "sqlite":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- These are sqlite3 CLI dot-commands, not SQL. Run with:\n")
		b.WriteString("--   sqlite3 <database file> < <this file>\n--\n")
		b.WriteString("-- .import appends to the table when it exists, and creates it from the CSV\n")
		b.WriteString("-- otherwise - every column typed TEXT. --skip needs sqlite3 3.32 or later.\n")
		b.WriteString(".mode csv\n")
		skip := ""
		if includeHeader {
			skip = "--skip 1 "
		}
		fmt.Fprintf(&b, ".import %s%s %s\n", skip, csvName, table)
	}
	return []byte(b.String())
}
