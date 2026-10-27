package importer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/parquet-go/parquet-go"
)

type gotCell struct {
	kind importer.Kind
	text string
	n    int64
}

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func collectRows(t *testing.T, path string, spec importer.SourceSpec) ([][]gotCell, error) {
	t.Helper()
	src, err := importer.OpenSource(path, spec)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	var rows [][]gotCell
	var rec importer.Record
	for {
		ok, err := src.Next(&rec)
		if err != nil {
			return rows, err
		}
		if !ok {
			return rows, nil
		}
		row := make([]gotCell, len(rec.Values))
		for i := range rec.Values {
			c := gotCell{kind: rec.Values[i].Kind, n: rec.Int(i)}
			if c.kind == importer.KindText || c.kind == importer.KindRaw {
				c.text = string(rec.Bytes(i))
			}
			row[i] = c
		}
		rows = append(rows, row)
	}
}

func textCell(text string) gotCell { return gotCell{kind: importer.KindText, text: text} }

func nullCell() gotCell { return gotCell{kind: importer.KindNull} }

func TestYAMLSource(t *testing.T) {
	// 01 is unquoted on purpose. yaml.v3 would resolve it to the
	// integer 1 if we decoded into any; Node.Value keeps the spelling.
	seq := "" +
		"- email: ada\n" +
		"  note: null\n" +
		"  code: 01\n" +
		"  blank: \"\"\n" +
		"- email: bea\n" +
		"  note: hi\n" +
		"  code: \"02\"\n"
	want := [][]gotCell{
		{textCell("ada"), nullCell(), textCell("01"), textCell("")},
		{textCell("bea"), textCell("hi"), textCell("02"), nullCell()},
	}
	path := writeTemp(t, "rows.yaml", seq)
	rows, err := collectRows(t, path, importer.SourceSpec{Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for i := range want {
		if len(rows[i]) != len(want[i]) {
			t.Fatalf("row %d len %d, want %d", i, len(rows[i]), len(want[i]))
		}
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Errorf("row %d col %d = %+v, want %+v", i, j, rows[i][j], want[i][j])
			}
		}
	}

	stream := "email: ada\ncode: 01\n---\nemail: bea\ncode: \"02\"\n"
	path = writeTemp(t, "stream.yaml", stream)
	rows, err = collectRows(t, path, importer.SourceSpec{Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	streamWant := [][]gotCell{
		{textCell("ada"), textCell("01")},
		{textCell("bea"), textCell("02")},
	}
	if len(rows) != len(streamWant) {
		t.Fatalf("stream rows = %d", len(rows))
	}
	for i := range streamWant {
		for j := range streamWant[i] {
			if rows[i][j] != streamWant[i][j] {
				t.Errorf("stream row %d col %d = %+v, want %+v", i, j, rows[i][j], streamWant[i][j])
			}
		}
	}

	extra := "- email: ada\n- email: bea\n  extra: 1\n"
	path = writeTemp(t, "extra.yaml", extra)
	if _, err := collectRows(t, path, importer.SourceSpec{Format: "yaml"}); err == nil || !strings.Contains(err.Error(), `unexpected key "extra"`) {
		t.Fatalf("extra key err = %v", err)
	}

	nested := "- email: ada\n  note:\n    nested: 1\n"
	path = writeTemp(t, "nested.yaml", nested)
	if _, err := collectRows(t, path, importer.SourceSpec{Format: "yaml"}); err == nil || !strings.Contains(err.Error(), `key "note"`) {
		t.Fatalf("non-scalar err = %v", err)
	}
}

func TestFluentSource(t *testing.T) {
	good := "2020-01-02T03:04:05Z\tapp\t{\"email\":\"ada\",\"note\":null}\n" +
		"2020-01-02T03:04:06Z\tapp\t{\"email\":\"bea\",\"note\":\"\"}\n"
	path := writeTemp(t, "ok.log", good)
	rows, err := collectRows(t, path, importer.SourceSpec{Format: "fluent"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]gotCell{
		{textCell("ada"), nullCell()},
		{textCell("bea"), textCell("")},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d", len(rows))
	}
	for i := range want {
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Errorf("row %d col %d = %+v, want %+v", i, j, rows[i][j], want[i][j])
			}
		}
	}

	bad := good + "not-a-fluent-line\n"
	path = writeTemp(t, "bad.log", bad)
	if _, err := collectRows(t, path, importer.SourceSpec{Format: "fluent"}); err == nil || !strings.Contains(err.Error(), "fluent") {
		t.Fatalf("bad line err = %v", err)
	}

	obj := good + "2020-01-02T03:04:07Z\tapp\t[]\n"
	path = writeTemp(t, "array.log", obj)
	if _, err := collectRows(t, path, importer.SourceSpec{Format: "fluent"}); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("array err = %v", err)
	}
}

