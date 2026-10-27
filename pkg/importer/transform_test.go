package importer_test

import (
	"errors"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

func strPtr(s string) *string { return &s }

func TestTransforms(t *testing.T) {
	text := schema.Column{Name: "c", SQLType: "text", Nullable: true}
	cases := []struct {
		name    string
		field   importer.Field
		in      []string
		want    string
		wantNil bool
		wantErr error
	}{
		{
			name:  "trim whitespace",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{{Name: "trim"}}},
			in:    []string{"  a  "},
			want:  "a",
		},
		{
			name:  "trim cutset",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{{Name: "trim", HasCutset: true, Cutset: "-."}}},
			in:    []string{"-a."},
			want:  "a",
		},
		{
			name:  "case upper lower title",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{{Name: "case", Case: "title"}}},
			in:    []string{"HELLO there"},
			want:  "Hello There",
		},
		{
			name: "replace then nothing missing",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{
				{Name: "replace", From: "-", To: ""},
			}},
			in:   []string{"a-b-a"},
			want: "aba",
		},
		{
			name: "regex",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{
				{Name: "regex_replace", Pattern: `\d+`, To: "#"},
			}},
			in:   []string{"a12b"},
			want: "a#b",
		},
		{
			name: "lookup hit and default",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{{
				Name:          "map",
				Pairs:         []importer.Pair{{From: "a", To: "active"}},
				LookupDefault: strPtr("closed"),
			}}},
			in:   []string{"z"},
			want: "closed",
		},
		{
			name: "lookup miss",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{{
				Name:  "map",
				Pairs: []importer.Pair{{From: "a", To: "active"}},
			}}},
			in:      []string{"z"},
			wantErr: importer.ErrLookupMiss,
		},
		{
			name: "pad left exact and longer",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{
				{Name: "pad_left", Width: 5, With: "0"},
			}},
			in:   []string{"42"},
			want: "00042",
		},
		{
			name: "pad right leaves longer input",
			field: importer.Field{Source: "a", Transforms: []importer.Transform{
				{Name: "pad_right", Width: 2, With: " "},
			}},
			in:   []string{"abcd"},
			want: "abcd",
		},
		{
			name: "null_if runs after trim",
			field: importer.Field{
				Source:     "a",
				Transforms: []importer.Transform{{Name: "trim"}},
				NullIf:     []string{"N/A"},
			},
			in:      []string{"  N/A  "},
			wantNil: true,
		},
		{
			name: "join skips empty parts",
			field: importer.Field{
				Sources:    []string{"a", "b"},
				Join:       strPtr(" "),
				Transforms: []importer.Transform{{Name: "trim"}},
			},
			in:   []string{"Ada", ""},
			want: "Ada",
		},
		{
			name: "coalesce skips null_if and keeps the rest",
			field: importer.Field{
				Coalesce:   []string{"nick", "first"},
				Transforms: []importer.Transform{{Name: "trim"}},
				NullIf:     []string{""},
			},
			in:   []string{"  ", "Ada"},
			want: "Ada",
		},
		{
			name: "empty coalesce candidate is a value when not in null_if",
			field: importer.Field{
				Coalesce: []string{"nick", "first"},
			},
			in:   []string{"", "Ada"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, isNull, err := importer.Apply(text, tc.field, tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if isNull != tc.wantNil {
				t.Fatalf("null = %v, want %v (value %q)", isNull, tc.wantNil, got)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyOrderCoercesAfterNullIf(t *testing.T) {
	col := schema.Column{Name: "signed_up", SQLType: "timestamp", Nullable: true}
	f := importer.Field{
		Source:     "SIGNUP_DT",
		Transforms: []importer.Transform{{Name: "trim"}},
		Format:     "01/02/2006",
		NullIf:     []string{"", "N/A"},
		Default:    strPtr("2006-01-02"),
	}
	got, isNull, err := importer.Apply(col, f, []string{"  01/02/2006 "})
	if err != nil {
		t.Fatal(err)
	}
	if isNull || got != "2006-01-02T00:00:00Z" {
		t.Fatalf("got %q null=%v", got, isNull)
	}
	got, isNull, err = importer.Apply(col, f, []string{"  N/A "})
	if err != nil {
		t.Fatal(err)
	}
	// The default is a column value, not a source date, so it is not read
	// with the CSV layout.
	if isNull || got != "2006-01-02T00:00:00Z" {
		t.Fatalf("default got %q null=%v", got, isNull)
	}
}

func TestNumericScale(t *testing.T) {
	// Scale comes from sql_type. numeric(12,0) stores no fraction, so
	// "1.5" is not a value of that column. numeric(12,2) keeps two
	// places and rejects a third. A bare numeric has no scale, and
	// stays a float so a fraction is not rejected for a limit the
	// schema never stated.
	field := importer.Field{Source: "n"}
	cases := []struct {
		name    string
		col     schema.Column
		in      string
		want    string
		wantErr bool
	}{
		{
			name:    "scale zero rejects a fraction",
			col:     schema.Column{SQLType: "numeric(12,0)"},
			in:      "1.5",
			wantErr: true,
		},
		{
			name: "scale two accepts 1.25",
			col:  schema.Column{SQLType: "numeric(12,2)"},
			in:   "1.25",
			want: "1.25",
		},
		{
			name:    "scale two rejects a third digit",
			col:     schema.Column{SQLType: "numeric(12,2)"},
			in:      "1.255",
			wantErr: true,
		},
		{
			name: "bare numeric stays float",
			col:  schema.Column{SQLType: "numeric"},
			in:   "1.5",
			want: "1.5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, isNull, err := importer.Apply(tc.col, field, []string{tc.in})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if isNull || got != tc.want {
				t.Errorf("got %q null=%v, want %q", got, isNull, tc.want)
			}
		})
	}
}

func TestCoerceTimeKeepsOffset(t *testing.T) {
	// The offset is part of the value. Converting to UTC would change
	// the clock stored in a timestamp without time zone.
	col := schema.Column{SQLType: "timestamp", Nullable: true}
	got, _, err := importer.Apply(col, importer.Field{Source: "t"}, []string{
		"2020-01-02T15:04:05-07:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "2020-01-02T15:04:05-07:00"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestChainTrimsBeforeCase(t *testing.T) {
	got, _, err := importer.Apply(schema.Column{SQLType: "text"}, importer.Field{
		Source: "a",
		Transforms: []importer.Transform{
			{Name: "trim"},
			{Name: "case", Case: "lower"},
		},
	}, []string{"  Ab "})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ab" {
		t.Errorf("got %q, want %q", got, "ab")
	}
}
