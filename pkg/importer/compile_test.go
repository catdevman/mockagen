package importer_test

import (
	"errors"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

func i64(n int64) *int64 { return &n }

func testSchema() schema.Schema {
	return schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", SQLType: "integer", Identity: true},
			{Name: "email", SQLType: "varchar(255)", MaxLength: i64(255)},
			{Name: "status", SQLType: "user_status", EnumValues: []string{"active", "closed"}},
			{Name: "signed_up", SQLType: "timestamp"},
			{Name: "login_count", SQLType: "integer", Nullable: true},
			{Name: "created_at", SQLType: "timestamp", Default: "now()"},
		},
	}
}

func header(names ...string) importer.SourceSpec {
	return importer.SourceSpec{Format: "csv", HasHeader: true, Fields: names}
}

func TestCompileDiagnostics(t *testing.T) {
	sch := testSchema()
	cases := []struct {
		name   string
		field  importer.Field
		omit   []string
		source importer.SourceSpec
		want   error
		column string
	}{
		{
			name:   "unknown transform",
			field:  importer.Field{Target: "email", Source: "email", Transforms: []importer.Transform{{Name: "shuffle"}}},
			source: header("email"),
			want:   importer.ErrUnknownTransform,
			column: "email",
		},
		{
			name:   "bad regex",
			field:  importer.Field{Target: "email", Source: "email", Transforms: []importer.Transform{{Name: "regex_replace", Pattern: "("}}},
			source: header("email"),
			want:   importer.ErrBadRegex,
			column: "email",
		},
		{
			name:   "two origins",
			field:  importer.Field{Target: "email", Source: "email", Constant: strPtr("a")},
			source: header("email"),
			want:   importer.ErrTwoOrigins,
			column: "email",
		},
		{
			name:   "sources without join",
			field:  importer.Field{Target: "email", Sources: []string{"email", "email"}},
			source: header("email"),
			want:   importer.ErrSourcesNeedJoin,
			column: "email",
		},
		{
			name:   "format on a text column",
			field:  importer.Field{Target: "email", Source: "email", Format: "01/02/2006"},
			source: header("email"),
			want:   importer.ErrFormatNotTime,
			column: "email",
		},
		{
			name:   "constant does not fit",
			field:  importer.Field{Target: "email", Constant: strPtr("nope-nope"), Transforms: []importer.Transform{{Name: "trim"}}},
			source: header(),
			want:   importer.ErrBadConstant,
			column: "email",
		},
		{
			name:   "identity target",
			field:  importer.Field{Target: "id", Source: "id"},
			source: header("id"),
			want:   importer.ErrIdentityTarget,
			column: "id",
		},
		{
			name:   "blocker",
			field:  importer.Field{Target: "login_count", Source: "login_count"},
			source: header("login_count"),
			want:   importer.ErrBlocker,
			column: "email",
		},
		{
			name:   "stale field",
			field:  importer.Field{Target: "gone", Source: "email"},
			source: header("email"),
			want:   importer.ErrStale,
			column: "gone",
		},
		{
			name: "unfinished lookup",
			field: importer.Field{Target: "status", Source: "status", Transforms: []importer.Transform{{
				Name:   "map",
				Accept: []string{"active", "closed"},
			}}},
			source: header("status"),
			want:   importer.ErrUnfinishedLookup,
			column: "status",
		},
		{
			name:   "omitted required column",
			omit:   []string{"email"},
			source: header(),
			want:   importer.ErrBadOmitted,
			column: "email",
		},
		{
			name:   "duplicate target",
			field:  importer.Field{Target: "email", Source: "email"},
			source: header("email"),
			want:   importer.ErrDuplicateTarget,
			column: "email",
		},
		{
			name:   "unknown source",
			field:  importer.Field{Target: "email", Source: "e-mail"},
			source: header("email"),
			want:   importer.ErrUnknownSource,
			column: "email",
		},
		{
			name: "unknown case",
			field: importer.Field{Target: "email", Source: "email", Transforms: []importer.Transform{{
				Name: "case", Case: "shout",
			}}},
			source: header("email"),
			want:   importer.ErrUnknownCase,
			column: "email",
		},
		{
			name: "empty replace",
			field: importer.Field{Target: "email", Source: "email", Transforms: []importer.Transform{{
				Name: "replace",
			}}},
			source: header("email"),
			want:   importer.ErrEmptyReplace,
			column: "email",
		},
		{
			name: "bad pad",
			field: importer.Field{Target: "email", Source: "email", Transforms: []importer.Transform{{
				Name: "pad_left", Width: 0, With: "0",
			}}},
			source: header("email"),
			want:   importer.ErrBadPad,
			column: "email",
		},
		{
			name:   "unseen nullable",
			field:  importer.Field{Target: "email", Source: "email", Transforms: []importer.Transform{{Name: "trim"}}},
			source: header("email"),
			want:   importer.ErrUnseen,
			column: "login_count",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := importer.FieldMap{Source: tc.source, Omitted: tc.omit}
			if tc.field.Target != "" {
				m.Fields = []importer.Field{tc.field}
			}
			if tc.name == "duplicate target" {
				m.Fields = append(m.Fields, tc.field)
			}
			if tc.name == "constant does not fit" {
				// varchar(255) would accept this constant. Force the length.
				sch := testSchema()
				sch.Columns[1].MaxLength = i64(3)
				assertDiag(t, sch, m, tc.column, tc.want)
				return
			}
			assertDiag(t, sch, m, tc.column, tc.want)
		})
	}
}

func assertDiag(t *testing.T, sch schema.Schema, m importer.FieldMap, column string, want error) {
	t.Helper()
	_, diags := importer.Compile(sch, m)
	for _, d := range diags {
		if d.Column == column && errors.Is(d.Err, want) {
			return
		}
	}
	t.Fatalf("diagnostics = %v, want %v on %q", diags, want, column)
}

func TestCompileIgnoresGeneratorType(t *testing.T) {
	// The column's format and generator type are generation decisions.
	// Coercion uses sql_type, so an integer column accepts "1" even when
	// those fields describe a date.
	sch := schema.Schema{Columns: []schema.Column{{
		Name:    "n",
		SQLType: "integer",
		Type:    "Datetime",
		Format:  "01/02/2006",
	}}}
	m := importer.FieldMap{Fields: []importer.Field{{
		Target:   "n",
		Constant: strPtr("1"),
	}}}
	plan, diags := importer.Compile(sch, m)
	if len(diags) != 0 {
		t.Fatalf("diags = %v", diags)
	}
	if len(plan.Write) != 1 || plan.Write[0].Column.Name != "n" {
		t.Fatalf("write = %#v", plan.Write)
	}
}

func TestPlanSkipsOmittedAndIdentity(t *testing.T) {
	sch := testSchema()
	m := importer.FieldMap{
		Source: header("email", "status"),
		Fields: []importer.Field{
			{Target: "email", Source: "email", Transforms: []importer.Transform{{Name: "trim"}}},
			{Target: "status", Constant: strPtr("active")},
		},
		Omitted: []string{"login_count", "created_at", "id", "signed_up"},
	}
	// signed_up is NOT NULL with no default, so omitting it is illegal.
	// Use a nullable stand-in by adjusting the schema copy.
	sch.Columns[3].Nullable = true
	plan, diags := importer.Compile(sch, m)
	if len(diags) != 0 {
		t.Fatalf("diags = %v", diags)
	}
	var names []string
	for _, w := range plan.Write {
		names = append(names, w.Column.Name)
	}
	if len(names) != 2 || names[0] != "email" || names[1] != "status" {
		t.Fatalf("write = %v", names)
	}
}
