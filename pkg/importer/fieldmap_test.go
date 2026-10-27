package importer_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
)

// designFieldMapYAML is the field map from docs/design/import-tooling.md,
// without comments. yaml.v3 drops comments, so the pin is the decoded
// structure rather than the bytes.
const designFieldMapYAML = `version: mockagen/v1
kind: FieldMap
name: legacy-users-import
schema: ./users.schema.yaml
source:
  format: csv
  has_header: true
  delimiter: ","
  encoding: utf-8
  fields: [USER_GUID, EMAIL_ADDR, STATUS_CODE, SIGNUP_DT, FIRST, LAST, NICKNAME]
fields:
  - target: id
    source: USER_GUID
    transforms:
      - trim: {}
      - case: lower
  - target: email
    source: EMAIL_ADDR
    transforms:
      - trim: {}
      - case: lower
  - target: status
    source: STATUS_CODE
    transforms:
      - trim: {}
      - case: lower
      - map: {a: active, s: suspended, c: closed}
    default: active
  - target: signed_up
    source: SIGNUP_DT
    format: "01/02/2006"
    null_if: ["", "N/A", "NULL"]
  - target: full_name
    sources: [FIRST, LAST]
    join: " "
  - target: first_name
    coalesce: [NICKNAME, FIRST]
  - target: imported_batch
    constant: "2026-10-legacy"
omitted:
  - login_count
unmapped:
  source: ignore
on_error: abort
`

func TestUnmarshalDesignFieldMap(t *testing.T) {
	m, err := importer.Unmarshal([]byte(designFieldMapYAML), ".yaml")
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.Kind != "FieldMap" || m.Name != "legacy-users-import" {
		t.Fatalf("kind %q name %q", m.Kind, m.Name)
	}
	if m.Schema != "./users.schema.yaml" {
		t.Errorf("schema = %q", m.Schema)
	}
	if !slices.Equal(m.Source.Fields, []string{"USER_GUID", "EMAIL_ADDR", "STATUS_CODE", "SIGNUP_DT", "FIRST", "LAST", "NICKNAME"}) {
		t.Errorf("fields = %#v", m.Source.Fields)
	}
	if len(m.Fields) != 7 {
		t.Fatalf("fields len = %d", len(m.Fields))
	}
	status := m.Fields[2]
	if status.Default == nil || *status.Default != "active" {
		t.Fatalf("status default = %v", status.Default)
	}
	if len(status.Transforms) != 3 || status.Transforms[2].Name != "map" {
		t.Fatalf("status transforms = %#v", status.Transforms)
	}
	pairs := status.Transforms[2].Pairs
	wantPairs := []importer.Pair{{From: "a", To: "active"}, {From: "s", To: "suspended"}, {From: "c", To: "closed"}}
	if !slices.Equal(pairs, wantPairs) {
		t.Errorf("pairs = %#v, want %#v", pairs, wantPairs)
	}
	signed := m.Fields[3]
	if signed.Format != "01/02/2006" || !slices.Equal(signed.NullIf, []string{"", "N/A", "NULL"}) {
		t.Errorf("signed_up = %#v", signed)
	}
	full := m.Fields[4]
	if full.Join == nil || *full.Join != " " || !slices.Equal(full.Sources, []string{"FIRST", "LAST"}) {
		t.Errorf("full_name = %#v", full)
	}
	first := m.Fields[5]
	if !slices.Equal(first.Coalesce, []string{"NICKNAME", "FIRST"}) {
		t.Errorf("coalesce = %#v", first.Coalesce)
	}
	batch := m.Fields[6]
	if batch.Constant == nil || *batch.Constant != "2026-10-legacy" {
		t.Errorf("constant = %v", batch.Constant)
	}
	if !slices.Equal(m.Omitted, []string{"login_count"}) {
		t.Errorf("omitted = %#v", m.Omitted)
	}

	raw, err := importer.Marshal(m, ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	again, err := importer.Unmarshal(raw, ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	againRaw, err := importer.Marshal(again, ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(againRaw) {
		t.Errorf("round trip changed YAML\nfirst:\n%s\nsecond:\n%s", raw, againRaw)
	}
}

func TestUnmarshalRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		ext  string
		want error
	}{
		{
			name: "spec kind",
			yaml: "kind: Spec\nname: x\n",
			ext:  ".yaml",
			want: importer.ErrUnknownKind,
		},
		{
			name: "expr is reserved",
			yaml: "kind: FieldMap\nfields:\n  - target: a\n    source: b\n    transforms:\n      - expr: \"1\"\n",
			ext:  ".yaml",
			want: importer.ErrExprReserved,
		},
		{
			name: "unknown transform",
			yaml: "kind: FieldMap\nfields:\n  - target: a\n    source: b\n    transforms:\n      - shuffle: {}\n",
			ext:  ".yaml",
			want: importer.ErrUnknownTransform,
		},
		{
			name: "toml extension",
			yaml: "kind: FieldMap\n",
			ext:  ".toml",
			want: importer.ErrUnsupportedFormat,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := importer.Unmarshal([]byte(tc.yaml), tc.ext)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnmarshalRejectsRequired(t *testing.T) {
	// required was removed. KnownFields must reject the key; accepting
	// it would bring back a flag the schema column already decides.
	raw := "kind: FieldMap\nfields:\n  - target: a\n    source: b\n    required: true\n"
	_, err := importer.Unmarshal([]byte(raw), ".yaml")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, importer.ErrUnknownKind) {
		t.Fatalf("kind error = %v", err)
	}
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %v, want the field name", err)
	}
}

func TestResolveSchema(t *testing.T) {
	got := importer.ResolveSchema("/work/maps/users.map.yaml", "./users.schema.yaml")
	want := "/work/maps/users.schema.yaml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	abs := importer.ResolveSchema("/work/maps/users.map.yaml", "/abs/users.schema.yaml")
	if abs != "/abs/users.schema.yaml" {
		t.Errorf("absolute = %q", abs)
	}
}

func TestLookupJSONKeepsPairOrder(t *testing.T) {
	in := []byte(`{"map":{"a":"active","s":"suspended","default":"active"}}`)
	var tr importer.Transform
	if err := tr.UnmarshalJSON(in); err != nil {
		t.Fatal(err)
	}
	want := []importer.Pair{{From: "a", To: "active"}, {From: "s", To: "suspended"}}
	if !slices.Equal(tr.Pairs, want) {
		t.Fatalf("pairs = %#v", tr.Pairs)
	}
	if tr.LookupDefault == nil || *tr.LookupDefault != "active" {
		t.Fatalf("default = %v", tr.LookupDefault)
	}
	out, err := tr.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var again importer.Transform
	if err := again.UnmarshalJSON(out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.Pairs, want) {
		t.Fatalf("round trip pairs = %#v", again.Pairs)
	}
}
