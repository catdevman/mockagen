package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/internal/tui/mapui"
	"github.com/catdevman/mockagen/pkg/importer"
)

func TestMapCommand(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "users.schema.yaml")
	dataPath := filepath.Join(dir, "legacy.csv")
	outPath := filepath.Join(dir, "out", "users.map.yaml")
	schemaYAML := []byte(`version: mockagen/v1
kind: Schema
name: users
columns:
  - name: email
    sql_type: varchar(255)
  - name: note
    sql_type: text
    nullable: true
`)
	if err := os.WriteFile(schemaPath, schemaYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("email,note,title\na,b,c\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"-schema", schemaPath, "-data", dataPath, "-out", outPath, "-sample", "1"}
	if code := runMap(args, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `version: mockagen/v1
kind: FieldMap
name: users-import
schema: ../users.schema.yaml
source:
    format: csv
    has_header: true
    delimiter: ','
    encoding: utf-8
    fields: [email, note, title]
fields:
    - target: email
      source: email
      transforms:
        - trim: {}
    - target: note
      source: note
      transforms:
        - trim: {}
unmapped:
    source: ignore
on_error: abort
`
	if string(got) != want {
		t.Fatalf("yaml =\n%s\nwant:\n%s", got, want)
	}

	edited := strings.Replace(string(got), "trim: {}", "case: lower", 1)
	if err := os.WriteFile(outPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	schemaYAML = []byte(`version: mockagen/v1
kind: Schema
name: users
columns:
  - name: email
    sql_type: varchar(255)
  - name: note
    sql_type: text
    nullable: true
  - name: title
    sql_type: varchar(80)
`)
	if err := os.WriteFile(schemaPath, schemaYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args = []string{"-schema", schemaPath, "-data", dataPath, "-map", outPath, "-out", outPath, "-sample", "1"}
	if code := runMap(args, &stdout, &stderr); code != 0 {
		t.Fatalf("extend code = %d, stderr %s", code, stderr.String())
	}
	got, err = os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "case: lower") {
		t.Fatalf("edited chain was rewritten:\n%s", got)
	}
	if strings.Count(string(got), "target: title") != 1 {
		t.Fatalf("title was not added once:\n%s", got)
	}
	if strings.Count(string(got), "target: email") != 1 {
		t.Fatalf("email was duplicated:\n%s", got)
	}

	swapped := filepath.Join(dir, "swapped.csv")
	if err := os.WriteFile(swapped, []byte("title,email,note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := string(got)
	stdout.Reset()
	stderr.Reset()
	args = []string{"-schema", schemaPath, "-data", swapped, "-map", outPath, "-out", outPath}
	if code := runMap(args, &stdout, &stderr); code != 1 {
		t.Fatalf("swap code = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "map: email,note,title") || !strings.Contains(stderr.String(), "data: title,email,note") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	after, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("header mismatch rebound the map\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMapFixedLayout(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "people.schema.yaml")
	dataPath := filepath.Join(dir, "people.fixed")
	outPath := filepath.Join(dir, "out.map.yaml")
	mapPath := filepath.Join(dir, "people.map.yaml")
	schemaYAML := []byte(`version: mockagen/v1
kind: Schema
name: people
columns:
  - name: name
    sql_type: text
`)
	if err := os.WriteFile(schemaPath, schemaYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("Ann   NY  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	args := []string{"-schema", schemaPath, "-data", dataPath, "-out", outPath}
	if code := runMap(args, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "source.fixed") {
		t.Fatalf("stderr = %s", stderr.String())
	}

	mapYAML := []byte(`version: mockagen/v1
kind: FieldMap
name: people-import
source:
    format: fixed
    has_header: false
    encoding: utf-8
    fixed:
        - name: name
          start: 1
          end: 6
        - name: city
          start: 7
          end: 10
fields:
    - target: name
      source: name
unmapped:
    source: ignore
on_error: abort
`)
	if err := os.WriteFile(mapPath, mapYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	args = []string{"-schema", schemaPath, "-data", dataPath, "-map", mapPath, "-out", outPath}
	if code := runMap(args, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "start: 1") || !strings.Contains(string(got), "end: 10") {
		t.Fatalf("layout was dropped:\n%s", got)
	}
}

func TestMapCommandHelpMentionsTUI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMap([]string{"-help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-tui") || !strings.Contains(stderr.String(), "missing file") {
		t.Fatalf("help = %s", stderr.String())
	}
}

func TestMapCommandUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMap(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-schema and -data are required") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func stubScreen(t *testing.T, fn func(mapui.Session) (mapui.Result, error)) {
	t.Helper()
	prev := runMapScreen
	t.Cleanup(func() { runMapScreen = prev })
	runMapScreen = fn
}

func TestMapTUIPromptsForMissingFiles(t *testing.T) {
	// -tui with no files opens the screen instead of exiting 2. A
	// script without -tui still has to pass -schema and -data.
	var got mapui.Session
	stubScreen(t, func(sess mapui.Session) (mapui.Result, error) {
		got = sess
		return mapui.Result{}, nil
	})
	var stdout, stderr bytes.Buffer
	if code := runMap([]string{"-tui"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "required") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if got.Ask == nil || !got.Ask.NeedSchema || !got.Ask.NeedData || !got.Ask.NeedMap {
		t.Fatalf("ask = %+v", got.Ask)
	}
}

func TestMapTUIKeepsFlagsAlreadyPassed(t *testing.T) {
	// A flag the user typed is loaded before the screen and is not
	// asked again. -map is still asked, because it was left off.
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "users.schema.yaml")
	dataPath := filepath.Join(dir, "legacy.csv")
	schemaYAML := []byte("version: mockagen/v1\nkind: Schema\nname: users\ncolumns:\n  - name: email\n    sql_type: varchar(255)\n")
	if err := os.WriteFile(schemaPath, schemaYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("email\nada\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got mapui.Session
	stubScreen(t, func(sess mapui.Session) (mapui.Result, error) {
		got = sess
		return mapui.Result{}, nil
	})
	var stdout, stderr bytes.Buffer
	args := []string{"-tui", "-schema", schemaPath, "-data", dataPath}
	if code := runMap(args, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	if got.Ask == nil || got.Ask.NeedSchema || got.Ask.NeedData || !got.Ask.NeedMap {
		t.Fatalf("ask = %+v", got.Ask)
	}
	if got.Schema.Name != "users" || len(got.Sample.Fields) != 1 || got.Sample.Fields[0] != "email" {
		t.Fatalf("schema %q fields %v", got.Schema.Name, got.Sample.Fields)
	}
}

func TestMapTUIRejectsABadFlagBeforeTheScreen(t *testing.T) {
	// A path on the command line that does not load is exit 1. The
	// picker is for flags that were left off, not a retry of a bad one.
	called := false
	stubScreen(t, func(mapui.Session) (mapui.Result, error) {
		called = true
		return mapui.Result{}, nil
	})
	var stdout, stderr bytes.Buffer
	args := []string{"-tui", "-schema", filepath.Join(t.TempDir(), "missing.yaml")}
	if code := runMap(args, &stdout, &stderr); code != 1 || called {
		t.Fatalf("code %d called %v stderr %s", code, called, stderr.String())
	}
}

func TestMapTUIDriftWritesTheFlagMap(t *testing.T) {
	// The screen found that a -map from the command line does not
	// match the data it was given. The file is written unchanged.
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "users.schema.yaml")
	dataPath := filepath.Join(dir, "legacy.csv")
	outPath := filepath.Join(dir, "users.map.yaml")
	schemaYAML := []byte("version: mockagen/v1\nkind: Schema\nname: users\ncolumns:\n  - name: email\n    sql_type: varchar(255)\n  - name: note\n    sql_type: text\n    nullable: true\n")
	if err := os.WriteFile(schemaPath, schemaYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("email,note\na,b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runMap([]string{"-schema", schemaPath, "-data", dataPath, "-out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("setup code = %d, stderr %s", code, stderr.String())
	}
	before, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	stubScreen(t, func(sess mapui.Session) (mapui.Result, error) {
		if sess.Ask == nil || !sess.Ask.NeedData || sess.Ask.NeedMap || sess.Ask.Base == nil {
			t.Fatalf("ask = %+v", sess.Ask)
		}
		return mapui.Result{
			Drift:  true,
			Map:    *sess.Ask.Base,
			Sample: importer.Sample{HasHeader: true, Fields: []string{"other"}},
		}, nil
	})
	stdout.Reset()
	stderr.Reset()
	args := []string{"-tui", "-schema", schemaPath, "-map", outPath, "-out", outPath}
	if code := runMap(args, &stdout, &stderr); code != 1 {
		t.Fatalf("code = %d, stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "source header does not match") || !strings.Contains(stderr.String(), "data: other") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	after, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("drift rewrote the map\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
