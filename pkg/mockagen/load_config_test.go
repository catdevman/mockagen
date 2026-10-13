package mockagen

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestLoadConfig_JSON(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.json")

	if cfg.Name != "single" {
		t.Errorf("Name: got %q, want %q", cfg.Name, "single")
	}
	if cfg.NumberOfRecords != 1 {
		t.Errorf("NumberOfRecords: got %d, want 1", cfg.NumberOfRecords)
	}
	if cfg.FileFormat != "json" {
		t.Errorf("FileFormat: got %q, want %q", cfg.FileFormat, "json")
	}
	if !cfg.IncludeHeader {
		t.Error("IncludeHeader: got false, want true")
	}
	if len(cfg.Columns) != 7 {
		t.Fatalf("Columns: got %d, want 7", len(cfg.Columns))
	}
}

func TestLoadConfig_JSON_Columns(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.json")

	tests := []struct {
		idx            int
		name           string
		typ            string
		nullPercentage int64
		formula        string
	}{
		{0, "id", "GUID", 0, "upper(this)"},
		{1, "first_name", "First Name", 0, ""},
		{2, "last_name", "Last Name", 0, ""},
		{3, "email", "Email Address", 0, ""},
		{4, "gender", "Gender", 10, ""},
		{5, "date", "Datetime", 10, ""},
		{6, "custom", "Custom List", 10, ""},
	}
	for _, tt := range tests {
		col := cfg.Columns[tt.idx]
		if col.Name != tt.name {
			t.Errorf("col[%d].Name: got %q, want %q", tt.idx, col.Name, tt.name)
		}
		if col.Type != tt.typ {
			t.Errorf("col[%d].Type: got %q, want %q", tt.idx, col.Type, tt.typ)
		}
		if col.NullPercentage != tt.nullPercentage {
			t.Errorf("col[%d].NullPercentage: got %d, want %d", tt.idx, col.NullPercentage, tt.nullPercentage)
		}
		if col.Formula != tt.formula {
			t.Errorf("col[%d].Formula: got %q, want %q", tt.idx, col.Formula, tt.formula)
		}
	}
}

func TestLoadConfig_JSON_DatetimeColumn(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.json")
	date := cfg.Columns[5]

	if date.Min != "12/24/2019" {
		t.Errorf("Min: got %q, want %q", date.Min, "12/24/2019")
	}
	if date.Max != "12/23/2020" {
		t.Errorf("Max: got %q, want %q", date.Max, "12/23/2020")
	}
	if date.Format != "%Y/%m/%d" {
		t.Errorf("Format: got %q, want %q", date.Format, "%Y/%m/%d")
	}
}

func TestLoadConfig_JSON_CustomListColumn(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.json")
	col := cfg.Columns[6]

	if len(col.Values) != 3 {
		t.Fatalf("Values: got %d, want 3", len(col.Values))
	}
	if col.Values[0] != "1" || col.Values[1] != "2" || col.Values[2] != "3" {
		t.Errorf("Values: got %v, want [1 2 3]", col.Values)
	}
	if col.SelectionStyle != "random" {
		t.Errorf("SelectionStyle: got %q, want %q", col.SelectionStyle, "random")
	}
}

func TestLoadConfig_JSON_FixedWidth(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.fixed.json")

	if cfg.FileFormat != "fixed" {
		t.Errorf("FileFormat: got %q, want %q", cfg.FileFormat, "fixed")
	}
	col := cfg.Columns[0]
	if col.StartPosition != 1 {
		t.Errorf("StartPosition: got %d, want 1", col.StartPosition)
	}
	if col.EndPosition != 36 {
		t.Errorf("EndPosition: got %d, want 36", col.EndPosition)
	}
}

func TestLoadConfig_YAML(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/Mockadoo.schema.yaml")

	if cfg.Name != "Data Generator Schema" {
		t.Errorf("Name: got %q, want %q", cfg.Name, "Data Generator Schema")
	}
	if cfg.NumberOfRecords != 1000000 {
		t.Errorf("NumberOfRecords: got %d, want 1000000", cfg.NumberOfRecords)
	}
	if cfg.FileFormat != "yaml" {
		t.Errorf("FileFormat: got %q, want %q", cfg.FileFormat, "yaml")
	}
	if len(cfg.Columns) != 7 {
		t.Fatalf("Columns: got %d, want 7", len(cfg.Columns))
	}
}

