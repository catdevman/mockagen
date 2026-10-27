package schema_test

import (
	"slices"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen/types"
	"github.com/catdevman/mockagen/pkg/schema"
)

// TestInferRules drives every inference rule and the two the catalog
// cannot honour. A type string that is not in the catalog does not fail
// on its own: generation looks it up by exact text and fills the column
// with random strings. The lookup in this test is that guard.
func TestInferRules(t *testing.T) {
	enums := []string{"active", "suspended", "closed"}
	cases := []struct {
		name       string
		col        schema.Column
		wantType   string
		wantID     string
		wantValues []string
	}{
		{
			name:     "uuid primary key is a text key",
			col:      schema.Column{Name: "id", SQLType: "uuid", PrimaryKey: true},
			wantType: "GUID",
			wantID:   "key:text-pk",
		},
		{
			name:     "unique email is a key before it is a name",
			col:      schema.Column{Name: "email", SQLType: "varchar(255)", Unique: true},
			wantType: "GUID",
			wantID:   "key:text-pk",
		},
		{
			name:     "email",
			col:      schema.Column{Name: "email", SQLType: "varchar(255)"},
			wantType: "Email Address",
			wantID:   "name:email",
		},
		{
			name:     "email_address",
			col:      schema.Column{Name: "Email_Address", SQLType: "text"},
			wantType: "Email Address",
			wantID:   "name:email",
		},
		{
			name:     "suffix _email",
			col:      schema.Column{Name: "user_email", SQLType: "text"},
			wantType: "Email Address",
			wantID:   "name:email",
		},
		{
			name:     "notemail is not an email",
			col:      schema.Column{Name: "notemail", SQLType: "text"},
			wantType: "Sentence",
			wantID:   "type:text",
		},
		{
			name:     "first_name",
			col:      schema.Column{Name: "first_name", SQLType: "text"},
			wantType: "First Name",
			wantID:   "name:first_name",
		},
		{
			name:     "fname",
			col:      schema.Column{Name: "fname", SQLType: "varchar(40)"},
			wantType: "First Name",
			wantID:   "name:first_name",
		},
		{
			name:     "given_name",
			col:      schema.Column{Name: "given_name", SQLType: "character varying(40)"},
			wantType: "First Name",
			wantID:   "name:first_name",
		},
		{
			name:     "created_at",
			col:      schema.Column{Name: "created_at", SQLType: "text"},
			wantType: "Datetime",
			wantID:   "name:*_at",
		},
		{
			name:     "signed_up_at prefers the name over the type",
			col:      schema.Column{Name: "signed_up_at", SQLType: "timestamp with time zone"},
			wantType: "Datetime",
			wantID:   "name:*_at",
		},
		{
			name: "integer primary key stays unresolved",
			col:  schema.Column{Name: "id", SQLType: "integer", PrimaryKey: true, Type: "Sentence", InferredBy: "type:text"},
		},
		{
			name: "integer primary key does not fall through to its name",
			col:  schema.Column{Name: "created_at", SQLType: "bigint", PrimaryKey: true},
		},
		{
			name: "plain integer stays unresolved",
			col:  schema.Column{Name: "login_count", SQLType: "integer", Nullable: true},
		},
		{
			name:       "enum copies the catalog labels",
			col:        schema.Column{Name: "status", SQLType: "user_status", EnumValues: enums},
			wantType:   "Custom List",
			wantID:     "rule:enum",
			wantValues: enums,
		},
		{
			name:       "enum beats a name",
			col:        schema.Column{Name: "email", SQLType: "user_status", EnumValues: enums},
			wantType:   "Custom List",
			wantID:     "rule:enum",
			wantValues: enums,
		},
		{
			name:     "boolean",
			col:      schema.Column{Name: "active", SQLType: "boolean"},
			wantType: "Boolean",
			wantID:   "type:bool",
		},
		{
			name:     "bit",
			col:      schema.Column{Name: "active", SQLType: "bit"},
			wantType: "Boolean",
			wantID:   "type:bool",
		},
		{
			name:     "tinyint(1)",
			col:      schema.Column{Name: "active", SQLType: "tinyint(1)"},
			wantType: "Boolean",
			wantID:   "type:bool",
		},
		{
			name: "tinyint(1) primary key is an integer key",
			col:  schema.Column{Name: "id", SQLType: "tinyint(1)", PrimaryKey: true},
		},
		{
			name: "tinyint without the width is not a bool",
			col:  schema.Column{Name: "n", SQLType: "tinyint"},
		},
		{
			name:     "timestamp",
			col:      schema.Column{Name: "when", SQLType: "timestamp"},
			wantType: "Datetime",
			wantID:   "type:timestamp",
		},
		{
			name:     "datetime2",
			col:      schema.Column{Name: "when", SQLType: "datetime2(7)"},
			wantType: "Datetime",
			wantID:   "type:timestamp",
		},
		{
			name:     "date",
			col:      schema.Column{Name: "born", SQLType: "date"},
			wantType: "Datetime",
			wantID:   "type:timestamp",
		},
		{
			name: "time is not a timestamp rule",
			col:  schema.Column{Name: "born", SQLType: "time"},
		},
		{
			name: "numeric stays unresolved",
			col:  schema.Column{Name: "balance", SQLType: "numeric(12,2)"},
		},
		{
			name: "decimal is the same unresolved rule",
			col:  schema.Column{Name: "balance", SQLType: "decimal(12,2)"},
		},
		{
			name:     "numeric named created_at still follows the name",
			col:      schema.Column{Name: "created_at", SQLType: "numeric(12,2)"},
			wantType: "Datetime",
			wantID:   "name:*_at",
		},
		{
			name: "float matches no rule",
			col:  schema.Column{Name: "ratio", SQLType: "float"},
		},
		{
			name:     "text",
			col:      schema.Column{Name: "note", SQLType: "text"},
			wantType: "Sentence",
			wantID:   "type:text",
		},
		{
			name:     "character varying",
			col:      schema.Column{Name: "note", SQLType: "character varying(255)"},
			wantType: "Sentence",
			wantID:   "type:text",
		},
		{
			name:     "character",
			col:      schema.Column{Name: "code", SQLType: "character(2)"},
			wantType: "Sentence",
			wantID:   "type:text",
		},
		{
			name:     "nvarchar",
			col:      schema.Column{Name: "note", SQLType: "nvarchar(max)"},
			wantType: "Sentence",
			wantID:   "type:text",
		},
		{
			name:     "nchar",
			col:      schema.Column{Name: "code", SQLType: "nchar(2)"},
			wantType: "Sentence",
			wantID:   "type:text",
		},
		{
			name: "jsonb email is a document",
			col:  schema.Column{Name: "email", SQLType: "jsonb"},
		},
		{
			name: "json",
			col:  schema.Column{Name: "payload", SQLType: "json"},
		},
		{
			name:     "jsonb array is not a document",
			col:      schema.Column{Name: "email", SQLType: "jsonb[]"},
			wantType: "Email Address",
			wantID:   "name:email",
		},
		{
			name:     "unique uuid",
			col:      schema.Column{Name: "token", SQLType: "uuid", Unique: true},
			wantType: "GUID",
			wantID:   "key:text-pk",
		},
		{
			name:     "bare uuid",
			col:      schema.Column{Name: "token", SQLType: "uuid"},
			wantType: "GUID",
			wantID:   "type:uuid",
		},
		{
			name:     "uniqueidentifier",
			col:      schema.Column{Name: "token", SQLType: "uniqueidentifier"},
			wantType: "GUID",
			wantID:   "type:uuid",
		},
		{
			name: "bit varying is not a flag",
			col:  schema.Column{Name: "mask", SQLType: "bit varying"},
		},
		{
			name:       "user decision is kept",
			col:        schema.Column{Name: "email", SQLType: "varchar(255)", Type: "Word", InferredBy: "user", Values: []string{"a"}},
			wantType:   "Word",
			wantID:     "user",
			wantValues: []string{"a"},
		},
		{
			name:     "a previous guess is replaced",
			col:      schema.Column{Name: "email", SQLType: "varchar(255)", Type: "Sentence", InferredBy: "type:text", Values: []string{"stale"}},
			wantType: "Email Address",
			wantID:   "name:email",
		},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := schema.Infer(schema.Schema{Columns: []schema.Column{tc.col}})
			if len(got.Columns) != 1 {
				t.Fatalf("columns = %d, want 1", len(got.Columns))
			}
			col := got.Columns[0]
			if col.Type != tc.wantType || col.InferredBy != tc.wantID {
				t.Fatalf("type %q by %q, want %q by %q", col.Type, col.InferredBy, tc.wantType, tc.wantID)
			}
			if !slices.Equal(col.Values, tc.wantValues) {
				t.Fatalf("values %#v, want %#v", col.Values, tc.wantValues)
			}
			if tc.wantType != "" {
				if _, ok := types.Lookup(tc.wantType); !ok {
					t.Fatalf("type %q is not in the catalog", tc.wantType)
				}
			}
		})
		if tc.wantID != "" && tc.wantID != "user" {
			if prev, ok := seen[tc.wantID]; ok && prev != tc.wantType {
				t.Errorf("rule %s emitted %q and %q", tc.wantID, prev, tc.wantType)
			}
			seen[tc.wantID] = tc.wantType
		}
	}
	for _, id := range []string{
		"key:text-pk", "rule:enum", "name:email", "name:first_name",
		"name:*_at", "type:uuid", "type:bool", "type:timestamp", "type:text",
	} {
		if _, ok := seen[id]; !ok {
			t.Errorf("no case emitted rule %s", id)
		}
	}
}

