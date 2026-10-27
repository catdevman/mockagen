package importer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

// usersSchema is the table the sink tests load. id is identity, so a
// clean plan omits it and the INSERT must not name it. note and
// login_count both have database defaults; a mapped null is still an
// explicit NULL, which is what overrides that default.
func usersSchema() schema.Schema {
	return schema.Schema{
		Name: "users",
		Source: schema.Source{
			Dialect: "postgres",
			Table:   "users",
		},
		Columns: []schema.Column{
			{Name: "id", SQLType: "integer", Identity: true},
			{Name: "email", SQLType: "text"},
			{Name: "note", SQLType: "text", Nullable: true, Default: "''"},
			{Name: "login_count", SQLType: "integer", Nullable: true, Default: "0"},
		},
	}
}

func usersMap() importer.FieldMap {
	return importer.FieldMap{
		Version: "mockagen/v1",
		Kind:    "FieldMap",
		Name:    "users-import",
		Source: importer.SourceSpec{
			Format:    "csv",
			HasHeader: true,
			Fields:    []string{"email", "note", "login_count"},
		},
		Fields: []importer.Field{
			{Target: "email", Source: "email"},
			{Target: "note", Source: "note"},
			{Target: "login_count", Source: "login_count"},
		},
		Omitted:  []string{"id"},
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
		OnError:  "abort",
	}
}

func usersPlan(t *testing.T) *importer.Plan {
	t.Helper()
	p, diags := importer.Compile(usersSchema(), usersMap())
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	return p
}

func sp(s string) *string { return &s }

func usersRows() [][]*string {
	return [][]*string{
		{sp("ada@example.com"), sp("hello"), sp("3")},
		// Empty note is a present empty string. A nil count is NULL,
		// including though the column has a database default.
		{sp("bea@example.com"), sp(""), nil},
	}
}

