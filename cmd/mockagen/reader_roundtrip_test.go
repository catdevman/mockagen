package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/mockagen"
)

func roundTripFields(names []string, spans [][2]int64) ([]reflect.StructField, reflect.Type) {
	fields := make([]reflect.StructField, len(names))
	for i, name := range names {
		tag := fmt.Sprintf("json:%q yaml:%q csv:%q parquet:%q sql:%q", name, name, name, name, name)
		if spans != nil {
			tag += fmt.Sprintf(" fixed:%q", fmt.Sprintf("%d,%d", spans[i][0], spans[i][1]))
		}
		fields[i] = reflect.StructField{
			Name: structFieldName(name, i),
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(tag),
		}
	}
	return fields, reflect.StructOf(fields)
}

func TestWriterReaderRoundTrip(t *testing.T) {
	names := []string{"name", "city"}
	// go-fixedwidth prefills each span with spaces and writes the value
	// over the left of it. The reader returns those spaces; stripping
	// them would drop a value that is only padding.
	cases := []struct {
		format string
		fixed  bool
		want   [][]string
	}{
		{format: "yaml", want: [][]string{{"Ann", "NY"}, {"Bo", ""}}},
		{format: "fluent", want: [][]string{{"Ann", "NY"}, {"Bo", ""}}},
		{format: "parquet", want: [][]string{{"Ann", "NY"}, {"Bo", ""}}},
		{format: "fixed", fixed: true, want: [][]string{{"Ann   ", "NY  "}, {"Bo    ", "    "}}},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			cols := []mockagen.MockagenColumn{{Name: "name"}, {Name: "city"}}
			var spans [][2]int64
			if tc.fixed {
				cols[0].StartPosition, cols[0].EndPosition = 1, 6
				cols[1].StartPosition, cols[1].EndPosition = 7, 10
				spans = [][2]int64{{1, 6}, {7, 10}}
			}
			structArr, typ := roundTripFields(names, spans)
			path := filepath.Join(t.TempDir(), "out."+tc.format)
			w, err := newRecordWriter(mockagen.MockagenConfig{
				FileFormat: tc.format,
				Name:       "people",
				Columns:    cols,
			}, structArr, path)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range [][2]string{{"Ann", "NY"}, {"Bo", ""}} {
				rec := reflect.New(typ).Elem()
				rec.Field(0).SetString(row[0])
				rec.Field(1).SetString(row[1])
				if err := w.WriteRecord(rec.Interface()); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			spec := importer.SourceSpec{Format: tc.format}
			if tc.fixed {
				spec.Fixed = []importer.FixedCol{
					{Name: "name", Start: 1, End: 6},
					{Name: "city", Start: 7, End: 10},
				}
			}
			src, err := importer.OpenSource(path, spec)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			var got [][]string
			var rec importer.Record
			for {
				ok, err := src.Next(&rec)
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				row := make([]string, len(rec.Values))
				for i := range rec.Values {
					if rec.Values[i].Kind == importer.KindText {
						row[i] = string(rec.Bytes(i))
					}
				}
				got = append(got, row)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %d, want %d", len(got), len(tc.want))
			}
			for i := range tc.want {
				for j := range tc.want[i] {
					if got[i][j] != tc.want[i][j] {
						t.Errorf("row %d col %d = %q, want %q", i, j, got[i][j], tc.want[i][j])
					}
				}
			}
		})
	}
}

func TestFileFormatsHaveAWriterAndAReader(t *testing.T) {
	for _, format := range importer.FileFormats() {
		t.Run(format, func(t *testing.T) {
			cols := []mockagen.MockagenColumn{{Name: "a"}}
			spans := [][2]int64{{1, 1}}
			if format == "fixed" {
				cols[0].StartPosition = 1
				cols[0].EndPosition = 1
			}
			structArr, typ := roundTripFields([]string{"a"}, spans)
			path := filepath.Join(t.TempDir(), "out."+format)
			w, err := newRecordWriter(mockagen.MockagenConfig{
				FileFormat: format,
				Name:       "people",
				Dialect:    "postgres",
				Columns:    cols,
			}, structArr, path)
			if err != nil {
				t.Fatalf("writer: %v", err)
			}
			rec := reflect.New(typ).Elem()
			rec.Field(0).SetString("a")
			if err := w.WriteRecord(rec.Interface()); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			spec := importer.SourceSpec{Format: format}
			if format == "fixed" {
				spec.Fixed = []importer.FixedCol{{Name: "a", Start: 1, End: 1}}
			}
			src, err := importer.OpenSource(path, spec)
			if format == "sql" {
				if !errors.Is(err, importer.ErrUnknownFormat) {
					t.Fatalf("sql open err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			var row importer.Record
			ok, err := src.Next(&row)
			if err != nil || !ok {
				t.Fatalf("next ok=%v err=%v", ok, err)
			}
			if string(row.Bytes(0)) != "a" {
				t.Fatalf("value = %q", row.Bytes(0))
			}
		})
	}

	t.Run("xml", func(t *testing.T) {
		structArr, _ := roundTripFields([]string{"a"}, nil)
		_, err := newRecordWriter(mockagen.MockagenConfig{FileFormat: "xml"}, structArr, filepath.Join(t.TempDir(), "out.xml"))
		if err == nil || !strings.Contains(err.Error(), `unsupported file_format "xml"`) {
			t.Fatalf("writer err = %v", err)
		}
		path := filepath.Join(t.TempDir(), "x.xml")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = importer.OpenSource(path, importer.SourceSpec{Format: "xml"})
		if !errors.Is(err, importer.ErrUnknownFormat) {
			t.Fatalf("open err = %v", err)
		}
	})
}