// TestInferJSONDocumentKeepsSQLType pins the exemption the name rules
// would otherwise undo. A jsonb column called email is not an email
// address, and the sql_type has to stay jsonb so a field map can still
// tell a document from text.
func TestInferJSONDocumentKeepsSQLType(t *testing.T) {
	in := schema.Schema{Columns: []schema.Column{{
		Name: "email", SQLType: "jsonb",
	}}}
	got := schema.Infer(in)
	if len(got.Columns) != 1 {
		t.Fatalf("columns = %d", len(got.Columns))
	}
	col := got.Columns[0]
	if col.SQLType != "jsonb" || col.Type != "" || col.InferredBy != "" {
		t.Fatalf("column = %+v", col)
	}
}

// TestInferDoesNotAlias clones the value list. The caller's enum slice
// is a fact, and editing the decision must not change it.
func TestInferDoesNotAlias(t *testing.T) {
	enums := []string{"active", "closed"}
	in := schema.Schema{Columns: []schema.Column{{
		Name: "status", SQLType: "user_status", EnumValues: enums,
	}}}
	got := schema.Infer(in)
	got.Columns[0].Values[0] = "changed"
	if enums[0] != "active" {
		t.Fatalf("enum fact changed to %q", enums[0])
	}
	if in.Columns[0].Type != "" {
		t.Fatalf("input type = %q, want empty", in.Columns[0].Type)
	}
}

