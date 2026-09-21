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
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

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

// newRecordWriter builds the recordWriter for config.FileFormat. json/yaml/
// fixed share one opened file; parquet manages its own file internally.
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
