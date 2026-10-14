package sqlgen_test

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen/sqlgen"
)

func TestLookup(t *testing.T) {
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
		d, err := sqlgen.Lookup(in)
		if err != nil {
			t.Errorf("Lookup(%q): %v", in, err)
			continue
		}
		if d.Name != want {
			t.Errorf("Lookup(%q) = %q, want %q", in, d.Name, want)
		}
	}

	// An unknown dialect must fail loudly: falling back to another one's
	// escaping would only surface as an error inside the database.
	if _, err := sqlgen.Lookup("oracle"); err == nil {
		t.Error("expected an error for an unknown dialect")
	} else if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error should list the valid dialects, got: %v", err)
	}
}

func TestTable(t *testing.T) {
	cases := []struct {
		name      string
		tableName string
		cfgName   string
		want      string
	}{
		{
			name:      "explicit table wins",
			cfgName:   "web logs",
			tableName: "dbo.events",
			want:      "dbo.events",
		},
		{
			name:      "blank table falls back to name",
			cfgName:   "web logs",
			tableName: "  ",
			want:      "web_logs",
		},
		{
			name:      "derived from name",
			cfgName:   "Web Logs",
			tableName: "",
			want:      "web_logs",
		},
		{
			name:      "no name at all",
			cfgName:   "",
			tableName: "",
			want:      "mockagen",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sqlgen.Table(tc.cfgName, tc.tableName)
			if got != tc.want {
				t.Errorf("Table(%q, %q) = %q, want %q",
					tc.cfgName, tc.tableName, got, tc.want)
			}
		})
	}
}

