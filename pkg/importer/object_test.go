package importer_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

// designObjectYAML is the object origin from the design, plus a nested
// address so key order and an omitted empty object can be pinned
// together. status comes before email in a second case; this one keeps
// the design's order.
const designObjectYAML = `version: mockagen/v1
kind: FieldMap
name: profiles
schema: ./users.schema.yaml
source:
  format: csv
  has_header: true
  fields: [city, email, zip]
fields:
  - target: profile
    object:
      city:
        source: city
        transforms:
          - trim: {}
      email:
        source: email
      status:
        constant: active
      address:
        object:
          zip:
            source: zip
unmapped:
  source: ignore
on_error: abort
`

func profileSchema(sqlType string) schema.Schema {
	return schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "profile", SQLType: sqlType, Nullable: true},
		},
	}
}

func TestObjectOrigin(t *testing.T) {
	fm, err := importer.Unmarshal([]byte(designObjectYAML), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, diags := importer.Compile(profileSchema("jsonb"), fm)
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	if p.Target.Columns[0].Kind != importer.KindText || p.Target.Columns[0].SQLType != "jsonb" {
		t.Fatalf("column = %+v", p.Target.Columns[0])
	}

	apply := func(city, email, zip *string) (string, error) {
		t.Helper()
		src := importer.NewSliceSource([]string{"city", "email", "zip"}, [][]*string{{city, email, zip}})
		var sink importer.RecordSink
		_, err := importer.Run(context.Background(), src, p, &sink, importer.Options{})
		if err != nil {
			return "", err
		}
		if len(sink.Rows) != 1 {
			t.Fatalf("rows = %d", len(sink.Rows))
		}
		return string(sink.Rows[0].Bytes(0)), nil
	}

	got, err := apply(sp(" Austin "), sp("a@b.c"), sp("78701"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"city":"Austin","email":"a@b.c","status":"active","address":{"zip":"78701"}}`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}

	got, err = apply(sp("   "), sp("a@b.c"), sp(""))
	if err != nil {
		t.Fatal(err)
	}
	// A blank city is omitted, and an empty nested object is omitted
	// with it. The constant leaf stays.
	want = `{"email":"a@b.c","status":"active"}`
	if got != want {
		t.Fatalf("blank got %s\nwant %s", got, want)
	}

	got, err = apply(nil, sp("a@b.c"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("null got %s\nwant %s", got, want)
	}

	textPlan, diags := importer.Compile(profileSchema("nvarchar(max)"), fm)
	if len(diags) > 0 {
		t.Fatalf("text column: %v", diags)
	}
	src := importer.NewSliceSource([]string{"city", "email", "zip"}, [][]*string{{sp("Austin"), sp("a@b.c"), sp("78701")}})
	var sink importer.RecordSink
	if _, err := importer.Run(context.Background(), src, textPlan, &sink, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	if string(sink.Rows[0].Bytes(0)) != `{"city":"Austin","email":"a@b.c","status":"active","address":{"zip":"78701"}}` {
		t.Fatalf("nvarchar document %s", sink.Rows[0].Bytes(0))
	}
}

func TestObjectKeyOrder(t *testing.T) {
	// Alphabetical order would put city first. File order is the
	// document order, which is why Object is not a Go map.
	const raw = `version: mockagen/v1
kind: FieldMap
name: profiles
source:
  format: csv
  has_header: true
  fields: [zip, city]
fields:
  - target: profile
    object:
      zip:
        source: zip
      city:
        source: city
unmapped:
  source: ignore
on_error: abort
`
	fm, err := importer.Unmarshal([]byte(raw), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, diags := importer.Compile(profileSchema("json"), fm)
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	src := importer.NewSliceSource(fm.Source.Fields, [][]*string{{sp("78701"), sp("Austin")}})
	var sink importer.RecordSink
	if _, err := importer.Run(context.Background(), src, p, &sink, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	got := string(sink.Rows[0].Bytes(0))
	if got != `{"zip":"78701","city":"Austin"}` {
		t.Fatalf("got %s", got)
	}
}

func TestObjectBadConstant(t *testing.T) {
	sch := profileSchema("jsonb")
	badDoc := importer.FieldMap{
		Kind: "FieldMap",
		Source: importer.SourceSpec{
			HasHeader: true,
			Fields:    []string{"city"},
		},
		Fields: []importer.Field{{
			Target:   "profile",
			Constant: sp("{"),
		}},
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
	}
	_, diags := importer.Compile(sch, badDoc)
	if !hasDiag(diags, importer.ErrBadConstant) {
		t.Fatalf("diags = %v", diags)
	}

	badLeaf := importer.FieldMap{
		Kind: "FieldMap",
		Source: importer.SourceSpec{
			HasHeader: true,
			Fields:    []string{"city"},
		},
		Fields: []importer.Field{{
			Target: "profile",
			Object: &importer.Object{Keys: []importer.ObjectField{{
				Key:      "city",
				Constant: sp(string([]byte{0xff})),
			}}},
		}},
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
	}
	_, diags = importer.Compile(sch, badLeaf)
	if !hasDiag(diags, importer.ErrBadConstant) {
		t.Fatalf("leaf diags = %v", diags)
	}

	two := badLeaf
	two.Fields[0].Source = "city"
	_, diags = importer.Compile(sch, two)
	if !hasDiag(diags, importer.ErrTwoOrigins) {
		t.Fatalf("origins diags = %v", diags)
	}
}

func TestObjectBadRow(t *testing.T) {
	fm := importer.FieldMap{
		Kind: "FieldMap",
		Source: importer.SourceSpec{
			HasHeader: true,
			Fields:    []string{"code"},
		},
		Fields: []importer.Field{{
			Target: "profile",
			Object: &importer.Object{Keys: []importer.ObjectField{{
				Key:    "status",
				Source: "code",
				Transforms: []importer.Transform{{
					Name:  "map",
					Pairs: []importer.Pair{{From: "a", To: "active"}},
				}},
			}}},
		}},
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
		OnError:  "abort",
	}
	p, diags := importer.Compile(profileSchema("jsonb"), fm)
	if len(diags) > 0 {
		t.Fatalf("compile: %v", diags)
	}
	src := importer.NewSliceSource([]string{"code"}, [][]*string{{sp("z")}})
	var sink importer.RecordSink
	rep, err := importer.Run(context.Background(), src, p, &sink, importer.Options{OnError: importer.ErrorAbort})
	if !errors.Is(err, importer.ErrLookupMiss) {
		t.Fatalf("err = %v", err)
	}
	if rep == nil || len(rep.Errors) != 1 || rep.Errors[0].Target != "profile" || rep.Written != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if len(sink.Rows) != 0 {
		t.Fatalf("wrote %d rows", len(sink.Rows))
	}
}

func hasDiag(diags []importer.Diagnostic, target error) bool {
	for _, d := range diags {
		if errors.Is(d.Err, target) {
			return true
		}
	}
	return false
}

func TestObjectConstantOnTextColumn(t *testing.T) {
	// A text column may hold a document. The constant is still just
	// text there, so "{" is legal, where the same constant on jsonb
	// is rejected above.
	fm := importer.FieldMap{
		Kind: "FieldMap",
		Fields: []importer.Field{{
			Target:   "profile",
			Constant: sp("{"),
		}},
		Unmapped: importer.UnmappedPolicy{Source: "ignore"},
	}
	p, diags := importer.Compile(profileSchema("nvarchar(max)"), fm)
	if len(diags) > 0 {
		t.Fatalf("diags = %v", diags)
	}
	src := importer.NewSliceSource(nil, [][]*string{{}})
	var sink importer.RecordSink
	if _, err := importer.Run(context.Background(), src, p, &sink, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	if string(sink.Rows[0].Bytes(0)) != "{" {
		t.Fatalf("got %q", sink.Rows[0].Bytes(0))
	}
	if strings.Contains(p.Target.Columns[0].SQLType, "json") {
		t.Fatalf("sql type %s", p.Target.Columns[0].SQLType)
	}
}
