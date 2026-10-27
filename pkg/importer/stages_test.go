package importer_test

import (
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

// stageWant is inline. Err is text because the table cannot hold the
// fmt.Errorf values Stages returns and still compare them.
type stageWant struct {
	name  string
	value string
	null  bool
	err   string
}

func TestStages(t *testing.T) {
	max3 := int64(3)
	cases := []struct {
		name  string
		col   schema.Column
		field importer.Field
		in    []string
		want  []stageWant
	}{
		{
			name: "trim keeps the text",
			col:  schema.Column{Name: "name", SQLType: "varchar"},
			field: importer.Field{
				Source:     "name",
				Transforms: []importer.Transform{{Name: "trim"}},
			},
			in: []string{" ada "},
			want: []stageWant{
				{name: "chain", value: "ada"},
				{name: "null_if", value: "ada"},
				{name: "coerce", value: "ada"},
				{name: "default", value: "ada"},
				{name: "check", value: "ada"},
			},
		},
		{
			name: "null_if without a default stops",
			col:  schema.Column{Name: "name", SQLType: "varchar"},
			field: importer.Field{
				Source: "name",
				NullIf: []string{"none"},
			},
			in: []string{"none"},
			want: []stageWant{
				{name: "chain", value: "none"},
				{name: "null_if", null: true},
				{name: "default", null: true},
			},
		},
		{
			name: "null_if uses the default",
			col:  schema.Column{Name: "n", SQLType: "integer"},
			field: importer.Field{
				Source:  "n",
				NullIf:  []string{"none"},
				Default: strPtr("0"),
			},
			in: []string{"none"},
			want: []stageWant{
				{name: "chain", value: "none"},
				{name: "null_if", null: true},
				{name: "default", value: "0"},
				{name: "check", value: "0"},
			},
		},
		{
			name:  "bad integer stops on coerce",
			col:   schema.Column{Name: "n", SQLType: "integer"},
			field: importer.Field{Source: "n"},
			in:    []string{"nope"},
			want: []stageWant{
				{name: "chain", value: "nope"},
				{name: "null_if", value: "nope"},
				{name: "coerce", err: `strconv.ParseInt: parsing "nope": invalid syntax`},
			},
		},
		{
			name: "length fails on check",
			col: schema.Column{
				Name:      "name",
				SQLType:   "varchar",
				MaxLength: &max3,
			},
			field: importer.Field{Source: "name"},
			in:    []string{"abcd"},
			want: []stageWant{
				{name: "chain", value: "abcd"},
				{name: "null_if", value: "abcd"},
				{name: "coerce", value: "abcd"},
				{name: "default", value: "abcd"},
				{name: "check", value: "abcd", err: "length 4 exceeds 3"},
			},
		},
		{
			name: "constant is trimmed like a cell",
			col:  schema.Column{Name: "name", SQLType: "varchar"},
			field: importer.Field{
				Constant:   strPtr(" ada "),
				Transforms: []importer.Transform{{Name: "trim"}},
			},
			want: []stageWant{
				{name: "chain", value: "ada"},
				{name: "null_if", value: "ada"},
				{name: "coerce", value: "ada"},
				{name: "default", value: "ada"},
				{name: "check", value: "ada"},
			},
		},
		{
			name: "coalesce keeps the first live candidate",
			col:  schema.Column{Name: "name", SQLType: "varchar"},
			field: importer.Field{
				Coalesce:   []string{"nick", "first"},
				Transforms: []importer.Transform{{Name: "trim"}},
				NullIf:     []string{"none"},
			},
			in: []string{"none", " ada "},
			want: []stageWant{
				{name: "chain", value: "ada"},
				{name: "null_if", value: "ada"},
				{name: "coerce", value: "ada"},
				{name: "default", value: "ada"},
				{name: "check", value: "ada"},
			},
		},
		{
			name: "coalesce all null stops like finishNull",
			col:  schema.Column{Name: "name", SQLType: "varchar"},
			field: importer.Field{
				Coalesce: []string{"nick", "first"},
				NullIf:   []string{"none"},
			},
			in: []string{"none", "none"},
			want: []stageWant{
				{name: "chain", value: "none"},
				{name: "null_if", null: true},
				{name: "default", null: true},
			},
		},
		{
			name: "object reduce error is the chain",
			col:  schema.Column{Name: "doc", SQLType: "json"},
			field: importer.Field{
				Object: &importer.Object{Keys: []importer.ObjectField{{
					Key: "city", Source: "city",
				}}},
			},
			want: []stageWant{
				{name: "chain", err: "importer: object leaf is missing a source value"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := importer.Stages(tc.col, tc.field, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %#v, want %d stages", got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Name != w.name {
					t.Errorf("name = %q, want %q", g.Name, w.name)
				}
				if g.Value != w.value {
					t.Errorf("value = %q, want %q", g.Value, w.value)
				}
				if g.Null != w.null {
					t.Errorf("null = %v, want %v", g.Null, w.null)
				}
				switch {
				case w.err == "" && g.Err != nil:
					t.Errorf("err = %v, want nil", g.Err)
				case w.err != "" && g.Err == nil:
					t.Errorf("err = nil, want %q", w.err)
				case w.err != "" && g.Err.Error() != w.err:
					t.Errorf("err = %q, want %q", g.Err.Error(), w.err)
				}
			}
		})
	}
}