func TestFixedSource(t *testing.T) {
	spec := importer.SourceSpec{
		Format: "fixed",
		Fixed: []importer.FixedCol{
			{Name: "name", Start: 1, End: 6},
			{Name: "city", Start: 7, End: 10},
		},
	}
	// The spaces are the pad. The reader must return them.
	path := writeTemp(t, "ok.fixed", "Ann   NY  \nBo        \r\n")
	rows, err := collectRows(t, path, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]gotCell{
		{textCell("Ann   "), textCell("NY  ")},
		{textCell("Bo    "), textCell("    ")},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d", len(rows))
	}
	for i := range want {
		for j := range want[i] {
			if rows[i][j] != want[i][j] {
				t.Errorf("row %d col %d = %+v, want %+v", i, j, rows[i][j], want[i][j])
			}
		}
	}

	path = writeTemp(t, "short.fixed", "Ann\n")
	_, err = collectRows(t, path, spec)
	if err == nil || !strings.Contains(err.Error(), "length 3") || !strings.Contains(err.Error(), "width 10") {
		t.Fatalf("short err = %v", err)
	}

	path = writeTemp(t, "empty.fixed", "\n")
	_, err = collectRows(t, path, spec)
	if err == nil || !strings.Contains(err.Error(), "length 0") || !strings.Contains(err.Error(), "width 10") {
		t.Fatalf("empty err = %v", err)
	}

	overlap := spec
	overlap.Fixed = []importer.FixedCol{
		{Name: "a", Start: 1, End: 5},
		{Name: "b", Start: 4, End: 8},
	}
	path = writeTemp(t, "over.fixed", "abcdefgh\n")
	if _, err := collectRows(t, path, overlap); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap err = %v", err)
	}

	mismatch := spec
	mismatch.Fields = []string{"city", "name"}
	path = writeTemp(t, "order.fixed", "Ann   NY  \n")
	if _, err := collectRows(t, path, mismatch); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("fields err = %v", err)
	}

	path = writeTemp(t, "none.fixed", "Ann   NY  \n")
	if _, err := collectRows(t, path, importer.SourceSpec{Format: "fixed"}); err == nil || !strings.Contains(err.Error(), "source.fixed") {
		t.Fatalf("empty layout err = %v", err)
	}

	// A gap is not an overlap. Columns 1-3 and 5-6 leave byte 4 unread.
	gapped := importer.SourceSpec{
		Format: "fixed",
		Fixed: []importer.FixedCol{
			{Name: "name", Start: 1, End: 3},
			{Name: "city", Start: 5, End: 6},
		},
	}
	path = writeTemp(t, "gap.fixed", "Ann XY\n")
	rows, err = collectRows(t, path, gapped)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].text != "Ann" || rows[0][1].text != "XY" {
		t.Fatalf("gap row = %+v", rows[0])
	}
}

