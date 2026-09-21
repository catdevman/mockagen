package main

// Benchmarks that decompose a run into its two halves - generating records
// and writing them out - and compare every output format against the others
// on one identical schema.
//
// Read them together: BenchmarkGenerateRecords is the generation cost all
// formats share, BenchmarkWriteRecords is what each format adds on top, and
// BenchmarkPipeline is the two combined the way main() runs them. A format
// whose write cost is small next to generation is not worth optimising.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen"
)

// benchRecords is how many records one benchmark op covers. An op is a whole
// batch rather than a single record so each measurement includes opening the
// file and Close() - the parquet footer and row-group flush are a real
// per-file cost that a per-record loop would amortise to nothing, and the
// buffered writers only pay for their flushes at a realistic batch size.
const benchRecords = 1000

// benchSizes are the record counts the write benchmark sweeps. The top end
// stops at 10k deliberately: every op rewrites the whole file, so a larger
// sweep would push hundreds of megabytes through the runner's disk per pass
// without telling us anything the 10k point does not.
var benchSizes = []int{10, 1000, 10000}

// benchFormats is every file_format newRecordWriter supports.
var benchFormats = []string{"json", "yaml", "fixed", "parquet", "fluent"}

// benchFieldWidth is the column width handed to the fixed-width encoder.
// Wide enough that the generated values are not systematically truncated,
// which would make "fixed" look cheap for the wrong reason.
const benchFieldWidth = 64

// benchConfig builds a mixed-type schema - UUID, names, network addresses,
// an enum, and a free-text sentence - so no single faker generator dominates
// the numbers. Fixed-width positions are filled in for every format, not
// just "fixed", so all five run against an identical column set.
func benchConfig(format string, rows int) mockagen.MockagenConfig {
	types := []struct{ name, typ string }{
		{"id", "GUID"},
		{"first_name", "First Name"},
		{"last_name", "Last Name"},
		{"email", "Email Address"},
		{"client_ip", "IP Address"},
		{"company", "Company Name"},
		{"level", "Custom List"},
		{"message", "Sentence"},
	}
	columns := make([]mockagen.MockagenColumn, 0, len(types))
	for i, t := range types {
		col := mockagen.MockagenColumn{
			Name:          t.name,
			Type:          t.typ,
			StartPosition: int64(i*benchFieldWidth + 1),
			EndPosition:   int64((i + 1) * benchFieldWidth),
		}
		if t.typ == "Custom List" {
			col.Values = []string{"DEBUG", "INFO", "WARN", "ERROR"}
			col.SelectionStyle = "random"
		}
		columns = append(columns, col)
	}
	return mockagen.MockagenConfig{
		NumberOfRecords: rows,
		FileFormat:      format,
		Name:            "bench",
		Tag:             "mockagen.bench",
		Columns:         columns,
	}
}

// drain collects a whole generation run into a slice. The write benchmarks
// call it outside the timed region so they measure the writer alone.
func drain(config mockagen.MockagenConfig) ([]any, []reflect.StructField) {
	fakesCh, structArr := generateFakes(config)
	records := make([]any, 0, config.NumberOfRecords)
	for fake := range fakesCh {
		records = append(records, fake)
	}
	return records, structArr
}

// reportOutputSize writes one throwaway batch and turns the resulting file
// size into two reported metrics:
//
//   - B/s, via SetBytes, is the throughput that makes formats comparable.
//     (Reported by `go test` as MB/s.)
//     Raw file size does not: parquet compresses and fixed-width pads, so the
//     same records legitimately produce very different byte counts, and
//     comparing wall-clock alone would credit a format for writing less.
//
// It returns the per-record byte count for the caller to report as B/rec
// *after* its timed loop - b.ResetTimer deletes any metric reported before
// it, so that one cannot be set here alongside SetBytes.
//
// The measurement comes from a priming run outside the timed region, so the
// stat call and the first-write page faults are not charged to the benchmark.
func reportOutputSize(b *testing.B, config mockagen.MockagenConfig, structArr []reflect.StructField, path string, records []any) float64 {
	b.Helper()
	w, err := newRecordWriter(config, structArr, path)
	if err != nil {
		b.Fatal(err)
	}
	for _, rec := range records {
		if err := w.WriteRecord(rec); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(fi.Size())
	return float64(fi.Size()) / float64(len(records))
}

// BenchmarkGenerateRecords is the shared baseline for the two benchmarks
// below: the cost of producing benchRecords records, with no writer attached.
// Generation is format-independent apart from the fixed-width struct tags, so
// one format stands in for all of them.
func BenchmarkGenerateRecords(b *testing.B) {
	config := benchConfig("json", benchRecords)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fakesCh, _ := generateFakes(config)
		// Draining is mandatory: generateFakes returns once the workers are
		// spawned, so an unconsumed channel measures setup only while leaking
		// a blocked worker pool per iteration.
		for range fakesCh {
		}
	}
}

// BenchmarkPipeline runs generation and writing together, in the same
// streaming shape as main(): records are consumed off the channel as they
// arrive rather than collected first.
func BenchmarkPipeline(b *testing.B) {
	for _, format := range benchFormats {
		b.Run(format, func(b *testing.B) {
			config := benchConfig(format, benchRecords)
			records, structArr := drain(config)
			path := filepath.Join(b.TempDir(), "out."+outputExt(format))
			// Sized from the priming batch. Every op generates fresh records,
			// so the real output wobbles by a few bytes around this - the
			// point is a stable per-format divisor, not an exact count.
			bytesPerRecord := reportOutputSize(b, config, structArr, path, records)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w, err := newRecordWriter(config, structArr, path)
				if err != nil {
					b.Fatal(err)
				}
				fakesCh, _ := generateFakes(config)
				for fake := range fakesCh {
					if err := w.WriteRecord(fake); err != nil {
						b.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(bytesPerRecord, "B/rec")
		})
	}
}

// BenchmarkWriteRecords times only the writer, over a record set generated
// once up front - the number to compare across formats. The size sweep shows
// how each one scales: the buffered line formats should be close to linear,
// while parquet's per-file footer and row-group flush are a fixed cost that
// only amortises once the file is large.
func BenchmarkWriteRecords(b *testing.B) {
	for _, size := range benchSizes {
		for _, format := range benchFormats {
			b.Run(fmt.Sprintf("%s/%d", format, size), func(b *testing.B) {
				config := benchConfig(format, size)
				records, structArr := drain(config)
				// One path reused across ops: newRecordWriter truncates it on
				// every open, so the file never grows past a single op's
				// output no matter how high b.N climbs.
				path := filepath.Join(b.TempDir(), "out."+outputExt(format))
				bytesPerRecord := reportOutputSize(b, config, structArr, path, records)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					w, err := newRecordWriter(config, structArr, path)
					if err != nil {
						b.Fatal(err)
					}
					for _, rec := range records {
						if err := w.WriteRecord(rec); err != nil {
							b.Fatal(err)
						}
					}
					if err := w.Close(); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(bytesPerRecord, "B/rec")
			})
		}
	}
}
