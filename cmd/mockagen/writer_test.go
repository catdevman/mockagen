package main

import (
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
	yaml "gopkg.in/yaml.v3"
)

func TestFluentWriterLineShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newFluentWriter(f, "mockagen.test")
	type rec struct {
		User string `json:"user"`
		IP   string `json:"ip"`
	}
	for _, r := range []rec{{"alice", "10.0.0.1"}, {"bob", "10.0.0.2"}} {
		if err := w.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), b)
	}
	for i, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("line %d: got %d tab-separated fields, want 3: %q", i, len(parts), line)
		}
		if _, err := time.Parse(time.RFC3339, parts[0]); err != nil {
			t.Errorf("line %d: timestamp %q: %v", i, parts[0], err)
		}
		if parts[1] != "mockagen.test" {
			t.Errorf("line %d: tag = %q, want %q", i, parts[1], "mockagen.test")
		}
		var got rec
		if err := json.Unmarshal([]byte(parts[2]), &got); err != nil {
			t.Errorf("line %d: record %q: %v", i, parts[2], err)
		}
	}
}

func TestFluentTag(t *testing.T) {
	cases := []struct {
		name   string
		config mockagen.MockagenConfig
		want   string
	}{
		{"explicit tag wins", mockagen.MockagenConfig{Name: "web logs", Tag: "app.access"}, "app.access"},
		{"blank tag falls back to name", mockagen.MockagenConfig{Name: "web logs", Tag: "  "}, "mockagen.web.logs"},
		{"derived from name", mockagen.MockagenConfig{Name: "Web Logs"}, "mockagen.web.logs"},
		{"no name at all", mockagen.MockagenConfig{}, "mockagen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fluentTag(tc.config); got != tc.want {
				t.Errorf("fluentTag() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutputExt(t *testing.T) {
	for format, want := range map[string]string{
		"json":    "json",
		"yaml":    "yaml",
		"fixed":   "fixed",
		"parquet": "parquet",
		"fluent":  "log",
		"sql":     "sql",
		"csv":     "csv",
	} {
		if got := outputExt(format); got != want {
			t.Errorf("outputExt(%q) = %q, want %q", format, got, want)
		}
	}
}

// yamlRecordType builds the same kind of record the generator does: a
// runtime struct whose fields are all strings, tagged with the column name.
func yamlRecordType(cols []string) ([]reflect.StructField, reflect.Type) {
	fields := make([]reflect.StructField, 0, len(cols))
	for i, name := range cols {
		fields = append(fields, reflect.StructField{
			Name: structFieldName(name, i),
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(fmt.Sprintf("yaml:%q", name)),
		})
	}
	return fields, reflect.StructOf(fields)
}

// TestYAMLSeqWriterRoundTrip is the safety net for the hand-rolled emitter:
// whatever it writes must parse back as exactly the strings that went in,
// including values YAML would otherwise resolve to a bool, a number or null.
func TestYAMLSeqWriterRoundTrip(t *testing.T) {
	cols := []string{"plain", "tricky", "text"}
	structArr, recType := yamlRecordType(cols)

	tricky := []string{
		"", "true", "False", "NULL", "~", "y", "no", "on", "off",
		"123", "0x1f", "1e5", "-7", "0.5", "1:30", "2019-12-24",
		" leading", "trailing ", "  ", "a: b", "x #c", "#comment",
		"- dash", "*alias", "&anchor", "!tag", "[bracket", "{brace",
		"|pipe", ">gt", "%pct", "@at", "`tick", "'single'", `d"quote`,
		"back\\slash", "multi\nline", "tab\there", "ret\rurn",
		"日本語", "emoji 🎉", "nul\x00byte", "colon:", ":", "--",
	}

	path := filepath.Join(t.TempDir(), "out.yaml")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newYAMLSeqWriter(f, structArr)
	if w.keys == nil {
		t.Fatal("expected the fast path to be selected for an all-string struct")
	}
	want := make([]map[string]string, 0, len(tricky))
	for _, v := range tricky {
		rec := reflect.New(recType).Elem()
		rec.Field(0).SetString("plainvalue")
		rec.Field(1).SetString(v)
		rec.Field(2).SetString("Cum dolorem sed aliquid fugit aut, quo " +
			"voluptas nulla eveniet assumenda longer than eighty columns.")
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatalf("value %q: %v", v, err)
		}
		want = append(want, map[string]string{
			"plain":  rec.Field(0).String(),
			"tricky": v,
			"text":   rec.Field(2).String(),
		})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not valid YAML mapping to strings: %v\n%s", err, b)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		for _, k := range cols {
			if got[i][k] != want[i][k] {
				t.Errorf("record %d key %q: got %q, want %q", i, k, got[i][k], want[i][k])
			}
		}
	}
}

// TestYAMLSeqWriterMatchesMarshal pins the fast path to the general one: both
// must describe the same data, even where they format it differently.
func TestYAMLSeqWriterMatchesMarshal(t *testing.T) {
	cols := []string{"id", "level", "message"}
	structArr, recType := yamlRecordType(cols)
	rec := reflect.New(recType).Elem()
	rec.Field(0).SetString("4ce3f93b-cce8-4930-bd49-4bd849ccaf23")
	rec.Field(1).SetString("true")
	rec.Field(2).SetString("Necessitatibus fugit ipsam ab quia odit, and then " +
		"some more text to push this value past the point where yaml.v3 folds.")

	decode := func(t *testing.T, write func(w *yamlSeqWriter) error) []map[string]string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "out.yaml")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := newYAMLSeqWriter(f, structArr)
		if err := write(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]string
		if err := yaml.Unmarshal(b, &out); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return out
	}

	fast := decode(t, func(w *yamlSeqWriter) error { return w.WriteRecord(rec.Interface()) })
	slow := decode(t, func(w *yamlSeqWriter) error { return w.writeMarshaled(rec.Interface()) })
	if !reflect.DeepEqual(fast, slow) {
		t.Errorf("fast path %v != yaml.Marshal path %v", fast, slow)
	}
}

