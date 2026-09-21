package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
	yaml "gopkg.in/yaml.v3"
)

func TestFluentWriterLineShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newFluentWriter(f, "mockagen.test")
	type rec struct {
		User string `json:"user"`
		IP   string `json:"ip"`
	}
	for _, r := range []rec{{"alice", "10.0.0.1"}, {"bob", "10.0.0.2"}} {
		if err := w.WriteRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), b)
	}
	for i, line := range lines {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("line %d: got %d tab-separated fields, want 3: %q", i, len(parts), line)
		}
		if _, err := time.Parse(time.RFC3339, parts[0]); err != nil {
			t.Errorf("line %d: timestamp %q: %v", i, parts[0], err)
		}
		if parts[1] != "mockagen.test" {
			t.Errorf("line %d: tag = %q, want %q", i, parts[1], "mockagen.test")
		}
		var got rec
		if err := json.Unmarshal([]byte(parts[2]), &got); err != nil {
			t.Errorf("line %d: record %q: %v", i, parts[2], err)
		}
	}
}

func TestFluentTag(t *testing.T) {
	cases := []struct {
		name   string
		config mockagen.MockagenConfig
		want   string
	}{
		{"explicit tag wins", mockagen.MockagenConfig{Name: "web logs", Tag: "app.access"}, "app.access"},
		{"blank tag falls back to name", mockagen.MockagenConfig{Name: "web logs", Tag: "  "}, "mockagen.web.logs"},
		{"derived from name", mockagen.MockagenConfig{Name: "Web Logs"}, "mockagen.web.logs"},
		{"no name at all", mockagen.MockagenConfig{}, "mockagen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fluentTag(tc.config); got != tc.want {
				t.Errorf("fluentTag() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutputExt(t *testing.T) {
	for format, want := range map[string]string{
		"json":    "json",
		"yaml":    "yaml",
		"fixed":   "fixed",
		"parquet": "parquet",
		"fluent":  "log",
	} {
		if got := outputExt(format); got != want {
			t.Errorf("outputExt(%q) = %q, want %q", format, got, want)
		}
	}
}

// yamlRecordType builds the same kind of record the generator does: a
// runtime struct whose fields are all strings, tagged with the column name.
func yamlRecordType(cols []string) ([]reflect.StructField, reflect.Type) {
	fields := make([]reflect.StructField, 0, len(cols))
	for i, name := range cols {
		fields = append(fields, reflect.StructField{
			Name: structFieldName(name, i),
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(fmt.Sprintf("yaml:%q", name)),
		})
	}
	return fields, reflect.StructOf(fields)
}

// TestYAMLSeqWriterRoundTrip is the safety net for the hand-rolled emitter:
// whatever it writes must parse back as exactly the strings that went in,
// including values YAML would otherwise resolve to a bool, a number or null.
func TestYAMLSeqWriterRoundTrip(t *testing.T) {
	cols := []string{"plain", "tricky", "text"}
	structArr, recType := yamlRecordType(cols)

	tricky := []string{
		"", "true", "False", "NULL", "~", "y", "no", "on", "off",
		"123", "0x1f", "1e5", "-7", "0.5", "1:30", "2019-12-24",
		" leading", "trailing ", "  ", "a: b", "x #c", "#comment",
		"- dash", "*alias", "&anchor", "!tag", "[bracket", "{brace",
		"|pipe", ">gt", "%pct", "@at", "`tick", "'single'", `d"quote`,
		"back\\slash", "multi\nline", "tab\there", "ret\rurn",
		"日本語", "emoji 🎉", "nul\x00byte", "colon:", ":", "--",
	}

	path := filepath.Join(t.TempDir(), "out.yaml")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := newYAMLSeqWriter(f, structArr)
	if w.keys == nil {
		t.Fatal("expected the fast path to be selected for an all-string struct")
	}
	want := make([]map[string]string, 0, len(tricky))
	for _, v := range tricky {
		rec := reflect.New(recType).Elem()
		rec.Field(0).SetString("plainvalue")
		rec.Field(1).SetString(v)
		rec.Field(2).SetString("Cum dolorem sed aliquid fugit aut, quo " +
			"voluptas nulla eveniet assumenda longer than eighty columns.")
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatalf("value %q: %v", v, err)
		}
		want = append(want, map[string]string{
			"plain":  rec.Field(0).String(),
			"tricky": v,
			"text":   rec.Field(2).String(),
		})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]string
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not valid YAML mapping to strings: %v\n%s", err, b)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		for _, k := range cols {
			if got[i][k] != want[i][k] {
				t.Errorf("record %d key %q: got %q, want %q", i, k, got[i][k], want[i][k])
			}
		}
	}
}

// TestYAMLSeqWriterMatchesMarshal pins the fast path to the general one: both
// must describe the same data, even where they format it differently.
func TestYAMLSeqWriterMatchesMarshal(t *testing.T) {
	cols := []string{"id", "level", "message"}
	structArr, recType := yamlRecordType(cols)
	rec := reflect.New(recType).Elem()
	rec.Field(0).SetString("4ce3f93b-cce8-4930-bd49-4bd849ccaf23")
	rec.Field(1).SetString("true")
	rec.Field(2).SetString("Necessitatibus fugit ipsam ab quia odit, and then " +
		"some more text to push this value past the point where yaml.v3 folds.")

	decode := func(t *testing.T, write func(w *yamlSeqWriter) error) []map[string]string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "out.yaml")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := newYAMLSeqWriter(f, structArr)
		if err := write(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var out []map[string]string
		if err := yaml.Unmarshal(b, &out); err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		return out
	}

	fast := decode(t, func(w *yamlSeqWriter) error { return w.WriteRecord(rec.Interface()) })
	slow := decode(t, func(w *yamlSeqWriter) error { return w.writeMarshaled(rec.Interface()) })
	if !reflect.DeepEqual(fast, slow) {
		t.Errorf("fast path %v != yaml.Marshal path %v", fast, slow)
	}
}

// TestYAMLSeqWriterFallback checks that a record type the fast path cannot
// describe is handed to yaml.Marshal instead of being emitted wrongly.
func TestYAMLSeqWriterFallback(t *testing.T) {
	structArr := []reflect.StructField{{
		Name: "N_0",
		Type: reflect.TypeOf(0),
		Tag:  `yaml:"n"`,
	}}
	f, err := os.Create(filepath.Join(t.TempDir(), "out.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	w := newYAMLSeqWriter(f, structArr)
	if w.keys != nil {
		t.Error("expected the yaml.Marshal fallback for a non-string field")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestYAMLPlainSafe(t *testing.T) {
	for _, s := range []string{"abc", "Alice", "a-b_c.d", "user@host", "a/b+c", "x1"} {
		if !yamlPlainSafe(s) {
			t.Errorf("yamlPlainSafe(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"", " ", "123", "1e5", "-x", ".5", "true", "TRUE", "no", "y", "~",
		"a b", "a: b", "a#b", "a:b", "*x", "[x", "日本",
	} {
		if yamlPlainSafe(s) {
			t.Errorf("yamlPlainSafe(%q) = true, want false", s)
		}
	}
}
