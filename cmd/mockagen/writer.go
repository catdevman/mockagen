package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
		return newYAMLSeqWriter(f), nil
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
	// buf assembles each line in one allocation-free pass so the timestamp
	// and tag cost an append rather than a fmt call per record.
	buf []byte
	// stamp is the formatted timestamp every record in the current tick
	// shares, and written counts records since it was last refreshed.
	stamp   []byte
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
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if w.written%fluentClockTick == 0 {
		w.stamp = time.Now().AppendFormat(w.stamp[:0], time.RFC3339)
	}
	w.written++
	w.buf = append(w.buf[:0], w.stamp...)
	w.buf = append(w.buf, '\t')
	w.buf = append(w.buf, w.tag...)
	w.buf = append(w.buf, '\t')
	w.buf = append(w.buf, b...)
	w.buf = append(w.buf, '\n')
	_, err = w.w.Write(w.buf)
	return err
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
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = w.w.Write(b)
	return err
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
}

func newYAMLSeqWriter(f *os.File) *yamlSeqWriter {
	return &yamlSeqWriter{f: f, w: bufio.NewWriter(f)}
}

// WriteRecord marshals rec on its own, then reframes it as one YAML
// sequence item: the first line gets a "- " marker and every following
// line is shifted two spaces to stay aligned under it. Uniformly shifting
// every line preserves any indentation already inside the record's YAML.
func (w *yamlSeqWriter) WriteRecord(rec any) error {
	b, err := yaml.Marshal(rec)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	for i, line := range lines {
		if i == 0 {
			if _, err := w.w.WriteString("- "); err != nil {
				return err
			}
		} else {
			if _, err := w.w.WriteString("  "); err != nil {
				return err
			}
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
