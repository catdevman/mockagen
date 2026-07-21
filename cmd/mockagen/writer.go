package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

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
	case "parquet":
		return newParquetWriter(outputFile, structArr)
	default:
		return nil, fmt.Errorf("unsupported file_format %q", config.FileFormat)
	}
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