// TestYAMLSeqWriterFallback checks that a record type the fast path cannot
// describe is handed to yaml.Marshal instead of being emitted wrongly.
func TestYAMLSeqWriterFallback(t *testing.T) {
	structArr := []reflect.StructField{{
		Name: "N_0",
		Type: reflect.TypeOf(0),
		Tag:  `yaml:"n"`,
	}}
	f, err := os.Create(filepath.Join(t.TempDir(), "out.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	w := newYAMLSeqWriter(f, structArr)
	if w.keys != nil {
		t.Error("expected the yaml.Marshal fallback for a non-string field")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestYAMLPlainSafe(t *testing.T) {
	for _, s := range []string{"abc", "Alice", "a-b_c.d", "user@host", "a/b+c", "x1"} {
		if !yamlPlainSafe(s) {
			t.Errorf("yamlPlainSafe(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"", " ", "123", "1e5", "-x", ".5", "true", "TRUE", "no", "y", "~",
		"a b", "a: b", "a#b", "a:b", "*x", "[x", "日本",
	} {
		if yamlPlainSafe(s) {
			t.Errorf("yamlPlainSafe(%q) = true, want false", s)
		}
	}
}

// sqlRecordType builds the same kind of record the generator does for the sql
// format: a runtime struct of strings tagged with the column name.
func sqlRecordType(cols []string) ([]reflect.StructField, reflect.Type) {
	fields := make([]reflect.StructField, 0, len(cols))
	for i, name := range cols {
		fields = append(fields, reflect.StructField{
			Name: structFieldName(name, i),
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(fmt.Sprintf("sql:%q", name)),
		})
	}
	return fields, reflect.StructOf(fields)
}

// writeSQL runs rows through a fresh sqlWriter for the named dialect and
// returns the whole file.
func writeSQL(t *testing.T, dialect, table string, cols []string, rows [][]string) string {
	t.Helper()
	d, err := lookupSQLDialect(dialect)
	if err != nil {
		t.Fatal(err)
	}
	structArr, recType := sqlRecordType(cols)
	path := filepath.Join(t.TempDir(), "out.sql")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newSQLWriter(f, d, table, structArr)
	for _, row := range rows {
		rec := reflect.New(recType).Elem()
		for i, v := range row {
			rec.Field(i).SetString(v)
		}
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSQLWriterDialects pins the exact statement each dialect gets. Only
// sqlite can be executed here (see TestSQLWriterSQLiteRoundTrip), so for the
// other three this golden output is what stands in for running the file:
// identifier quoting, backslash handling and the N” prefix are precisely
// where the four disagree.
func TestSQLWriterDialects(t *testing.T) {
	cols := []string{"user", "note"}
	rows := [][]string{
		{"alice", `it's a \path\ to "quoted" text`},
		{"bob", "日本語"},
	}
	for _, tc := range []struct {
		dialect string
		want    string
	}{
		{"postgres", "INSERT INTO \"web_logs\" (\"user\", \"note\") VALUES" +
			"\n\t('alice', 'it''s a \\path\\ to \"quoted\" text')," +
			"\n\t('bob', '日本語');\n"},
		{"sqlite", "INSERT INTO \"web_logs\" (\"user\", \"note\") VALUES" +
			"\n\t('alice', 'it''s a \\path\\ to \"quoted\" text')," +
			"\n\t('bob', '日本語');\n"},
		// MySQL is the one dialect that reads backslash as an escape
		// character, so each one is doubled.
		{"mysql", "INSERT INTO `web_logs` (`user`, `note`) VALUES" +
			"\n\t('alice', 'it''s a \\\\path\\\\ to \"quoted\" text')," +
			"\n\t('bob', '日本語');\n"},
		// SQL Server needs N'' for the non-ASCII row, and only that row.
		{"mssql", "INSERT INTO [web_logs] ([user], [note]) VALUES" +
			"\n\t('alice', 'it''s a \\path\\ to \"quoted\" text')," +
			"\n\t('bob', N'日本語');\n"},
	} {
		t.Run(tc.dialect, func(t *testing.T) {
			if got := writeSQL(t, tc.dialect, "web_logs", cols, rows); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestSQLWriterSQLiteRoundTrip is the safety net for the hand-rolled literal
// escaping: sqlite actually executes the generated file, and every value must
// come back as exactly the string that went in.
func TestSQLWriterSQLiteRoundTrip(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}

	tricky := []string{
		"", "plain", "it's", "''", "a''b", `back\slash`, `\`, `\\`,
		`d"quote`, "`tick", "multi\nline", "tab\there", "ret\rurn",
		"semi;colon", "com,ma", "paren)", "--comment", "/*block*/",
		"0x1f", "123", "NULL", "null", "true", "日本語", "emoji 🎉",
		"nul\x00byte", "%pct", "_under", "a: b", strings.Repeat("x", 500),
	}
	// NUL is the one byte the writer drops, since no dialect's literal
	// syntax can carry one (see sqlDialect.appendString).
	want := make([]string, len(tricky))
	for i, v := range tricky {
		want[i] = strings.ReplaceAll(v, "\x00", "")
	}

	rows := make([][]string, len(tricky))
	for i, v := range tricky {
		rows[i] = []string{strconv.Itoa(i), v}
	}
	sqlText := writeSQL(t, "sqlite", "vals", []string{"id", "v"}, rows)

	dir := t.TempDir()
	sqlPath := filepath.Join(dir, "out.sql")
	if err := os.WriteFile(sqlPath, []byte(sqlText), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	// -json rather than the default output mode so values holding newlines,
	// tabs or commas survive the trip back out of the CLI unambiguously.
	cmd := exec.Command(sqlite, "-json", dbPath,
		"CREATE TABLE vals (id TEXT, v TEXT);",
		".read "+sqlPath,
		"SELECT v FROM vals ORDER BY CAST(id AS INTEGER);",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3: %v\n%s\nfile:\n%s", err, out, sqlText)
	}
	var got []struct{ V string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("sqlite3 output is not json: %v\n%s", err, out)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].V != want[i] {
			t.Errorf("row %d: got %q, want %q", i, got[i].V, want[i])
		}
	}
}

// TestSQLWriterBatching checks the statement boundary: a run is split every
// maxRowsPerStatement rows, and a partial final statement is still terminated.
func TestSQLWriterBatching(t *testing.T) {
	for _, tc := range []struct {
		rows       int
		wantStmts  int
		wantTuples int
	}{
		{1, 1, 1},
		{maxRowsPerStatement - 1, 1, maxRowsPerStatement - 1},
		{maxRowsPerStatement, 1, maxRowsPerStatement},
		{maxRowsPerStatement + 1, 2, maxRowsPerStatement + 1},
		{2*maxRowsPerStatement + 3, 3, 2*maxRowsPerStatement + 3},
	} {
		t.Run(strconv.Itoa(tc.rows), func(t *testing.T) {
			rows := make([][]string, tc.rows)
			for i := range rows {
				rows[i] = []string{strconv.Itoa(i)}
			}
			got := writeSQL(t, "postgres", "t", []string{"id"}, rows)
			if n := strings.Count(got, "INSERT INTO"); n != tc.wantStmts {
				t.Errorf("got %d statements, want %d", n, tc.wantStmts)
			}
			if n := strings.Count(got, ";\n"); n != tc.wantStmts {
				t.Errorf("got %d terminators, want %d", n, tc.wantStmts)
			}
			if n := strings.Count(got, "\n\t("); n != tc.wantTuples {
				t.Errorf("got %d row tuples, want %d", n, tc.wantTuples)
			}
			// A terminator must only ever appear at the very end of a
			// statement, never before a row tuple of the same one.
			if strings.Contains(got, ";\n\t(") {
				t.Error("statement terminated before its last row tuple")
			}
		})
	}
}

// TestSQLWriterEmptyRun checks that a zero-record run produces an empty file
// rather than a dangling "INSERT INTO ... VALUES;".
func TestSQLWriterEmptyRun(t *testing.T) {
	if got := writeSQL(t, "postgres", "t", []string{"id"}, nil); got != "" {
		t.Errorf("got %q, want an empty file", got)
	}
}

func TestSQLWriterFieldCountMismatch(t *testing.T) {
	d, err := lookupSQLDialect("postgres")
	if err != nil {
		t.Fatal(err)
	}
	structArr, _ := sqlRecordType([]string{"a", "b"})
	f, err := os.Create(filepath.Join(t.TempDir(), "out.sql"))
	if err != nil {
		t.Fatal(err)
	}
	w := newSQLWriter(f, d, "t", structArr)
	if err := w.WriteRecord(struct{ A string }{"x"}); err == nil {
		t.Error("expected an error for a record with the wrong field count")
	}
	if err := w.WriteRecord("not a struct"); err == nil {
		t.Error("expected an error for a non-struct record")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLookupSQLDialect(t *testing.T) {
	for in, want := range map[string]string{
		"":           "postgres",
		"  ":         "postgres",
		"postgres":   "postgres",
		"PostgreSQL": "postgres",
		"pg":         "postgres",
		" MySQL ":    "mysql",
		"mariadb":    "mysql",
		"sqlite":     "sqlite",
		"sqlite3":    "sqlite",
		"mssql":      "mssql",
		"sqlserver":  "mssql",
		"tsql":       "mssql",
	} {
		d, err := lookupSQLDialect(in)
		if err != nil {
			t.Errorf("lookupSQLDialect(%q): %v", in, err)
			continue
		}
		if d.name != want {
			t.Errorf("lookupSQLDialect(%q) = %q, want %q", in, d.name, want)
		}
	}
	// An unknown dialect must fail loudly: falling back to another one's
	// escaping would only surface as an error inside the database.
	if _, err := lookupSQLDialect("oracle"); err == nil {
		t.Error("expected an error for an unknown dialect")
	} else if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error should list the valid dialects, got: %v", err)
	}
}

func TestSQLTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config mockagen.MockagenConfig
		want   string
	}{
		{"explicit table wins", mockagen.MockagenConfig{Name: "web logs", Table: "dbo.events"}, "dbo.events"},
		{"blank table falls back to name", mockagen.MockagenConfig{Name: "web logs", Table: "  "}, "web_logs"},
		{"derived from name", mockagen.MockagenConfig{Name: "Web Logs"}, "web_logs"},
		{"no name at all", mockagen.MockagenConfig{}, "mockagen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqlTable(tc.config); got != tc.want {
				t.Errorf("sqlTable() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSQLIdentQuoting covers the identifier cases the generator cannot
// produce but a hand-written config can: a dotted table name, and a name
// holding the dialect's own closing quote.
func TestSQLIdentQuoting(t *testing.T) {
	for _, tc := range []struct {
		dialect string
		table   string
		want    string
	}{
		{"mssql", "dbo.users", "[dbo].[users]"},
		{"postgres", "public.users", `"public"."users"`},
		{"mysql", "app.users", "`app`.`users`"},
		{"mssql", "we[ir]d", "[we[ir]]d]"},
		{"postgres", `we"ird`, `"we""ird"`},
		{"mysql", "we`ird", "`we``ird`"},
	} {
		t.Run(tc.dialect+"/"+tc.table, func(t *testing.T) {
			d, err := lookupSQLDialect(tc.dialect)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(d.appendQualifiedIdent(nil, tc.table)); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// csvRecordType builds the same kind of record the generator does for the csv
// format: a runtime struct of strings tagged with the column name.
func csvRecordType(cols []string) ([]reflect.StructField, reflect.Type) {
	fields := make([]reflect.StructField, 0, len(cols))
	for i, name := range cols {
		fields = append(fields, reflect.StructField{
			Name: structFieldName(name, i),
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(fmt.Sprintf("csv:%q", name)),
		})
	}
	return fields, reflect.StructOf(fields)
}

// writeCSV runs rows through a fresh csvWriter and returns the whole file.
func writeCSV(t *testing.T, cols []string, rows [][]string, includeHeader bool) string {
	t.Helper()
	structArr, recType := csvRecordType(cols)
	path := filepath.Join(t.TempDir(), "out.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newCSVWriter(f, structArr, includeHeader)
	for _, row := range rows {
		rec := reflect.New(recType).Elem()
		for i, v := range row {
			rec.Field(i).SetString(v)
		}
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// csvTricky is the value set both csv tests run on: everything that decides
// whether a field gets quoted, plus values that must survive verbatim.
var csvTricky = []string{
	"", "plain", "a,b", `a"b`, `"`, `""`, "multi\nline", "ret\rurn",
	"crlf\r\nhere", " leading", "trailing ", "  ", "\tleading-tab",
	" nbsp-leading", "mid dle", `\.`, `\.x`, `x\.`, "-", "0x1f",
	"123", "NULL", "true", "日本語", "emoji 🎉", "nul\x00byte",
	"\xff\xfe invalid utf8", strings.Repeat("x", 500),
}

// TestCSVWriterMatchesStdlib is the core guarantee for the hand-rolled
// renderer: for the same records it must produce exactly the bytes
// encoding/csv would. Byte equality rather than a round trip, because it
// pins the quoting decisions too, not just what survives re-reading.
func TestCSVWriterMatchesStdlib(t *testing.T) {
	cols := []string{"id", "v"}
	rows := make([][]string, len(csvTricky))
	for i, v := range csvTricky {
		rows[i] = []string{strconv.Itoa(i), v}
	}

	got := writeCSV(t, cols, rows, true)

	var want strings.Builder
	cw := csv.NewWriter(&want)
	if err := cw.Write(cols); err != nil {
		t.Fatal(err)
	}
	if err := cw.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		t.Fatal(err)
	}

	if got != want.String() {
		t.Errorf("output differs from encoding/csv\ngot:\n%q\nwant:\n%q", got, want.String())
	}
}

// TestCSVWriterRoundTrip checks the other direction: encoding/csv's Reader
// must give back exactly the strings that went in.
func TestCSVWriterRoundTrip(t *testing.T) {
	cols := []string{"id", "v"}
	rows := make([][]string, len(csvTricky))
	for i, v := range csvTricky {
		rows[i] = []string{strconv.Itoa(i), v}
	}
	out := writeCSV(t, cols, rows, true)

	// encoding/csv's Reader collapses a CRLF inside a quoted field to a bare
	// LF, so a value holding one does not survive a round trip - through the
	// stdlib's own Writer either, which this writer is byte-identical to (see
	// TestCSVWriterMatchesStdlib). A lone CR is unaffected. The expectation
	// has to account for it; there is no encoding of a CRLF in a quoted field
	// that the Reader gives back intact.
	want := make([][]string, len(rows))
	for i, row := range rows {
		want[i] = make([]string, len(row))
		for j, v := range row {
			want[i][j] = strings.ReplaceAll(v, "\r\n", "\n")
		}
	}

	r := csv.NewReader(strings.NewReader(out))
	// Values holding a bare CR or LF make a record span lines; the reader
	// handles that inside quotes, but the field count must stay fixed.
	r.FieldsPerRecord = len(cols)
	got, err := r.ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%q", err, out)
	}
	if len(got) != len(want)+1 {
		t.Fatalf("got %d records, want %d", len(got), len(want)+1)
	}
	if !reflect.DeepEqual(got[0], cols) {
		t.Errorf("header: got %q, want %q", got[0], cols)
	}
	for i, row := range want {
		if !reflect.DeepEqual(got[i+1], row) {
			t.Errorf("record %d: got %q, want %q", i, got[i+1], row)
		}
	}
}

// TestCSVWriterHeader covers include_header in both positions, and that the
// header is written even when no records follow it.
func TestCSVWriterHeader(t *testing.T) {
	cols := []string{"first_name", "email"}
	rows := [][]string{{"alice", "a@example.com"}}

	if got, want := writeCSV(t, cols, rows, true), "first_name,email\nalice,a@example.com\n"; got != want {
		t.Errorf("with header: got %q, want %q", got, want)
	}
	if got, want := writeCSV(t, cols, rows, false), "alice,a@example.com\n"; got != want {
		t.Errorf("without header: got %q, want %q", got, want)
	}
	if got, want := writeCSV(t, cols, nil, true), "first_name,email\n"; got != want {
		t.Errorf("header, no records: got %q, want %q", got, want)
	}
	if got := writeCSV(t, cols, nil, false); got != "" {
		t.Errorf("no header, no records: got %q, want an empty file", got)
	}
}

// TestCSVColumnName pins the tag precedence, including the quoting a header
// name needs when the column name itself holds a comma.
func TestCSVColumnName(t *testing.T) {
	structArr := []reflect.StructField{
		{Name: "A_0", Type: reflect.TypeOf(""), Tag: `csv:"from_csv" json:"from_json"`},
		{Name: "B_1", Type: reflect.TypeOf(""), Tag: `json:"from_json"`},
		{Name: "Plain_2", Type: reflect.TypeOf("")},
		{Name: "D_3", Type: reflect.TypeOf(""), Tag: `csv:"a,b"`},
	}
	for i, want := range []string{"from_csv", "from_json", "plain_2", "a,b"} {
		if got := csvColumnName(structArr[i]); got != want {
			t.Errorf("field %d: got %q, want %q", i, got, want)
		}
	}

	f, err := os.Create(filepath.Join(t.TempDir(), "out.csv"))
	if err != nil {
		t.Fatal(err)
	}
	w := newCSVWriter(f, structArr, true)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if want := "from_csv,from_json,plain_2,\"a,b\"\n"; string(b) != want {
		t.Errorf("header: got %q, want %q", b, want)
	}
}

func TestCSVWriterFieldCountMismatch(t *testing.T) {
	structArr, _ := csvRecordType([]string{"a", "b"})
	f, err := os.Create(filepath.Join(t.TempDir(), "out.csv"))
	if err != nil {
		t.Fatal(err)
	}
	w := newCSVWriter(f, structArr, false)
	if err := w.WriteRecord(struct{ A string }{"x"}); err == nil {
		t.Error("expected an error for a record with the wrong field count")
	}
	if err := w.WriteRecord("not a struct"); err == nil {
		t.Error("expected an error for a non-struct record")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLookupSQLMode(t *testing.T) {
	for in, want := range map[string]sqlMode{
		"":            sqlModeInsert,
		"  ":          sqlModeInsert,
		"insert":      sqlModeInsert,
		"INSERT":      sqlModeInsert,
		"inserts":     sqlModeInsert,
		"statements":  sqlModeInsert,
		"bulk":        sqlModeBulk,
		" Bulk ":      sqlModeBulk,
		"bulk_import": sqlModeBulk,
		"copy":        sqlModeBulk,
		"load":        sqlModeBulk,
	} {
		got, err := lookupSQLMode(in)
		if err != nil {
			t.Errorf("lookupSQLMode(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("lookupSQLMode(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := lookupSQLMode("upsert"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

// bulkCommand strips the explanatory comments and blank lines from a
// generated script, leaving just the commands. The comments carry the caveats
// a user needs but churn freely; the commands are what must be exact.
func bulkCommand(script string) string {
	var keep []string
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// TestBulkLoadScript pins the command each dialect gets. Only sqlite can be
// executed here (see TestSQLBulkWriterSQLiteExecutes), so for the other three
// this is what stands in for running it.
func TestBulkLoadScript(t *testing.T) {
	cols := []string{"id", "email"}
	for _, tc := range []struct {
		dialect       string
		table         string
		includeHeader bool
		want          string
	}{
		{"postgres", "public.users", true,
			`\copy "public"."users" ("id", "email") FROM 'users.csv' WITH (FORMAT csv, HEADER)`},
		{"postgres", "users", false,
			`\copy "users" ("id", "email") FROM 'users.csv' WITH (FORMAT csv)`},
		{"mysql", "users", true,
			"LOAD DATA LOCAL INFILE 'users.csv' INTO TABLE `users`\n" +
				"\tFIELDS TERMINATED BY ',' ENCLOSED BY '\"'\n" +
				"\tLINES TERMINATED BY '\\n'\n" +
				"\tIGNORE 1 LINES\n" +
				"\t(`id`, `email`);"},
		{"mysql", "users", false,
			"LOAD DATA LOCAL INFILE 'users.csv' INTO TABLE `users`\n" +
				"\tFIELDS TERMINATED BY ',' ENCLOSED BY '\"'\n" +
				"\tLINES TERMINATED BY '\\n'\n" +
				"\t(`id`, `email`);"},
		{"mssql", "dbo.users", true,
			"BULK INSERT [dbo].[users]\n\tFROM 'users.csv'\n" +
				"\tWITH (FORMAT = 'CSV', FIRSTROW = 2, FIELDTERMINATOR = ',', ROWTERMINATOR = '0x0a', FIELDQUOTE = '\"');"},
		{"mssql", "users", false,
			"BULK INSERT [users]\n\tFROM 'users.csv'\n" +
				"\tWITH (FORMAT = 'CSV', FIRSTROW = 1, FIELDTERMINATOR = ',', ROWTERMINATOR = '0x0a', FIELDQUOTE = '\"');"},
		{"sqlite", "users", true, ".mode csv\n.import --skip 1 users.csv users"},
		{"sqlite", "users", false, ".mode csv\n.import users.csv users"},
	} {
		name := fmt.Sprintf("%s/header=%v", tc.dialect, tc.includeHeader)
		t.Run(name, func(t *testing.T) {
			d, err := lookupSQLDialect(tc.dialect)
			if err != nil {
				t.Fatal(err)
			}
			script := string(bulkLoadScript(d, tc.table, cols, "users.csv", tc.includeHeader))
			if got := bulkCommand(script); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			// Every script has to say where the CSV is read from relative to,
			// since that is the one thing a relative path makes ambiguous.
			if !strings.Contains(script, "users.csv") {
				t.Error("script does not mention the CSV file name")
			}
		})
	}
}

// TestBulkLoadScriptMSSQLWarnsAboutPath guards the one case where the
// generated command cannot work as written: BULK INSERT resolves the path on
// the server, so the script must say so rather than look runnable.
func TestBulkLoadScriptMSSQLWarnsAboutPath(t *testing.T) {
	d, err := lookupSQLDialect("mssql")
	if err != nil {
		t.Fatal(err)
	}
	script := string(bulkLoadScript(d, "users", []string{"id"}, "users.csv", true))
	for _, want := range []string{"WARNING", "SERVER", "bcp", "POSITION"} {
		if !strings.Contains(script, want) {
			t.Errorf("mssql script should mention %q:\n%s", want, script)
		}
	}
}

// newTestBulkWriter builds a sqlBulkWriter in a temp dir, as newRecordWriter
// would, and returns the dir plus the path main() would have passed in.
func newTestBulkWriter(t *testing.T, dialect, table string, cols []string, includeHeader bool) (*sqlBulkWriter, string) {
	t.Helper()
	d, err := lookupSQLDialect(dialect)
	if err != nil {
		t.Fatal(err)
	}
	structArr, _ := csvRecordType(cols)
	dir := t.TempDir()
	w, err := newSQLBulkWriter(filepath.Join(dir, "out.sql"), d, table, structArr, includeHeader)
	if err != nil {
		t.Fatal(err)
	}
	return w, dir
}

// TestSQLBulkWriterFiles checks the two-file contract: the data lands in
// <name>.csv, the script in <name>.load.sql, and nothing is written to the
// <name>.sql path the format's extension implies.
func TestSQLBulkWriterFiles(t *testing.T) {
	cols := []string{"id", "email"}
	w, dir := newTestBulkWriter(t, "postgres", "users", cols, true)
	structArr, recType := csvRecordType(cols)
	_ = structArr
	rec := reflect.New(recType).Elem()
	rec.Field(0).SetString("1")
	rec.Field(1).SetString("a@example.com")
	if err := w.WriteRecord(rec.Interface()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	csvBytes, err := os.ReadFile(filepath.Join(dir, "out.csv"))
	if err != nil {
		t.Fatalf("data file: %v", err)
	}
	if want := "id,email\n1,a@example.com\n"; string(csvBytes) != want {
		t.Errorf("data file: got %q, want %q", csvBytes, want)
	}
	scriptBytes, err := os.ReadFile(filepath.Join(dir, "out.load.sql"))
	if err != nil {
		t.Fatalf("script file: %v", err)
	}
	// The script must point at the CSV by bare name, not by any path, so it
	// resolves from the directory holding both files.
	if !strings.Contains(string(scriptBytes), "'out.csv'") {
		t.Errorf("script should reference 'out.csv' by bare name:\n%s", scriptBytes)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.sql")); !os.IsNotExist(err) {
		t.Error("bulk mode should not create the .sql path at all")
	}
}

// TestSQLBulkWriterSQLiteQualifiedTable covers the case sqlite loads into the
// wrong table silently: it must be refused, and before any file is created.
func TestSQLBulkWriterSQLiteQualifiedTable(t *testing.T) {
	d, err := lookupSQLDialect("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	structArr, _ := csvRecordType([]string{"id"})
	dir := t.TempDir()
	if _, err := newSQLBulkWriter(filepath.Join(dir, "out.sql"), d, "public.users", structArr, true); err == nil {
		t.Fatal("expected an error for a qualified table in sqlite bulk mode")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("no files should be created when the config is refused, found %d", len(entries))
	}
	// The same table is legal in insert mode, where it addresses an ATTACHed
	// database rather than being pasted into a dot-command.
	if got := writeSQL(t, "sqlite", "public.users", []string{"id"}, [][]string{{"1"}}); !strings.Contains(got, `"public"."users"`) {
		t.Errorf("insert mode should still accept a qualified table: %q", got)
	}
}

// TestSQLBulkWriterSQLiteExecutes runs the generated script through the real
// sqlite3 CLI, from the directory holding both files, exactly as the script's
// own instructions say to. It is the end-to-end check that the relative path,
// the header skip and the CSV quoting all line up.
//
// Values are compared as hex, not as text: it makes the check exact at the
// byte level, and avoids sqlite3's -json output replacing bytes that are not
// valid UTF-8 (which it does even though the stored value is intact - the
// column really does hold 61FFFE62 for "a\xff\xfeb").
func TestSQLBulkWriterSQLiteExecutes(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not installed")
	}
	cols := []string{"id", "v"}
	w, dir := newTestBulkWriter(t, "sqlite", "vals", cols, true)
	_, recType := csvRecordType(cols)

	// sqlite's .import truncates a field at a NUL byte, so that one value
	// cannot survive this path - a property of its CSV reader, not of what
	// this writer emits (TestCSVWriterMatchesStdlib covers the bytes). Note
	// that .import *does* preserve a quoted CRLF, unlike encoding/csv's
	// reader, so no normalisation is needed here.
	var want []string
	for _, v := range csvTricky {
		if strings.ContainsRune(v, 0) {
			continue
		}
		want = append(want, v)
	}

	for i, v := range want {
		rec := reflect.New(recType).Elem()
		rec.Field(0).SetString(strconv.Itoa(i))
		rec.Field(1).SetString(v)
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "test.db")
	if out, err := exec.Command(sqlite, dbPath,
		"CREATE TABLE vals (id TEXT, v TEXT);").CombinedOutput(); err != nil {
		t.Fatalf("create table: %v\n%s", err, out)
	}
	script, err := os.Open(filepath.Join(dir, "out.load.sql"))
	if err != nil {
		t.Fatal(err)
	}
	defer script.Close()
	// Run from dir, so the script's bare CSV name resolves the way its own
	// instructions promise it will.
	cmd := exec.Command(sqlite, "test.db")
	cmd.Dir = dir
	cmd.Stdin = script
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("load script: %v\n%s", err, out)
	}

	out, err := exec.Command(sqlite, "-noheader", "-list", dbPath,
		"SELECT hex(v) FROM vals ORDER BY CAST(id AS INTEGER);").Output()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("loaded %d rows, want %d", len(got), len(want))
	}
	for i, v := range want {
		if wantHex := strings.ToUpper(hex.EncodeToString([]byte(v))); got[i] != wantHex {
			t.Errorf("row %d (%q): got hex %s, want %s", i, v, got[i], wantHex)
		}
	}
}

// fixedCol is a column with the given fixed-width range.
func fixedCol(name string, start, end int64) mockagen.MockagenColumn {
	return mockagen.MockagenColumn{Name: name, StartPosition: start, EndPosition: end}
}

// TestValidateFixedWidth covers each way a position range can be unusable.
// Two of them used to panic inside go-fixedwidth and the third dropped the
// column silently, so all three are regressions worth pinning.
func TestValidateFixedWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		cols []mockagen.MockagenColumn
		// wantNamed are the column names the error must mention; empty means
		// the config is valid.
		wantNamed []string
	}{
		{"valid", []mockagen.MockagenColumn{fixedCol("a", 1, 10), fixedCol("b", 11, 20)}, nil},
		{"adjacent single columns", []mockagen.MockagenColumn{fixedCol("a", 1, 1), fixedCol("b", 2, 2)}, nil},
		{"no columns at all", nil, nil},
		{"all positions unset", []mockagen.MockagenColumn{fixedCol("a", 0, 0), fixedCol("b", 0, 0)}, []string{"a", "b"}},
		{"zero start position", []mockagen.MockagenColumn{fixedCol("a", 0, 10), fixedCol("b", 11, 20)}, []string{"a"}},
		{"inverted range", []mockagen.MockagenColumn{fixedCol("a", 5, 2), fixedCol("b", 11, 20)}, []string{"a"}},
		{"negative start", []mockagen.MockagenColumn{fixedCol("a", -1, 10)}, []string{"a"}},
		{"several bad at once", []mockagen.MockagenColumn{fixedCol("a", 0, 0), fixedCol("b", 1, 10), fixedCol("c", 9, 3)}, []string{"a", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFixedWidth(tc.cols)
			if tc.cols == nil {
				// The empty case is still an error, just not a named one.
				if err == nil {
					t.Error("expected an error for a config with no columns")
				}
				return
			}
			if len(tc.wantNamed) == 0 {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, name := range tc.wantNamed {
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
					t.Errorf("error should name column %q: %v", name, err)
				}
			}
			// A column with a usable range must not be blamed alongside them.
			for _, col := range tc.cols {
				if slices.Contains(tc.wantNamed, col.Name) {
					continue
				}
				if strings.Contains(err.Error(), fmt.Sprintf("%q", col.Name)) {
					t.Errorf("error should not name valid column %q: %v", col.Name, err)
				}
			}
		})
	}
}

// TestNewRecordWriterFixedRejectsBadPositions checks the guard runs before any
// file is created, so a rejected config leaves nothing behind.
func TestNewRecordWriterFixedRejectsBadPositions(t *testing.T) {
	dir := t.TempDir()
	config := mockagen.MockagenConfig{
		FileFormat: "fixed",
		Name:       "bad",
		Columns:    []mockagen.MockagenColumn{fixedCol("a", 0, 0)},
	}
	structArr, _ := csvRecordType([]string{"a"})
	if _, err := newRecordWriter(config, structArr, filepath.Join(dir, "out.fixed")); err == nil {
		t.Fatal("expected an error for unset fixed-width positions")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("no file should be created when the config is refused, found %d", len(entries))
	}
}

// TestFixedWidthWriterRoundTrip is the coverage the format never had: a valid
// config must still produce lines that hold each value at its declared
// columns. Positions are 1-based and inclusive, so "a" at 1-10 occupies
// bytes 0 through 9.
func TestFixedWidthWriterRoundTrip(t *testing.T) {
	cols := []mockagen.MockagenColumn{fixedCol("a", 1, 10), fixedCol("b", 11, 20)}
	if err := validateFixedWidth(cols); err != nil {
		t.Fatalf("config should be valid: %v", err)
	}
	fields := make([]reflect.StructField, len(cols))
	for i, col := range cols {
		fields[i] = reflect.StructField{
			Name: structFieldName(col.Name, i),
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(fmt.Sprintf(`fixed:"%d,%d"`, col.StartPosition, col.EndPosition)),
		}
	}
	recType := reflect.StructOf(fields)

	path := filepath.Join(t.TempDir(), "out.fixed")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newFixedWidthWriter(f)
	want := [][2]string{{"Calista", "Carroll"}, {"Brook", "Mills"}}
	for _, row := range want {
		rec := reflect.New(recType).Elem()
		rec.Field(0).SetString(row[0])
		rec.Field(1).SetString(row[1])
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), b)
	}
	for i, line := range lines {
		if len(line) != 20 {
			t.Errorf("line %d: got width %d, want 20: %q", i, len(line), line)
		}
		if got := strings.TrimRight(line[0:10], " "); got != want[i][0] {
			t.Errorf("line %d cols 1-10: got %q, want %q", i, got, want[i][0])
		}
		if got := strings.TrimRight(line[10:20], " "); got != want[i][1] {
			t.Errorf("line %d cols 11-20: got %q, want %q", i, got, want[i][1])
		}
	}
}