func TestUpdatePreservesUserAndReportsDrift(t *testing.T) {
	prev := schema.Schema{Columns: []schema.Column{
		{Name: "email", SQLType: "varchar(50)", Type: "Word", InferredBy: "user", Min: "1"},
		{Name: "label", SQLType: "varchar(20)", Type: "Sentence", InferredBy: "type:text"},
		{Name: "gone", SQLType: "text", Type: "Sentence", InferredBy: "type:text"},
	}}
	fresh := schema.Schema{Columns: []schema.Column{
		{Name: "email", SQLType: "varchar(50)", Nullable: true},
		{Name: "label", SQLType: "varchar(100)"},
		{Name: "extra", SQLType: "text"},
	}}
	got, drift := schema.Update(prev, fresh)
	if len(got.Columns) != 3 {
		t.Fatalf("columns = %d, want 3", len(got.Columns))
	}
	email := got.Columns[0]
	if email.Type != "Word" || email.InferredBy != "user" || email.Min != "1" {
		t.Fatalf("email = %+v, want the user decision", email)
	}
	if email.SQLType != "varchar(50)" || !email.Nullable {
		t.Fatalf("email facts = type %q nullable %v", email.SQLType, email.Nullable)
	}
	label := got.Columns[1]
	if label.SQLType != "varchar(100)" || label.Type != "Sentence" || label.InferredBy != "type:text" {
		t.Fatalf("label = %+v", label)
	}
	extra := got.Columns[2]
	if extra.Type != "Sentence" || extra.InferredBy != "type:text" {
		t.Fatalf("extra = %+v", extra)
	}
	wantDrift := []string{
		`column "label" sql_type "varchar(20)" is now "varchar(100)"`,
		`column "extra" added`,
		`column "gone" dropped`,
	}
	if !slices.Equal(drift, wantDrift) {
		t.Fatalf("drift = %#v, want %#v", drift, wantDrift)
	}
}