func writeParquet(t *testing.T, path string, sample any, rows []any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := parquet.NewGenericWriter[any](f, parquet.SchemaOf(sample))
	if _, err := w.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func compilePlan(t *testing.T, fields []string, cols []schema.Column, mapped []importer.Field) *importer.Plan {
	t.Helper()
	sch := schema.Schema{
		Name:    "t",
		Source:  schema.Source{Dialect: "postgres", Table: "t"},
		Columns: cols,
	}
	p, diags := importer.Compile(sch, importer.FieldMap{
		Kind: "FieldMap",
		Source: importer.SourceSpec{
			HasHeader: true,
			Fields:    fields,
		},
		Fields:   mapped,
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
	})
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	return p
}

func TestParquetTypedValues(t *testing.T) {
	type row struct {
		N int64  `parquet:"n"`
		S string `parquet:"s"`
		B bool   `parquet:"b"`
		Z *int64 `parquet:"z,optional"`
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "typed.parquet")
	writeParquet(t, path, row{}, []any{row{N: 3, S: "  3 ", B: true}})
	fields := []string{"n", "s", "b", "z"}
	spec := importer.SourceSpec{Format: "parquet"}

	t.Run("int copies and sql does not quote", func(t *testing.T) {
		p := compilePlan(t, fields,
			[]schema.Column{{Name: "n", SQLType: "integer"}},
			[]importer.Field{{Target: "n", Source: "n"}},
		)
		src, err := importer.OpenSource(path, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var sink importer.RecordSink
		if _, err := importer.Run(context.Background(), src, p, &sink, importer.Options{}); err != nil {
			t.Fatal(err)
		}
		if len(sink.Rows) != 1 || sink.Rows[0].Values[0].Kind != importer.KindInt || sink.Rows[0].Int(0) != 3 {
			t.Fatalf("record = %+v", sink.Rows)
		}

		src2, err := importer.OpenSource(path, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer src2.Close()
		var buf bytes.Buffer
		if _, err := importer.Run(context.Background(), src2, p, importer.NewSQLSink(&buf), importer.Options{}); err != nil {
			t.Fatal(err)
		}
		const want = "INSERT INTO \"t\" (\"n\") VALUES\n\t(3);\n"
		if buf.String() != want {
			t.Fatalf("sql =\n%s\nwant:\n%s", buf.String(), want)
		}
		if strings.Contains(buf.String(), "'3'") {
			t.Fatalf("integer was quoted:\n%s", buf.String())
		}
	})

	t.Run("trim on a typed int fails", func(t *testing.T) {
		p := compilePlan(t, fields,
			[]schema.Column{{Name: "n", SQLType: "integer"}},
			[]importer.Field{{
				Target:     "n",
				Source:     "n",
				Transforms: []importer.Transform{{Name: "trim"}},
			}},
		)
		src, err := importer.OpenSource(path, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var sink importer.RecordSink
		_, err = importer.Run(context.Background(), src, p, &sink, importer.Options{})
		if err == nil || !strings.Contains(err.Error(), "transforms do not apply to a typed value") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("bool is not stringified into an integer", func(t *testing.T) {
		p := compilePlan(t, fields,
			[]schema.Column{{Name: "n", SQLType: "integer"}},
			[]importer.Field{{Target: "n", Source: "b"}},
		)
		src, err := importer.OpenSource(path, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var sink importer.RecordSink
		_, err = importer.Run(context.Background(), src, p, &sink, importer.Options{})
		if err == nil || !strings.Contains(err.Error(), "bool") || !strings.Contains(err.Error(), "int") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "true") {
			t.Fatalf("bool was stringified: %v", err)
		}
	})

	t.Run("text trim still coerces", func(t *testing.T) {
		p := compilePlan(t, fields,
			[]schema.Column{{Name: "n", SQLType: "integer"}},
			[]importer.Field{{
				Target:     "n",
				Source:     "s",
				Transforms: []importer.Transform{{Name: "trim"}},
			}},
		)
		src, err := importer.OpenSource(path, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var sink importer.RecordSink
		if _, err := importer.Run(context.Background(), src, p, &sink, importer.Options{}); err != nil {
			t.Fatal(err)
		}
		if sink.Rows[0].Values[0].Kind != importer.KindInt || sink.Rows[0].Int(0) != 3 {
			t.Fatalf("coerced = %+v int %d", sink.Rows[0].Values[0], sink.Rows[0].Int(0))
		}
	})

	t.Run("null stays null", func(t *testing.T) {
		p := compilePlan(t, fields,
			[]schema.Column{{Name: "z", SQLType: "integer", Nullable: true}},
			[]importer.Field{{Target: "z", Source: "z"}},
		)
		src, err := importer.OpenSource(path, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		var sink importer.RecordSink
		if _, err := importer.Run(context.Background(), src, p, &sink, importer.Options{}); err != nil {
			t.Fatal(err)
		}
		if sink.Rows[0].Values[0].Kind != importer.KindNull {
			t.Fatalf("null kind = %d", sink.Rows[0].Values[0].Kind)
		}
	})
}

func TestParquetLogicalTypes(t *testing.T) {
	day := int32(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC).Unix() / 86400)
	type dated struct {
		Day int32 `parquet:"day,date"`
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "day.parquet")
	writeParquet(t, path, dated{}, []any{dated{Day: day}})
	rows, err := collectRows(t, path, importer.SourceSpec{Format: "parquet"})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].kind != importer.KindText || rows[0][0].text != "2020-01-02" {
		t.Fatalf("date = %+v", rows[0][0])
	}

	nanos := time.Date(2020, 1, 2, 3, 4, 5, 6, time.UTC).UnixNano()
	type stamped struct {
		Ts int64 `parquet:"ts,timestamp(nanosecond)"`
	}
	path = filepath.Join(dir, "ts.parquet")
	writeParquet(t, path, stamped{}, []any{stamped{Ts: nanos}})
	rows, err = collectRows(t, path, importer.SourceSpec{Format: "parquet"})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].kind != importer.KindTime || rows[0][0].n != nanos {
		t.Fatalf("time = %+v", rows[0][0])
	}

	type money struct {
		D int64 `parquet:"d,decimal(2:10)"`
	}
	path = filepath.Join(dir, "dec.parquet")
	writeParquet(t, path, money{}, []any{money{D: 123}, money{D: 100}})
	rows, err = collectRows(t, path, importer.SourceSpec{Format: "parquet"})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0].text != "1.23" || rows[1][0].text != "1.00" {
		t.Fatalf("decimal = %q %q", rows[0][0].text, rows[1][0].text)
	}

	type grouped struct {
		G struct {
			X int64 `parquet:"x"`
		} `parquet:"g"`
	}
	path = filepath.Join(dir, "group.parquet")
	writeParquet(t, path, grouped{}, []any{grouped{}})
	if _, err := collectRows(t, path, importer.SourceSpec{Format: "parquet"}); err == nil || !strings.Contains(err.Error(), `column "g"`) {
		t.Fatalf("group err = %v", err)
	}
}

func TestKindTimeSinkQuotesInstant(t *testing.T) {
	instant := time.Date(2020, 1, 2, 3, 4, 5, 6, time.UTC)
	var rec importer.Record
	rec.Reset(1)
	rec.SetTime(0, instant)
	const want = "2020-01-02T03:04:05.000000006Z"
	target := importer.Target{
		Name:    "t",
		Table:   "t",
		Dialect: "postgres",
		Columns: []importer.Column{{Name: "ts", Kind: importer.KindTime}},
	}
	ctx := context.Background()

	t.Run("sql", func(t *testing.T) {
		var buf bytes.Buffer
		sink := importer.NewSQLSink(&buf)
		if err := sink.Open(ctx, target); err != nil {
			t.Fatal(err)
		}
		if err := sink.Write(ctx, &rec); err != nil {
			t.Fatal(err)
		}
		if err := sink.Close(ctx); err != nil {
			t.Fatal(err)
		}
		got := "INSERT INTO \"t\" (\"ts\") VALUES\n\t('" + want + "');\n"
		if buf.String() != got {
			t.Fatalf("sql =\n%s\nwant:\n%s", buf.String(), got)
		}
	})

	t.Run("csv", func(t *testing.T) {
		var buf bytes.Buffer
		sink := importer.NewCSVSink(&buf, false)
		if err := sink.Open(ctx, target); err != nil {
			t.Fatal(err)
		}
		if err := sink.Write(ctx, &rec); err != nil {
			t.Fatal(err)
		}
		got := "\"" + want + "\"\n"
		if buf.String() != got {
			t.Fatalf("csv = %q, want %q", buf.String(), got)
		}
	})

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		sink := importer.NewJSONSink(&buf)
		if err := sink.Open(ctx, target); err != nil {
			t.Fatal(err)
		}
		if err := sink.Write(ctx, &rec); err != nil {
			t.Fatal(err)
		}
		got := "{\"ts\":\"" + want + "\"}\n"
		if buf.String() != got {
			t.Fatalf("json = %q, want %q", buf.String(), got)
		}
	})
}

func TestReadSampleSpec(t *testing.T) {
	body := "- email: ada\n  note: null\n- email: bea\n  note: \"\"\n"
	path := writeTemp(t, "rows.yaml", body)
	got, err := importer.ReadSample(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "yaml" || !got.HasHeader {
		t.Fatalf("sample = %+v", got)
	}
	wantFields := []string{"email", "note"}
	if strings.Join(got.Fields, ",") != strings.Join(wantFields, ",") {
		t.Fatalf("fields = %q", got.Fields)
	}
	if got.Preview[0][0] != "ada" || got.Preview[0][1] != "" || got.Preview[1][1] != "" {
		t.Fatalf("preview = %#v", got.Preview)
	}

	// Format on the spec wins over a csv extension, so a fixed file
	// that was named .csv is not sniffed into one column.
	fixedPath := writeTemp(t, "named.csv", "Ann   NY  \n")
	spec := &importer.SourceSpec{
		Format:    "fixed",
		HasHeader: false,
		Fixed: []importer.FixedCol{
			{Name: "name", Start: 1, End: 6},
			{Name: "city", Start: 7, End: 10},
		},
	}
	got, err = importer.ReadSampleSpec(fixedPath, spec, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "fixed" || got.HasHeader || got.Preview[0][0] != "Ann   " {
		t.Fatalf("fixed sample = %+v preview %#v", got, got.Preview)
	}

	bare := writeTemp(t, "bare.fixed", "Ann   NY  \n")
	if _, err := importer.ReadSample(bare, 1); err == nil || !strings.Contains(err.Error(), "source.fixed") {
		t.Fatalf("bare fixed err = %v", err)
	}
}

func TestOpenSourceRejectsWriteOnlyAndUnknown(t *testing.T) {
	path := writeTemp(t, "x.sql", "INSERT INTO t VALUES (1);\n")
	for _, format := range []string{"sql", "xml"} {
		_, err := importer.OpenSource(path, importer.SourceSpec{Format: format})
		if !errors.Is(err, importer.ErrUnknownFormat) {
			t.Errorf("%s err = %v", format, err)
		}
	}
}
