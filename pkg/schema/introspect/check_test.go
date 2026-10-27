package introspect

import (
	"reflect"
	"slices"
	"testing"
)

func TestParsePostgresCheck(t *testing.T) {
	// The strings are what pg_get_constraintdef returned on postgres 17,
	// including the ANY(ARRAY) rewrite and the per-element cast. A parser
	// that looks for IN (...) accepts none of them.
	tests := []struct {
		in     string
		column string
		values []string
		ok     bool
	}{
		{
			in:     "CHECK ((tier = ANY (ARRAY['aa'::bpchar, 'bb'::bpchar, 'cc'::bpchar])))",
			column: "tier",
			values: []string{"aa", "bb", "cc"},
			ok:     true,
		},
		{
			in:     "CHECK ((tier = ANY (ARRAY['has, comma'::text, 'has''quote'::text])))",
			column: "tier",
			values: []string{"has, comma", "has'quote"},
			ok:     true,
		},
		{
			in:     `CHECK (("Tier" = ANY (ARRAY['a'::public.user_status])))`,
			column: "Tier",
			values: []string{"a"},
			ok:     true,
		},
		{in: "CHECK ((jsonb_typeof(doc) = 'object'::text))", ok: false},
		{in: "CHECK ((a = ANY (ARRAY['x'::text])) AND (b > 0))", ok: false},
		{in: "CHECK ((tier = ANY (ARRAY[])))", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			col, vals, ok := parsePostgresCheck(tt.in)
			if ok != tt.ok || col != tt.column || !slices.Equal(vals, tt.values) {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", col, vals, ok, tt.column, tt.values, tt.ok)
			}
		})
	}
}

func TestParseMySQLCheck(t *testing.T) {
	// CHECK_CLAUSE keeps the backslashes. Splitting on commas would be
	// wrong even after unescaping, because a label may contain one.
	tests := []struct {
		in     string
		column string
		values []string
		ok     bool
	}{
		{
			in:     "(`tier` in (_utf8mb4\\'aa\\',_utf8mb4\\'bb\\',_utf8mb4\\'cc\\'))",
			column: "tier",
			values: []string{"aa", "bb", "cc"},
			ok:     true,
		},
		{
			in:     "(`tier` in (_utf8mb4'aa',_utf8mb4'bb',_utf8mb4'cc'))",
			column: "tier",
			values: []string{"aa", "bb", "cc"},
			ok:     true,
		},
		{
			in:     "(`note` in (_utf8mb4'has, comma',_utf8mb4'has''quote'))",
			column: "note",
			values: []string{"has, comma", "has'quote"},
			ok:     true,
		},
		{in: "(json_schema_valid(_utf8mb4'{}', `doc`))", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			col, vals, ok := parseMySQLCheck(tt.in)
			if ok != tt.ok || col != tt.column || !slices.Equal(vals, tt.values) {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", col, vals, ok, tt.column, tt.values, tt.ok)
			}
		})
	}
}

func TestParseMySQLEnum(t *testing.T) {
	// A quote inside a label is doubled. split(",") turns the comma label
	// into two choices and leaves the quote escape attached to the next.
	got, ok := parseMySQLEnum("enum('plain','has, comma','has''quote','has:colon')")
	want := []string{"plain", "has, comma", "has'quote", "has:colon"}
	if !ok || !slices.Equal(got, want) {
		t.Fatalf("got (%q, %v), want (%q, true)", got, ok, want)
	}
	if _, ok := parseMySQLEnum("varchar(255)"); ok {
		t.Fatal("varchar parsed as an enum")
	}
}

func TestParseMSSQLCheck(t *testing.T) {
	// The engine rewrote the IN list into an OR chain and reversed it.
	// Declaration order is not in the catalog, so the values come back
	// sorted rather than in the order the server happened to emit.
	col, vals, ok := parseMSSQLCheck("([status]='closed' OR [status]='suspended' OR [status]='active')")
	want := []string{"active", "closed", "suspended"}
	if !ok || col != "status" || !slices.Equal(vals, want) {
		t.Fatalf("got (%q, %q, %v), want (status, %q, true)", col, vals, ok, want)
	}
	if _, _, ok := parseMSSQLCheck("([status]='closed' OR [other]='active')"); ok {
		t.Fatal("two columns parsed as one enum")
	}
	if _, _, ok := parseMSSQLCheck("(isjson([doc])>(0))"); ok {
		t.Fatal("ISJSON parsed as an enum")
	}
	if _, _, ok := parseMSSQLCheck("(json_path_exists([doc], N'$.a') = 1)"); ok {
		t.Fatal("JSON_PATH_EXISTS parsed as an enum")
	}
}

func TestParseSQLiteChecks(t *testing.T) {
	ddl := `CREATE TABLE users (
  status TEXT NOT NULL DEFAULT 'active'
              CHECK (status IN ('active', 'suspended', 'closed')),
  tier CHAR(2) CHECK (tier IN ('aa', 'bb', 'cc')),
  note TEXT CHECK (length(note) > 0)
)`
	got := parseSQLiteChecks(ddl)
	want := []parsedCheck{
		{column: "status", values: []string{"active", "suspended", "closed"}},
		{column: "tier", values: []string{"aa", "bb", "cc"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	// A commented-out check is not a constraint. Treating it as one would
	// invent enum values the table does not have.
	if got := parseSQLiteChecks("CREATE TABLE t (id INT /* CHECK (id IN ('a')) */)"); len(got) != 0 {
		t.Fatalf("comment parsed as a check: %#v", got)
	}
}

func TestStripParenLayers(t *testing.T) {
	// SQL Server wraps every default, and numeric defaults twice. The
	// function-call parentheses of sysutcdatetime() are the expression.
	tests := []struct{ in, want string }{
		{"(newid())", "newid()"},
		{"('active')", "'active'"},
		{"((0.00))", "0.00"},
		{"(sysutcdatetime())", "sysutcdatetime()"},
		{"gen_random_uuid()", "gen_random_uuid()"},
	}
	for _, tt := range tests {
		if got := stripParenLayers(tt.in); got != tt.want {
			t.Errorf("stripParenLayers(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestJSONChecksAreNotEnums(t *testing.T) {
	// Each parser has to bail on its own. A shared "contains IN" check
	// would still miss the postgres ANY rewrite and the mssql OR chain,
	// and would also miss these, but only if somebody remembered to add
	// them. Running every parser against every predicate is the guard.
	in := []string{
		"CHECK ((jsonb_typeof(doc) = 'object'::text))",
		"CHECK (ISJSON([doc])>(0))",
		"CHECK (JSON_PATH_EXISTS([doc], N'$.a') = 1)",
		"(JSON_SCHEMA_VALID(_utf8mb4'{}', `doc`) = 1)",
	}
	for _, def := range in {
		if _, _, ok := parsePostgresCheck(def); ok {
			t.Errorf("postgres accepted %q", def)
		}
		if _, _, ok := parseMySQLCheck(def); ok {
			t.Errorf("mysql accepted %q", def)
		}
		if _, _, ok := parseMSSQLCheck(def); ok {
			t.Errorf("mssql accepted %q", def)
		}
		if got := parseSQLiteChecks("CREATE TABLE t (doc TEXT CHECK " + def + ")"); len(got) != 0 {
			t.Errorf("sqlite accepted %q: %#v", def, got)
		}
	}
}
