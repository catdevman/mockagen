package introspect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/catdevman/mockagen/pkg/schema"
)

func TestInspectRejectsEmptyDialect(t *testing.T) {
	// sqlgen.Lookup turns an empty name into its default dialect. Doing
	// that here would read postgres because the flag was left off.
	_, err := Inspect(context.Background(), fakeQ{}, Request{Table: "users"})
	if !errors.Is(err, ErrUnknownDialect) {
		t.Fatalf("err = %v, want ErrUnknownDialect", err)
	}
}

func TestInspectCompositeUniqueIsNotUnique(t *testing.T) {
	// users_org_email_uniq covers the pair. Marking email unique because
	// of it would be wrong; the single-column users_email_key is what
	// sets the flag. org_id is only in the pair, so it stays not unique.
	q := scriptQ{
		"pg_attribute": []Row{
			{"ordinal": "1", "name": "email", "sql_type": "text", "nullable": "false"},
			{"ordinal": "2", "name": "org_id", "sql_type": "bigint", "nullable": "false"},
		},
		"pg_constraint": []Row{
			{"name": "users_email_key", "kind": "u", "column_name": "email"},
			{"name": "users_org_email_uniq", "kind": "u", "column_name": "org_id"},
			{"name": "users_org_email_uniq", "kind": "u", "column_name": "email"},
			{"name": "users_org_id_fkey", "kind": "f", "column_name": "org_id", "ref_schema": "public", "ref_table": "orgs", "ref_column": "id"},
		},
	}
	got, err := Inspect(context.Background(), q, Request{
		Dialect: "postgres", Schema: "public", Table: "users", At: fixedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	email := column(t, got, "email")
	org := column(t, got, "org_id")
	if !email.Unique {
		t.Error("email unique = false, want true")
	}
	if org.Unique {
		t.Error("org_id unique = true, want false")
	}
	if org.References == nil || org.References.Table != "orgs" || org.References.Column != "id" {
		t.Errorf("org_id references = %+v", org.References)
	}
	if email.Type != "" || email.InferredBy != "" {
		t.Errorf("email decisions were filled: type %q inferred_by %q", email.Type, email.InferredBy)
	}
}

func TestInspectEnumBeatsCheckAndJSONCheckDoesNot(t *testing.T) {
	q := scriptQ{
		"pg_attribute": []Row{
			{"ordinal": "1", "name": "status", "sql_type": "user_status", "base_type": "user_status", "is_enum": "true", "nullable": "false"},
			{"ordinal": "2", "name": "doc", "sql_type": "jsonb", "base_type": "jsonb", "is_enum": "false", "nullable": "true"},
		},
		"pg_enum": []Row{
			{"value": "active"},
			{"value": "suspended"},
			{"value": "closed"},
		},
		"pg_constraint": []Row{
			{"name": "users_doc_check", "kind": "c", "column_name": "doc", "definition": "CHECK ((jsonb_typeof(doc) = 'object'::text))"},
			{"name": "users_status_check", "kind": "c", "column_name": "status", "definition": "CHECK ((status = ANY (ARRAY['closed'::user_status, 'active'::user_status])))"},
		},
	}
	got, err := Inspect(context.Background(), q, Request{
		Dialect: "PostgreSQL", Schema: "public", Table: "users", At: fixedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := column(t, got, "status")
	want := []string{"active", "suspended", "closed"}
	if strings.Join(status.EnumValues, ",") != strings.Join(want, ",") {
		t.Errorf("status enum = %q, want %q", status.EnumValues, want)
	}
	doc := column(t, got, "doc")
	if doc.SQLType != "jsonb" || len(doc.EnumValues) != 0 {
		t.Errorf("doc = %+v, want jsonb and no enum", doc)
	}
}

func TestInspectMySQLEnumDropsMaxLength(t *testing.T) {
	// The catalog reports 9, the longest label. That is not a varchar width.
	q := scriptQ{
		"information_schema.COLUMNS": []Row{
			{
				"ordinal": "1", "name": "status", "sql_type": "enum('active','suspended','closed')",
				"base_type": "enum", "nullable": "0", "max_length": "9", "extra": "",
			},
		},
		"TABLE_CONSTRAINTS": []Row{},
	}
	got, err := Inspect(context.Background(), q, Request{
		Dialect: "mariadb", Schema: "mockagen", Table: "users", At: fixedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := column(t, got, "status")
	if status.MaxLength != nil {
		t.Errorf("max_length = %d, want unset", *status.MaxLength)
	}
	if strings.Join(status.EnumValues, ",") != "active,suspended,closed" {
		t.Errorf("enum = %q", status.EnumValues)
	}
	if got.Source.Table != "users" {
		t.Errorf("table = %q, want users", got.Source.Table)
	}
}

func TestInspectMSSQLCheckIsSorted(t *testing.T) {
	q := scriptQ{
		"sys.columns": []Row{
			{"ordinal": "1", "name": "status", "sql_type": "nvarchar(20)", "is_nullable": "false", "is_identity": "false", "max_length": "20"},
		},
		"sys.key_constraints": []Row{
			{"name": "users_status_check", "kind": "c", "column_name": "status", "definition": "([status]='closed' OR [status]='suspended' OR [status]='active')"},
		},
	}
	got, err := Inspect(context.Background(), q, Request{
		Dialect: "tsql", Schema: "dbo", Table: "users", At: fixedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := column(t, got, "status")
	if strings.Join(status.EnumValues, ",") != "active,closed,suspended" {
		t.Errorf("enum = %q, want sorted active,closed,suspended", status.EnumValues)
	}
}

func TestInspectSQLiteOrdinalAndRowID(t *testing.T) {
	q := scriptQ{
		"pragma_table_info": []Row{
			{"ordinal": "0", "name": "id", "sql_type": "INTEGER", "notnull": "1", "pk": "1"},
			{"ordinal": "1", "name": "seq", "sql_type": "INT", "notnull": "0", "pk": "0"},
			{"ordinal": "2", "name": "email", "sql_type": "TEXT", "notnull": "1", "pk": "0"},
		},
		"pragma_index_list": []Row{
			{"index_name": "sqlite_autoindex_users_1", "origin": "pk", "column_name": "id"},
			{"index_name": "sqlite_autoindex_users_2", "origin": "u", "column_name": "email"},
			{"index_name": "sqlite_autoindex_users_3", "origin": "u", "column_name": "org_id"},
			{"index_name": "sqlite_autoindex_users_3", "origin": "u", "column_name": "email"},
		},
		"pragma_foreign_key_list": []Row{},
		"sqlite_master": []Row{
			{"sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, seq INT, email TEXT UNIQUE, UNIQUE (org_id, email))"},
		},
	}
	got, err := Inspect(context.Background(), q, Request{Dialect: "sqlite3", Table: "users", At: fixedAt})
	if err != nil {
		t.Fatal(err)
	}
	id := column(t, got, "id")
	if id.Ordinal != 1 {
		t.Errorf("id ordinal = %d, want 1", id.Ordinal)
	}
	if !id.Identity || !id.PrimaryKey {
		t.Errorf("id identity=%v primary=%v", id.Identity, id.PrimaryKey)
	}
	if id.Unique {
		t.Error("primary key index also set unique")
	}
	seq := column(t, got, "seq")
	if seq.Identity || seq.Ordinal != 2 {
		t.Errorf("seq = %+v, want plain integer at ordinal 2", seq)
	}
	email := column(t, got, "email")
	if !email.Unique {
		t.Error("email unique = false")
	}
	if got.Source.Table != "users" || got.Source.Database != "" {
		t.Errorf("source = %+v", got.Source)
	}
}

func TestTablesUnknownRowEstimate(t *testing.T) {
	// reltuples is -1, not 0, until the table has been analysed. Zero
	// would tell the table picker the table is empty.
	q := scriptQ{"pg_class": []Row{
		{"schema": "public", "name": "users", "approx_rows": "-1"},
		{"schema": "public", "name": "empty", "approx_rows": "0"},
	}}
	got, err := Tables(context.Background(), q, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ApproxRows != nil {
		t.Errorf("users rows = %d, want unknown", *got[0].ApproxRows)
	}
	if got[1].ApproxRows == nil || *got[1].ApproxRows != 0 {
		t.Errorf("empty rows = %v, want 0", got[1].ApproxRows)
	}
}

func TestBindLiterals(t *testing.T) {
	got, err := bindLiterals("SELECT * FROM pragma_table_info(?) WHERE note = 'a?b'", []any{"o'brien"})
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM pragma_table_info('o''brien') WHERE note = 'a?b'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

var fixedAt = time.Date(2026, 10, 12, 14, 3, 11, 0, time.UTC)

type scriptQ map[string][]Row

func (s scriptQ) Query(_ context.Context, query string, _ ...any) ([]Row, error) {
	// Longest needle wins. A constraint query joins the same catalog the
	// column query reads, so the first map key that happens to be a
	// substring would return the wrong rows.
	best := ""
	for needle := range s {
		if strings.Contains(query, needle) && len(needle) > len(best) {
			best = needle
		}
	}
	if best == "" {
		return nil, nil
	}
	return s[best], nil
}

type fakeQ struct{}

func (fakeQ) Query(context.Context, string, ...any) ([]Row, error) { return nil, nil }

func column(t *testing.T, s schema.Schema, name string) schema.Column {
	t.Helper()
	for _, c := range s.Columns {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("column %q not in %+v", name, s.Columns)
	return schema.Column{}
}