// TestLoadConfig_YML guards the .yml extension against being rejected:
// allowedExt historically accepted .yaml only, panicking on .yml. This table
// test validates that configs in all three canonical formats (YAML, JSON,
// TOML) decode identically when persisted as a .yml file.
func TestLoadConfig_YML(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{
			name:   "yaml source",
			source: "../../test_data/config/Mockadoo.schema.yaml",
		},
		{
			name:   "json source",
			source: "../../test_data/config/single.schema.json",
		},
		{
			name:   "toml source",
			source: "../../test_data/config/single.schema.toml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := LoadConfig(tc.source)
			data, err := yaml.Marshal(orig)
			if err != nil {
				t.Fatalf("yaml.Marshal: %v", err)
			}
			path := filepath.Join(t.TempDir(), "schema.yml")
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got := LoadConfig(path)
			if got.Name != orig.Name {
				t.Errorf("Name: got %q, want %q", got.Name, orig.Name)
			}
			if got.NumberOfRecords != orig.NumberOfRecords {
				t.Errorf("NumberOfRecords: got %d, want %d", got.NumberOfRecords, orig.NumberOfRecords)
			}
			if got.FileFormat != orig.FileFormat {
				t.Errorf("FileFormat: got %q, want %q", got.FileFormat, orig.FileFormat)
			}
			if len(got.Columns) != len(orig.Columns) {
				t.Fatalf("Columns count: got %d, want %d", len(got.Columns), len(orig.Columns))
			}
			for i := range orig.Columns {
				if got.Columns[i].Name != orig.Columns[i].Name {
					t.Errorf("col[%d] Name: got %q, want %q", i, got.Columns[i].Name, orig.Columns[i].Name)
				}
				if got.Columns[i].Type != orig.Columns[i].Type {
					t.Errorf("col[%d] Type: got %q, want %q", i, got.Columns[i].Type, orig.Columns[i].Type)
				}
			}
		})
	}
}

func TestLoadConfig_YAML_MatchesJSON(t *testing.T) {
	json := LoadConfig("../../test_data/config/single.schema.json")
	yaml := LoadConfig("../../test_data/config/Mockadoo.schema.yaml")

	// Both have same column names and types
	if len(json.Columns) != len(yaml.Columns) {
		t.Fatalf("column count mismatch: json=%d yaml=%d", len(json.Columns), len(yaml.Columns))
	}
	for i := range json.Columns {
		if json.Columns[i].Name != yaml.Columns[i].Name {
			t.Errorf("col[%d] Name mismatch: json=%q yaml=%q", i, json.Columns[i].Name, yaml.Columns[i].Name)
		}
		if json.Columns[i].Type != yaml.Columns[i].Type {
			t.Errorf("col[%d] Type mismatch: json=%q yaml=%q", i, json.Columns[i].Type, yaml.Columns[i].Type)
		}
	}
}

func TestLoadConfig_TOML(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.toml")

	if cfg.Name != "single" {
		t.Errorf("Name: got %q, want %q", cfg.Name, "single")
	}
	if cfg.NumberOfRecords != 1 {
		t.Errorf("NumberOfRecords: got %d, want 1", cfg.NumberOfRecords)
	}
	if cfg.FileFormat != "json" {
		t.Errorf("FileFormat: got %q, want %q", cfg.FileFormat, "json")
	}
	if !cfg.IncludeHeader {
		t.Error("IncludeHeader: got false, want true")
	}
	if len(cfg.Columns) != 7 {
		t.Fatalf("Columns: got %d, want 7", len(cfg.Columns))
	}
}

func TestLoadConfig_TOML_MatchesJSON(t *testing.T) {
	jsonCfg := LoadConfig("../../test_data/config/single.schema.json")
	tomlCfg := LoadConfig("../../test_data/config/single.schema.toml")

	if jsonCfg.NumberOfRecords != tomlCfg.NumberOfRecords {
		t.Errorf("NumberOfRecords mismatch: json=%d toml=%d", jsonCfg.NumberOfRecords, tomlCfg.NumberOfRecords)
	}
	if len(jsonCfg.Columns) != len(tomlCfg.Columns) {
		t.Fatalf("column count mismatch: json=%d toml=%d", len(jsonCfg.Columns), len(tomlCfg.Columns))
	}
	for i := range jsonCfg.Columns {
		jc, tc := jsonCfg.Columns[i], tomlCfg.Columns[i]
		if jc.Name != tc.Name {
			t.Errorf("col[%d] Name: json=%q toml=%q", i, jc.Name, tc.Name)
		}
		if jc.Type != tc.Type {
			t.Errorf("col[%d] Type: json=%q toml=%q", i, jc.Type, tc.Type)
		}
		if jc.NullPercentage != tc.NullPercentage {
			t.Errorf("col[%d] NullPercentage: json=%d toml=%d", i, jc.NullPercentage, tc.NullPercentage)
		}
		if jc.Formula != tc.Formula {
			t.Errorf("col[%d] Formula: json=%q toml=%q", i, jc.Formula, tc.Formula)
		}
	}
}

