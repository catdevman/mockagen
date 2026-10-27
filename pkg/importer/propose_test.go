package importer_test

import (
	"slices"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
)

func TestProposeMatches(t *testing.T) {
	sch := schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", SQLType: "integer", Identity: true},
			{Name: "email", SQLType: "varchar(255)"},
			{Name: "home_phone", SQLType: "varchar(32)", Nullable: true},
			{Name: "status", SQLType: "text", EnumValues: []string{"active", "suspended", "closed"}},
			{Name: "signed_up", SQLType: "timestamp"},
			{Name: "created_at", SQLType: "timestamp", Default: "now()"},
			{Name: "nickname", SQLType: "text", Nullable: true},
			{Name: "cat", SQLType: "text"},
			{Name: "car", SQLType: "text"},
		},
	}
	sample := importer.Sample{
		Format:    "csv",
		Delimiter: ",",
		HasHeader: true,
		Encoding:  "utf-8",
		Fields:    []string{"id", "email", "Home Phone", "statuz", "can", "signed_up"},
	}
	got := importer.Propose(sch, sample, nil)

	var targets []string
	byName := map[string]importer.Field{}
	for _, f := range got.Fields {
		targets = append(targets, f.Target)
		byName[f.Target] = f
	}
	if !slices.Equal(targets, []string{"email", "home_phone", "status", "signed_up"}) {
		t.Fatalf("targets = %v", targets)
	}
	if byName["email"].Source != "email" || len(byName["email"].Transforms) != 1 || byName["email"].Transforms[0].Name != "trim" {
		t.Errorf("email = %#v", byName["email"])
	}
	if byName["home_phone"].Source != "Home Phone" {
		t.Errorf("normalised source = %q", byName["home_phone"].Source)
	}
	status := byName["status"]
	if status.Source != "statuz" || len(status.Transforms) != 2 {
		t.Fatalf("status = %#v", status)
	}
	if status.Transforms[0].Name != "trim" || status.Transforms[1].Name != "map" {
		t.Fatalf("status transforms = %#v", status.Transforms)
	}
	wantAccept := []string{"active", "suspended", "closed"}
	if !slices.Equal(status.Transforms[1].Accept, wantAccept) || len(status.Transforms[1].Pairs) != 0 {
		t.Errorf("enum seed = %#v", status.Transforms[1])
	}
	if len(byName["signed_up"].Transforms) != 0 {
		t.Errorf("time chain = %#v", byName["signed_up"].Transforms)
	}
	if _, ok := byName["id"]; ok {
		t.Error("identity column was mapped")
	}
	if !slices.Equal(got.Omitted, []string{"id", "created_at", "nickname"}) {
		t.Errorf("omitted = %v", got.Omitted)
	}
	for _, name := range []string{"cat", "car"} {
		if slices.Contains(got.Omitted, name) {
			t.Errorf("%s was omitted; an ambiguous pair should stay unseen", name)
		}
		if _, ok := byName[name]; ok {
			t.Errorf("%s was mapped from an ambiguous source", name)
		}
	}
}

func TestProposeKeepsEditedChain(t *testing.T) {
	sch := schema.Schema{Columns: []schema.Column{
		{Name: "email", SQLType: "varchar(255)"},
		{Name: "title", SQLType: "varchar(80)"},
	}}
	base := &importer.FieldMap{
		Name: "kept",
		Fields: []importer.Field{{
			Target:     "email",
			Source:     "email",
			Transforms: []importer.Transform{{Name: "case", Case: "lower"}},
		}},
	}
	sample := importer.Sample{
		Format: "csv", Delimiter: ",", HasHeader: true, Encoding: "utf-8",
		Fields: []string{"email", "title"},
	}
	got := importer.Propose(sch, sample, base)
	if got.Name != "kept" {
		t.Errorf("name = %q", got.Name)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("fields = %#v", got.Fields)
	}
	if got.Fields[0].Target != "email" || len(got.Fields[0].Transforms) != 1 || got.Fields[0].Transforms[0].Name != "case" {
		t.Errorf("edited chain = %#v", got.Fields[0])
	}
	if got.Fields[1].Target != "title" || got.Fields[1].Source != "title" {
		t.Errorf("new field = %#v", got.Fields[1])
	}
}