// TestIdentQuoting covers the identifier cases the generator cannot
// produce but a hand-written config can: a dotted table name, and a name
// holding the dialect's own closing quote.
func TestIdentQuoting(t *testing.T) {
	cases := []struct {
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
	}

	for _, tc := range cases {
		t.Run(tc.dialect+"/"+tc.table, func(t *testing.T) {
			d, err := sqlgen.Lookup(tc.dialect)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(d.AppendQualifiedIdent(nil, tc.table)); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// bulkCommand strips explanatory comments and blank lines from a generated
// script, leaving only the executable commands for comparison.
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

// TestBulkLoadScript pins the command each dialect gets for CSV ingestion.
func TestBulkLoadScript(t *testing.T) {
	cols := []string{"id", "email"}
	cases := []struct {
		dialect       string
		table         string
		includeHeader bool
		want          string
	}{
		{
			dialect:       "postgres",
			table:         "public.users",
			includeHeader: true,
			want:          `\copy "public"."users" ("id", "email") FROM 'users.csv' WITH (FORMAT csv, HEADER)`,
		},
		{
			dialect:       "postgres",
			table:         "users",
			includeHeader: false,
			want:          `\copy "users" ("id", "email") FROM 'users.csv' WITH (FORMAT csv)`,
		},
		{
			dialect:       "mysql",
			table:         "users",
			includeHeader: true,
			want: "LOAD DATA LOCAL INFILE 'users.csv' INTO TABLE `users`\n" +
				"\tFIELDS TERMINATED BY ',' ENCLOSED BY '\"' ESCAPED BY ''\n" +
				"\tLINES TERMINATED BY '\\n'\n" +
				"\tIGNORE 1 LINES\n" +
				"\t(`id`, `email`);",
		},
		{
			dialect:       "mysql",
			table:         "users",
			includeHeader: false,
			want: "LOAD DATA LOCAL INFILE 'users.csv' INTO TABLE `users`\n" +
				"\tFIELDS TERMINATED BY ',' ENCLOSED BY '\"' ESCAPED BY ''\n" +
				"\tLINES TERMINATED BY '\\n'\n" +
				"\t(`id`, `email`);",
		},
		{
			dialect:       "mssql",
			table:         "dbo.users",
			includeHeader: true,
			want: "BULK INSERT [dbo].[users]\n\tFROM 'users.csv'\n" +
				"\tWITH (FORMAT = 'CSV', FIRSTROW = 2, FIELDTERMINATOR = ',', ROWTERMINATOR = '0x0a', FIELDQUOTE = '\"');",
		},
		{
			dialect:       "mssql",
			table:         "users",
			includeHeader: false,
			want: "BULK INSERT [users]\n\tFROM 'users.csv'\n" +
				"\tWITH (FORMAT = 'CSV', FIRSTROW = 1, FIELDTERMINATOR = ',', ROWTERMINATOR = '0x0a', FIELDQUOTE = '\"');",
		},
		{
			dialect:       "sqlite",
			table:         "users",
			includeHeader: true,
			want:          ".mode csv\n.import --skip 1 users.csv users",
		},
		{
			dialect:       "sqlite",
			table:         "users",
			includeHeader: false,
			want:          ".mode csv\n.import users.csv users",
		},
	}

	for _, tc := range cases {
		name := fmt.Sprintf("%s/header=%v", tc.dialect, tc.includeHeader)
		t.Run(name, func(t *testing.T) {
			d, err := sqlgen.Lookup(tc.dialect)
			if err != nil {
				t.Fatal(err)
			}
			script := string(sqlgen.BulkLoadScript(d, tc.table, cols, "users.csv", tc.includeHeader))
			if got := bulkCommand(script); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			if !strings.Contains(script, "users.csv") {
				t.Error("script does not mention the CSV file name")
			}
		})
	}
}

// TestBulkLoadScriptMSSQLWarnsAboutPath guards against generating a BULK INSERT
// command that looks runnable from a client without explaining server-side path
// resolution.
func TestBulkLoadScriptMSSQLWarnsAboutPath(t *testing.T) {
	d, err := sqlgen.Lookup("mssql")
	if err != nil {
		t.Fatal(err)
	}
	script := string(sqlgen.BulkLoadScript(d, "users", []string{"id"}, "users.csv", true))
	for _, want := range []string{"WARNING", "SERVER", "bcp", "POSITION", "65001"} {
		if !strings.Contains(script, want) {
			t.Errorf("mssql script should mention %q:\n%s", want, script)
		}
	}
}

// TestPlaceholders verifies parameter placeholder formatting and parameter
// limits across all dialects.
func TestPlaceholders(t *testing.T) {
	cases := []struct {
		dialect   string
		wantP1    string
		wantP5    string
		wantMax   int
		wantStyle sqlgen.PlaceholderStyle
	}{
		{
			dialect:   "postgres",
			wantP1:    "$1",
			wantP5:    "$5",
			wantMax:   65535,
			wantStyle: sqlgen.PlaceholderDollar,
		},
		{
			dialect:   "mysql",
			wantP1:    "?",
			wantP5:    "?",
			wantMax:   65535,
			wantStyle: sqlgen.PlaceholderQuestion,
		},
		{
			dialect:   "sqlite",
			wantP1:    "?",
			wantP5:    "?",
			wantMax:   32766,
			wantStyle: sqlgen.PlaceholderQuestion,
		},
		{
			dialect:   "mssql",
			wantP1:    "@p1",
			wantP5:    "@p5",
			wantMax:   2100,
			wantStyle: sqlgen.PlaceholderAtP,
		},
	}

	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			d, err := sqlgen.Lookup(tc.dialect)
			if err != nil {
				t.Fatal(err)
			}
			if d.PlaceholderStyle != tc.wantStyle {
				t.Errorf("PlaceholderStyle = %q, want %q", d.PlaceholderStyle, tc.wantStyle)
			}
			if d.MaxBoundParams != tc.wantMax {
				t.Errorf("MaxBoundParams = %d, want %d", d.MaxBoundParams, tc.wantMax)
			}
			if got := d.Placeholder(1); got != tc.wantP1 {
				t.Errorf("Placeholder(1) = %q, want %q", got, tc.wantP1)
			}
			if got := d.Placeholder(5); got != tc.wantP5 {
				t.Errorf("Placeholder(5) = %q, want %q", got, tc.wantP5)
			}
			if got := string(d.AppendPlaceholder(nil, 1)); got != tc.wantP1 {
				t.Errorf("AppendPlaceholder(1) = %q, want %q", got, tc.wantP1)
			}
		})
	}
}

// TestAppendValue covers all four dialects across null, int, float, raw,
// unicode, quote, backslash, CR and embedded-NUL inputs.
func TestAppendValue(t *testing.T) {
	dialects := []string{"postgres", "sqlite", "mysql", "mssql"}

	type testCase struct {
		name          string
		val           any
		wantByDialect map[string]string
		wantErr       error
	}

	cases := []testCase{
		{
			name: "null from nil",
			val:  nil,
			wantByDialect: map[string]string{
				"postgres": "NULL",
				"sqlite":   "NULL",
				"mysql":    "NULL",
				"mssql":    "NULL",
			},
		},
		{
			name: "positive integer",
			val:  42,
			wantByDialect: map[string]string{
				"postgres": "42",
				"sqlite":   "42",
				"mysql":    "42",
				"mssql":    "42",
			},
		},
		{
			name: "negative integer",
			val:  -17,
			wantByDialect: map[string]string{
				"postgres": "-17",
				"sqlite":   "-17",
				"mysql":    "-17",
				"mssql":    "-17",
			},
		},
		{
			name: "floating point number",
			val:  3.14,
			wantByDialect: map[string]string{
				"postgres": "3.14",
				"sqlite":   "3.14",
				"mysql":    "3.14",
				"mssql":    "3.14",
			},
		},
		{
			name: "raw SQL expression",
			val:  sqlgen.Raw("CURRENT_TIMESTAMP"),
			wantByDialect: map[string]string{
				"postgres": "CURRENT_TIMESTAMP",
				"sqlite":   "CURRENT_TIMESTAMP",
				"mysql":    "CURRENT_TIMESTAMP",
				"mssql":    "CURRENT_TIMESTAMP",
			},
		},
		{
			name: "unicode text",
			val:  "café",
			wantByDialect: map[string]string{
				"postgres": "'café'",
				"sqlite":   "'café'",
				"mysql":    "'café'",
				"mssql":    "N'café'",
			},
		},
		{
			name: "single quote escaping",
			val:  "O'Reilly",
			wantByDialect: map[string]string{
				"postgres": "'O''Reilly'",
				"sqlite":   "'O''Reilly'",
				"mysql":    "'O''Reilly'",
				"mssql":    "'O''Reilly'",
			},
		},
		{
			name: "backslash escaping",
			val:  "path\\to\\file",
			wantByDialect: map[string]string{
				"postgres": `'path\to\file'`,
				"sqlite":   `'path\to\file'`,
				"mysql":    `'path\\to\\file'`,
				"mssql":    `'path\to\file'`,
			},
		},
		{
			name: "carriage return escaping",
			val:  "line1\rline2",
			wantByDialect: map[string]string{
				"postgres": "'line1\rline2'",
				"sqlite":   "'line1\rline2'",
				"mysql":    `'line1\rline2'`,
				"mssql":    "'line1\rline2'",
			},
		},
		{
			name:    "embedded NUL string error",
			val:     "hello\x00world",
			wantErr: sqlgen.ErrEmbeddedNUL,
		},
		{
			name:    "embedded NUL bytes error",
			val:     []byte("hello\x00world"),
			wantErr: sqlgen.ErrEmbeddedNUL,
		},
	}

	for _, tc := range cases {
		for _, dialect := range dialects {
			testName := fmt.Sprintf("%s/%s", dialect, tc.name)
			t.Run(testName, func(t *testing.T) {
				d, err := sqlgen.Lookup(dialect)
				if err != nil {
					t.Fatal(err)
				}
				got, err := d.AppendValue(nil, tc.val)
				if tc.wantErr != nil {
					if err == nil {
						t.Fatalf("expected error %v, got nil", tc.wantErr)
					}
					if !errors.Is(err, tc.wantErr) {
						t.Errorf("got error %v, want %v", err, tc.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := tc.wantByDialect[dialect]
				if string(got) != want {
					t.Errorf("AppendValue = %q, want %q", string(got), want)
				}
			})
		}
	}
}

// TestAppendValueBooleans pins the one value kind the four dialects spell
// differently. SQL Server has no boolean type or TRUE/FALSE keywords, so a
// BIT column takes 1 and 0 and an INSERT carrying the bare word TRUE is
// rejected by the server rather than coerced.
func TestAppendValueBooleans(t *testing.T) {
	cases := []struct {
		dialect      string
		wantT, wantF string
	}{
		{"postgres", "TRUE", "FALSE"},
		{"sqlite", "TRUE", "FALSE"},
		{"mysql", "TRUE", "FALSE"},
		{"mssql", "1", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			d, err := sqlgen.Lookup(tc.dialect)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.dialect, err)
			}
			got, err := d.AppendValue(nil, true)
			if err != nil {
				t.Fatalf("AppendValue(true): %v", err)
			}
			if string(got) != tc.wantT {
				t.Errorf("true = %q, want %q", got, tc.wantT)
			}
			if got, err = d.AppendValue(nil, false); err != nil {
				t.Fatalf("AppendValue(false): %v", err)
			}
			if string(got) != tc.wantF {
				t.Errorf("false = %q, want %q", got, tc.wantF)
			}
		})
	}
}

// TestAppendValuePreservesBufferOnError covers the contract that makes errors
// reportable: a caller writing `dst, err = d.AppendValue(dst, v)` must still
// hold the statement it had built, so every failure path returns dst rather
// than nil.
func TestAppendValuePreservesBufferOnError(t *testing.T) {
	d, err := sqlgen.Lookup("postgres")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "INSERT INTO t VALUES ("
	for _, tc := range []struct {
		name string
		val  any
	}{
		{"embedded NUL in string", "a\x00b"},
		{"embedded NUL in bytes", []byte("a\x00b")},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"unsupported type", struct{ A int }{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := []byte(prefix)
			got, err := d.AppendValue(dst, tc.val)
			if err == nil {
				t.Fatalf("AppendValue(%v) succeeded, want an error", tc.val)
			}
			if string(got) != prefix {
				t.Errorf("buffer = %q, want it unchanged as %q", got, prefix)
			}
		})
	}
}