func TestLoadConfig_TOML_DatetimeColumn(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.toml")
	date := cfg.Columns[5]

	if date.Min != "12/24/2019" {
		t.Errorf("Min: got %q, want %q", date.Min, "12/24/2019")
	}
	if date.Max != "12/23/2020" {
		t.Errorf("Max: got %q, want %q", date.Max, "12/23/2020")
	}
	if date.Format != "%Y/%m/%d" {
		t.Errorf("Format: got %q, want %q", date.Format, "%Y/%m/%d")
	}
}

func TestLoadConfig_TOML_CustomListColumn(t *testing.T) {
	cfg := LoadConfig("../../test_data/config/single.schema.toml")
	col := cfg.Columns[6]

	if len(col.Values) != 3 {
		t.Fatalf("Values: got %d, want 3", len(col.Values))
	}
	if col.Values[0] != "1" || col.Values[1] != "2" || col.Values[2] != "3" {
		t.Errorf("Values: got %v, want [1 2 3]", col.Values)
	}
}

func TestLoadConfig_InvalidTOML_Panics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("[[not valid toml"), 0600); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid TOML, got none")
		}
	}()
	LoadConfig(path)
}

func TestLoadConfig_UnsupportedExtension_Panics(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "schema-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for unsupported extension, got none")
		}
	}()
	LoadConfig(f.Name())
}

func TestLoadConfig_MissingFile_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for missing file, got none")
		}
	}()
	LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.json"))
}

func TestLoadConfig_InvalidJSON_Panics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not valid json {{{"), 0600); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid JSON, got none")
		}
	}()
	LoadConfig(path)
}

func TestLoadConfig_InvalidYAML_Panics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(":\t:\t:"), 0600); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid YAML, got none")
		}
	}()
	LoadConfig(path)
}

// TestLoad_KindDispatch verifies that kind-less files default to Spec, files
// with explicit kind: Spec load identically, and an unrecognised kind returns
// ErrUnknownKind.
func TestLoad_KindDispatch(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		ext      string
		wantErr  error
		wantKind string
	}{
		{
			name: "kind-less yaml defaults to Spec",
			content: `name: test
num_rows: 5
file_format: csv
columns:
  - name: id
    type: GUID
`,
			ext:      ".yaml",
			wantKind: "Spec",
		},
		{
			name: "explicit kind Spec in yaml",
			content: `version: mockagen/v1
kind: Spec
name: test
num_rows: 5
file_format: csv
columns:
  - name: id
    type: GUID
`,
			ext:      ".yaml",
			wantKind: "Spec",
		},
		{
			name: "explicit lowercase kind spec in json",
			content: `{
  "version": "mockagen/v1",
  "kind": "spec",
  "name": "test",
  "num_rows": 5,
  "file_format": "json",
  "columns": [
    {"name": "id", "type": "GUID"}
  ]
}`,
			ext:      ".json",
			wantKind: "Spec",
		},
		{
			name: "unknown kind returns ErrUnknownKind",
			content: `version: mockagen/v1
kind: MysteryKind
name: test
num_rows: 5
file_format: csv
columns:
  - name: id
    type: GUID
`,
			ext:     ".yaml",
			wantErr: ErrUnknownKind,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config"+tc.ext)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("Load(%s) expected error %v, got nil", tc.name, tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("Load(%s) error = %v, want %v", tc.name, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(%s) unexpected error: %v", tc.name, err)
			}
			if cfg.Kind != tc.wantKind {
				t.Errorf("cfg.Kind = %q, want %q", cfg.Kind, tc.wantKind)
			}
		})
	}
}

// TestLoad_UnknownColumnType guards against silent fallback to random string
// generation by ensuring unrecognised column types return ErrUnknownColumnType
// naming both the offending column and the unrecognised type.
func TestLoad_UnknownColumnType(t *testing.T) {
	content := `name: bad_type_test
num_rows: 1
file_format: json
columns:
  - name: good_col
    type: GUID
  - name: bad_col
    type: NonExistentGeneratorType
`
	path := filepath.Join(t.TempDir(), "bad_col.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown column type, got nil")
	}
	if !errors.Is(err, ErrUnknownColumnType) {
		t.Errorf("error = %v, want %v", err, ErrUnknownColumnType)
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "bad_col") {
		t.Errorf("error %q should mention column name bad_col", errMsg)
	}
	if !strings.Contains(errMsg, "NonExistentGeneratorType") {
		t.Errorf("error %q should mention type name NonExistentGeneratorType", errMsg)
	}
}
