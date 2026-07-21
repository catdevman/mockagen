package mockagen

import (
	"os"
	"path/filepath"
	"testing"
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