func TestNullAndEmptyOnEverySink(t *testing.T) {
	p := usersPlan(t)
	ctx := context.Background()
	src := func() *importer.SliceSource {
		return importer.NewSliceSource([]string{"email", "note", "login_count"}, usersRows())
	}

	t.Run("record", func(t *testing.T) {
		var sink importer.RecordSink
		rep, err := importer.Run(ctx, src(), p, &sink, importer.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Read != 2 || rep.Written != 2 || len(sink.Rows) != 2 {
			t.Fatalf("report %+v rows %d", rep, len(sink.Rows))
		}
		names := make([]string, len(p.Target.Columns))
		for i, c := range p.Target.Columns {
			names[i] = c.Name
		}
		if strings.Join(names, ",") != "email,note,login_count" {
			t.Fatalf("columns = %q", names)
		}
		ada, bea := sink.Rows[0], sink.Rows[1]
		if ada.Values[0].Kind != importer.KindText || string(ada.Bytes(0)) != "ada@example.com" {
			t.Fatalf("email = %q kind %d", ada.Bytes(0), ada.Values[0].Kind)
		}
		if ada.Values[2].Kind != importer.KindInt || ada.Int(2) != 3 {
			t.Fatalf("count kind %d int %d", ada.Values[2].Kind, ada.Int(2))
		}
		if bea.Values[1].Kind != importer.KindText || len(bea.Bytes(1)) != 0 {
			t.Fatalf("empty note kind %d bytes %q", bea.Values[1].Kind, bea.Bytes(1))
		}
		if bea.Values[2].Kind != importer.KindNull {
			t.Fatalf("null count kind %d", bea.Values[2].Kind)
		}
	})

	const wantSQL = "INSERT INTO \"users\" (\"email\", \"note\", \"login_count\") VALUES\n" +
		"\t('ada@example.com', 'hello', 3),\n" +
		"\t('bea@example.com', '', NULL);\n"

	t.Run("sql", func(t *testing.T) {
		var buf bytes.Buffer
		rep, err := importer.Run(ctx, src(), p, importer.NewSQLSink(&buf), importer.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Written != 2 {
			t.Fatalf("wrote %d", rep.Written)
		}
		if buf.String() != wantSQL {
			t.Fatalf("sql =\n%s\nwant:\n%s", buf.String(), wantSQL)
		}
		if strings.Contains(buf.String(), "\"id\"") {
			t.Fatalf("identity column was written:\n%s", buf.String())
		}
	})

	const wantCSV = "\"ada@example.com\",\"hello\",\"3\"\n" +
		"\"bea@example.com\",\"\",\n"

	t.Run("csv", func(t *testing.T) {
		var buf bytes.Buffer
		if _, err := importer.Run(ctx, src(), p, importer.NewCSVSink(&buf, false), importer.Options{}); err != nil {
			t.Fatal(err)
		}
		if buf.String() != wantCSV {
			t.Fatalf("csv =\n%s\nwant:\n%s", buf.String(), wantCSV)
		}
	})

	const wantJSON = "{\"email\":\"ada@example.com\",\"note\":\"hello\",\"login_count\":3}\n" +
		"{\"email\":\"bea@example.com\",\"note\":\"\",\"login_count\":null}\n"

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		if _, err := importer.Run(ctx, src(), p, importer.NewJSONSink(&buf), importer.Options{}); err != nil {
			t.Fatal(err)
		}
		if buf.String() != wantJSON {
			t.Fatalf("json =\n%s\nwant:\n%s", buf.String(), wantJSON)
		}
	})

	t.Run("bulk", func(t *testing.T) {
		var csvBuf, script bytes.Buffer
		sink := importer.NewBulkSink(&csvBuf, &script, "users.csv", false)
		if _, err := importer.Run(ctx, src(), p, sink, importer.Options{}); err != nil {
			t.Fatal(err)
		}
		if csvBuf.String() != wantCSV {
			t.Fatalf("bulk csv =\n%s", csvBuf.String())
		}
		got := script.String()
		if !strings.Contains(got, "\"email\", \"note\", \"login_count\"") || strings.Contains(got, "\"id\"") {
			t.Fatalf("script =\n%s", got)
		}
	})

	t.Run("discard", func(t *testing.T) {
		rep, err := importer.Run(ctx, src(), p, importer.DiscardSink{}, importer.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Read != 2 || rep.Written != 2 {
			t.Fatalf("report %+v", rep)
		}
	})
}

func TestBulkSinkRejectsSQLiteQualifiedTable(t *testing.T) {
	sch := usersSchema()
	sch.Source.Dialect = "sqlite"
	sch.Source.Table = "main.users"
	p, diags := importer.Compile(sch, usersMap())
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	var csvBuf, script bytes.Buffer
	_, err := importer.Run(context.Background(),
		importer.NewSliceSource([]string{"email", "note", "login_count"}, usersRows()),
		p, importer.NewBulkSink(&csvBuf, &script, "users.csv", false), importer.Options{})
	if err == nil || !strings.Contains(err.Error(), "sqlite's .import would create a table of that literal name") {
		t.Fatalf("err = %v", err)
	}
	if csvBuf.Len() != 0 || script.Len() != 0 {
		t.Fatalf("wrote csv %d script %d", csvBuf.Len(), script.Len())
	}
}

func TestOpenSourceNullAndHeader(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "legacy.csv")
	body := "email,note,login_count\nada@example.com,hello,3\nbea@example.com,\n"
	if err := os.WriteFile(csvPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := usersMap().Source
	src, err := importer.OpenSource(csvPath, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if err := importer.MatchHeader(spec, src); err != nil {
		t.Fatal(err)
	}
	var rec importer.Record
	ok, err := src.Next(&rec)
	if err != nil || !ok {
		t.Fatalf("first ok %v err %v", ok, err)
	}
	if rec.Line != 2 || string(rec.Bytes(0)) != "ada@example.com" {
		t.Fatalf("line %d email %q", rec.Line, rec.Bytes(0))
	}
	ok, err = src.Next(&rec)
	if err != nil || !ok {
		t.Fatalf("second ok %v err %v", ok, err)
	}
	// A short row leaves the missing integer NULL. The empty note is
	// a present field, so it stays an empty string.
	if rec.Line != 3 || rec.Values[1].Kind != importer.KindText || len(rec.Bytes(1)) != 0 {
		t.Fatalf("note kind %d bytes %q line %d", rec.Values[1].Kind, rec.Bytes(1), rec.Line)
	}
	if rec.Values[2].Kind != importer.KindNull {
		t.Fatalf("count kind %d", rec.Values[2].Kind)
	}

	swapped := spec
	swapped.Fields = []string{"note", "email", "login_count"}
	src2, err := importer.OpenSource(csvPath, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer src2.Close()
	err = importer.MatchHeader(swapped, src2)
	if !errors.Is(err, importer.ErrHeaderMismatch) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), `"email"`) || !strings.Contains(err.Error(), `"note"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestJSONLSourceNullAndMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.jsonl")
	body := "{\"email\":\"ada\",\"note\":null,\"n\":3}\n" +
		"{\"email\":\"bea\",\"note\":\"\"}\n" +
		"{\"n\":4,\"email\":\"cy\",\"extra\":true}\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := importer.OpenSource(path, importer.SourceSpec{Format: "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if strings.Join(src.Fields(), ",") != "email,note,n" {
		t.Fatalf("fields = %#v", src.Fields())
	}
	// Next reuses the record, so each row is snapshotted before the
	// next read. Bytes aliases the arena.
	type snap struct {
		kinds []importer.Kind
		text  []string
	}
	var rows []snap
	for {
		var rec importer.Record
		ok, err := src.Next(&rec)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		s := snap{kinds: make([]importer.Kind, len(rec.Values)), text: make([]string, len(rec.Values))}
		for i := range rec.Values {
			s.kinds[i] = rec.Values[i].Kind
			if rec.Values[i].Kind == importer.KindText || rec.Values[i].Kind == importer.KindRaw {
				s.text[i] = string(rec.Bytes(i))
			}
		}
		rows = append(rows, s)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].kinds[1] != importer.KindNull || rows[0].text[2] != "3" {
		t.Fatalf("row0 note kind %d n %q", rows[0].kinds[1], rows[0].text[2])
	}
	if rows[1].kinds[1] != importer.KindText || rows[1].text[1] != "" || rows[1].kinds[2] != importer.KindNull {
		t.Fatalf("row1 note %q kind %d n kind %d", rows[1].text[1], rows[1].kinds[1], rows[1].kinds[2])
	}
	if rows[2].text[0] != "cy" || rows[2].kinds[1] != importer.KindNull || rows[2].text[2] != "4" {
		t.Fatalf("row2 email %q note kind %d n %q", rows[2].text[0], rows[2].kinds[1], rows[2].text[2])
	}
}

// steadyPlan is the benchmark path: trim on text, then an integer.
// Regex, time parsing, and object assembly allocate, so they are not
// this plan. The arena exists so this one does not.
func steadyPlan(t testing.TB) *importer.Plan {
	t.Helper()
	sch := schema.Schema{
		Name: "users",
		Source: schema.Source{
			Dialect: "postgres",
			Table:   "users",
		},
		Columns: []schema.Column{
			{Name: "email", SQLType: "text"},
			{Name: "n", SQLType: "integer"},
		},
	}
	fm := importer.FieldMap{
		Kind: "FieldMap",
		Source: importer.SourceSpec{
			HasHeader: true,
			Fields:    []string{"email", "n"},
		},
		Fields: []importer.Field{
			{Target: "email", Source: "email", Transforms: []importer.Transform{{Name: "trim"}}},
			{Target: "n", Source: "n"},
		},
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
	}
	p, diags := importer.Compile(sch, fm)
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	return p
}

func steadySource() *importer.Record {
	var src importer.Record
	src.Reset(2)
	src.SetText(0, []byte("  ada@example.com  "))
	src.SetText(1, []byte("42"))
	return &src
}

func TestSteadyStateAllocs(t *testing.T) {
	p := steadyPlan(t)
	src := steadySource()
	var dst importer.Record
	if n := testing.AllocsPerRun(100, func() {
		if err := p.Apply(src, &dst); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("Apply allocs/op = %v, want 0", n)
	}

	ctx := context.Background()
	var row importer.Record
	if err := p.Apply(src, &row); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		open func(w *countingWriter) importer.Sink
	}{
		{"sql", func(w *countingWriter) importer.Sink { return importer.NewSQLSink(w) }},
		{"csv", func(w *countingWriter) importer.Sink { return importer.NewCSVSink(w, false) }},
		{"json", func(w *countingWriter) importer.Sink { return importer.NewJSONSink(w) }},
		{"bulk", func(w *countingWriter) importer.Sink {
			return importer.NewBulkSink(w, &countingWriter{}, "users.csv", false)
		}},
		{"discard", func(*countingWriter) importer.Sink { return importer.DiscardSink{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &countingWriter{}
			sink := tc.open(w)
			if err := sink.Open(ctx, p.Target); err != nil {
				t.Fatal(err)
			}
			n := testing.AllocsPerRun(100, func() {
				if err := sink.Write(ctx, &row); err != nil {
					t.Fatal(err)
				}
			})
			if err := sink.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("allocs/op = %v, want 0", n)
			}
		})
	}
}

// countingWriter records that bytes arrived. io.Discard would too, and
// neither allocates on Write; this one can be told apart in a failure.
type countingWriter struct{ n int }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
}

func BenchmarkPlanApply(b *testing.B) {
	p := steadyPlan(b)
	src := steadySource()
	var dst importer.Record
	for i := 0; i < 8; i++ {
		if err := p.Apply(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := p.Apply(src, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLSink(b *testing.B) {
	benchSink(b, func(w *countingWriter) importer.Sink { return importer.NewSQLSink(w) })
}
func BenchmarkCSVSink(b *testing.B) {
	benchSink(b, func(w *countingWriter) importer.Sink { return importer.NewCSVSink(w, false) })
}
func BenchmarkJSONSink(b *testing.B) {
	benchSink(b, func(w *countingWriter) importer.Sink { return importer.NewJSONSink(w) })
}
func BenchmarkDiscardSink(b *testing.B) {
	benchSink(b, func(*countingWriter) importer.Sink { return importer.DiscardSink{} })
}
func BenchmarkBulkSink(b *testing.B) {
	benchSink(b, func(w *countingWriter) importer.Sink {
		return importer.NewBulkSink(w, &countingWriter{}, "users.csv", false)
	})
}

func benchSink(b *testing.B, open func(w *countingWriter) importer.Sink) {
	b.Helper()
	p := steadyPlan(b)
	src := steadySource()
	var row importer.Record
	if err := p.Apply(src, &row); err != nil {
		b.Fatal(err)
	}
	w := &countingWriter{}
	sink := open(w)
	ctx := context.Background()
	if err := sink.Open(ctx, p.Target); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := sink.Write(ctx, &row); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sink.Write(ctx, &row); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := sink.Close(ctx); err != nil {
		b.Fatal(err)
	}
}
