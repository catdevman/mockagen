package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeImportFixture(t *testing.T, schemaYAML, mapYAML, data string) (dir, mapPath, dataPath string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "users.schema.yaml"), []byte(schemaYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	mapPath = filepath.Join(dir, "users.map.yaml")
	if err := os.WriteFile(mapPath, []byte(mapYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	dataPath = filepath.Join(dir, "legacy.csv")
	if err := os.WriteFile(dataPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, mapPath, dataPath
}

const importSchemaYAML = `version: mockagen/v1
kind: Schema
name: users
source:
  dialect: postgres
  table: users
columns:
  - name: id
    sql_type: integer
    identity: true
  - name: email
    sql_type: text
  - name: note
    sql_type: text
    nullable: true
    default: "''"
  - name: login_count
    sql_type: integer
    nullable: true
    default: "0"
`

const importMapYAML = `version: mockagen/v1
kind: FieldMap
name: users-import
schema: users.schema.yaml
source:
  format: csv
  has_header: true
  delimiter: ","
  encoding: utf-8
  fields: [email, note, login_count]
fields:
  - target: email
    source: email
  - target: note
    source: note
  - target: login_count
    source: login_count
omitted: [id]
unmapped:
  source: ignore
on_error: abort
`

func TestImportCommand(t *testing.T) {
	data := "email,note,login_count\nada@example.com,hello,3\nbea@example.com,\n"
	_, mapPath, dataPath := writeImportFixture(t, importSchemaYAML, importMapYAML, data)
	outPath := filepath.Join(filepath.Dir(mapPath), "out", "users.sql")

	var stdout, stderr strings.Builder
	code := runImport([]string{"-map", mapPath, "-data", dataPath, "-out", outPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d\n%s", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO \"users\" (\"email\", \"note\", \"login_count\") VALUES\n" +
		"\t('ada@example.com', 'hello', 3),\n" +
		"\t('bea@example.com', '', NULL);\n"
	if string(got) != want {
		t.Fatalf("sql =\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(string(got), "\"id\"") {
		t.Fatalf("identity column present:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "import: read 2, wrote 2, skipped 0, errors 0") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestImportRejects(t *testing.T) {
	schemaYAML := `version: mockagen/v1
kind: Schema
name: users
source:
  dialect: postgres
  table: users
columns:
  - name: email
    sql_type: text
  - name: login_count
    sql_type: integer
  - name: active
    sql_type: boolean
  - name: status
    sql_type: user_status
    enum_values: [active, suspended, closed]
`
	mapYAML := `version: mockagen/v1
kind: FieldMap
name: users-import
schema: users.schema.yaml
source:
  format: csv
  has_header: true
  fields: [email, login_count, active, status]
fields:
  - target: email
    source: email
  - target: login_count
    source: login_count
  - target: active
    source: active
  - target: status
    source: status
unmapped:
  source: ignore
on_error: skip_row
`
	data := "email,login_count,active,status\n" +
		"ada@example.com,nope,true,active\n" +
		"bea@example.com,1,maybe,active\n" +
		"cy@example.com,1,true,nope\n"
	dir, mapPath, dataPath := writeImportFixture(t, schemaYAML, mapYAML, data)
	outPath := filepath.Join(dir, "out.sql")
	rejPath := filepath.Join(dir, "rejects.csv")

	var stdout, stderr strings.Builder
	code := runImport([]string{
		"-map", mapPath, "-data", dataPath, "-out", outPath, "-rejects", rejPath,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d\n%s", code, stderr.String())
	}
	got, err := os.ReadFile(rejPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "line,target,reason\n" +
		"2,login_count,\"strconv.ParseInt: parsing \"\"nope\"\": invalid syntax\"\n" +
		"3,active,\"invalid boolean \"\"maybe\"\"\"\n" +
		"4,status,\"\"\"nope\"\" is not an allowed value\"\n"
	if string(got) != want {
		t.Fatalf("rejects =\n%s\nwant:\n%s", got, want)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "import: read 3, wrote 0, skipped 3, errors 3") {
		t.Fatalf("stderr = %s", msg)
	}
	for _, line := range []string{
		"import: line 2: column login_count: strconv.ParseInt: parsing \"nope\": invalid syntax",
		"import: line 3: column active: invalid boolean \"maybe\"",
		"import: line 4: column status: \"nope\" is not an allowed value",
	} {
		if !strings.Contains(msg, line) {
			t.Fatalf("missing %q\n%s", line, msg)
		}
	}
	if strings.Count(msg, "mockagen import:") != 0 {
		t.Fatalf("row error printed twice:\n%s", msg)
	}
}

func TestImportSwappedHeaderWritesNothing(t *testing.T) {
	data := "note,email,login_count\nhello,ada@example.com,3\n"
	dir, mapPath, dataPath := writeImportFixture(t, importSchemaYAML, importMapYAML, data)
	outPath := filepath.Join(dir, "out.sql")
	var stdout, stderr strings.Builder
	code := runImport([]string{"-map", mapPath, "-data", dataPath, "-out", outPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d\n%s", code, stderr.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, `file has ["note" "email" "login_count"]`) || !strings.Contains(msg, `map has ["email" "note" "login_count"]`) {
		t.Fatalf("stderr = %s", msg)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("output stat %v", err)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	data := "email,note,login_count\nada@example.com,hello,3\n"
	dir, mapPath, dataPath := writeImportFixture(t, importSchemaYAML, importMapYAML, data)
	outPath := filepath.Join(dir, "out.sql")
	var stdout, stderr strings.Builder
	code := runImport([]string{"-map", mapPath, "-data", dataPath, "-out", outPath, "-dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d\n%s", code, stderr.String())
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("output stat %v", err)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "mockagen import: dry run") || !strings.Contains(msg, "import: read 1, wrote 1, skipped 0, errors 0") {
		t.Fatalf("stderr = %s", msg)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestImportRefusesUncleanPlan(t *testing.T) {
	// nickname is NOT NULL with no default and no field. The plan is
	// refused before any output file exists.
	schemaYAML := `version: mockagen/v1
kind: Schema
name: users
columns:
  - name: email
    sql_type: text
  - name: nickname
    sql_type: text
`
	mapYAML := `version: mockagen/v1
kind: FieldMap
name: users-import
schema: users.schema.yaml
source:
  format: csv
  has_header: true
  fields: [email]
fields:
  - target: email
    source: email
unmapped:
  source: ignore
on_error: abort
`
	dir, mapPath, dataPath := writeImportFixture(t, schemaYAML, mapYAML, "email\nada@example.com\n")
	outPath := filepath.Join(dir, "out.sql")
	var stdout, stderr strings.Builder
	code := runImport([]string{"-map", mapPath, "-data", dataPath, "-out", outPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "required column is not mapped") {
		t.Fatalf("stderr = %s", stderr.String())
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatalf("output stat %v", err)
	}
}

func TestImportUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := runImport(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d\n%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runImport([]string{"-h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help code = %d\n%s", code, stderr.String())
	}
}
