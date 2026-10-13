package schema_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/schema"
)

// designDocSchemaYAML is the example schema given in docs/design/import-tooling.md
// formatted according to yaml.v3's canonical indentation style.
const designDocSchemaYAML = `version: mockagen/v1
kind: Schema
name: users
source:
    dialect: postgres
    database: appdb
    table: public.users
    captured_at: 2026-10-12T14:03:11Z
columns:
    - name: id
      sql_type: uuid
      nullable: false
      primary_key: true
      default: gen_random_uuid()
      type: GUID
      inferred_by: key:text-pk
    - name: email
      sql_type: varchar(255)
      max_length: 255
      nullable: false
      unique: true
      type: Email Address
      inferred_by: name:email
    - name: status
      sql_type: user_status
      nullable: false
      enum_values: [active, suspended, closed]
      type: Custom List
      values: [active, suspended, closed]
      inferred_by: rule:enum
    - name: login_count
      sql_type: integer
      nullable: true
      type: ""
`

// TestRoundTrip_DesignDocYAML verifies that a schema loaded from YAML and saved
// back produces byte-identical output, ensuring serialization stability.
func TestRoundTrip_DesignDocYAML(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "schema.yaml")
	if err := os.WriteFile(inPath, []byte(designDocSchemaYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := schema.Load(inPath)
	if err != nil {
		t.Fatalf("schema.Load failed: %v", err)
	}

	if loaded.Name != "users" {
		t.Errorf("loaded.Name = %q, want %q", loaded.Name, "users")
	}
	if len(loaded.Columns) != 4 {
		t.Fatalf("loaded.Columns len = %d, want 4", len(loaded.Columns))
	}

	outPath := filepath.Join(dir, "out.yaml")
	if err := schema.Save(outPath, loaded); err != nil {
		t.Fatalf("schema.Save failed: %v", err)
	}

	savedBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(savedBytes, []byte(designDocSchemaYAML)) {
		t.Errorf("round-trip YAML mismatch:\ngot:\n%s\nwant:\n%s",
			string(savedBytes), designDocSchemaYAML)
	}
}

// TestRoundTrip_JSON verifies that a schema can round-trip through JSON.
func TestRoundTrip_JSON(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "schema.yaml")
	if err := os.WriteFile(yamlPath, []byte(designDocSchemaYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := schema.Load(yamlPath)
	if err != nil {
		t.Fatal(err)
	}

	jsonPath := filepath.Join(dir, "schema.json")
	if err := schema.Save(jsonPath, s); err != nil {
		t.Fatalf("schema.Save JSON failed: %v", err)
	}

	loadedJSON, err := schema.Load(jsonPath)
	if err != nil {
		t.Fatalf("schema.Load JSON failed: %v", err)
	}

	if loadedJSON.Name != s.Name {
		t.Errorf("loadedJSON.Name = %q, want %q", loadedJSON.Name, s.Name)
	}
	if len(loadedJSON.Columns) != len(s.Columns) {
		t.Errorf("column count mismatch: got %d, want %d",
			len(loadedJSON.Columns), len(s.Columns))
	}
}

// TestRender_Couplings covers the structural couplings between introspected
// database facts and mockagen generation specs:
// 1. nullable: false forces null_percentage to 0
// 2. identity: true omits the column
// 3. unresolved type ("") refuses to render unless Force: true
// 4. foreign-key and unique constraints render with explicit warning comments
func TestRender_Couplings(t *testing.T) {
	cases := []struct {
		name        string
		schema      schema.Schema
		opts        schema.RenderOptions
		wantErr     error
		checkResult func(t *testing.T, cfg mockagen.MockagenConfig)
	}{
		{
			name: "nullable false forces null_percentage to 0",
			schema: schema.Schema{
				Name: "test_table",
				Columns: []schema.Column{
					{
						Name:           "non_null_col",
						SQLType:        "varchar(50)",
						Nullable:       false,
						NullPercentage: 45, // Human had configured 45%
						Type:           "First Name",
					},
					{
						Name:           "nullable_col",
						SQLType:        "varchar(50)",
						Nullable:       true,
						NullPercentage: 30,
						Type:           "Last Name",
					},
				},
			},
			checkResult: func(t *testing.T, cfg mockagen.MockagenConfig) {
				t.Helper()
				if len(cfg.Columns) != 2 {
					t.Fatalf("got %d columns, want 2", len(cfg.Columns))
				}
				if cfg.Columns[0].NullPercentage != 0 {
					t.Errorf("col[0].NullPercentage = %d, want 0", cfg.Columns[0].NullPercentage)
				}
				if cfg.Columns[1].NullPercentage != 30 {
					t.Errorf("col[1].NullPercentage = %d, want 30", cfg.Columns[1].NullPercentage)
				}
			},
		},
		{
			name: "identity true omits column",
			schema: schema.Schema{
				Name: "test_table",
				Columns: []schema.Column{
					{
						Name:     "seq_id",
						SQLType:  "bigint",
						Identity: true,
						Type:     "GUID",
					},
					{
						Name:     "username",
						SQLType:  "text",
						Identity: false,
						Type:     "Username",
					},
				},
			},
			checkResult: func(t *testing.T, cfg mockagen.MockagenConfig) {
				t.Helper()
				if len(cfg.Columns) != 1 {
					t.Fatalf("got %d columns, want 1 (identity column omitted)", len(cfg.Columns))
				}
				if cfg.Columns[0].Name != "username" {
					t.Errorf("remaining column = %q, want %q", cfg.Columns[0].Name, "username")
				}
			},
		},
		{
			name: "unresolved type without force returns ErrUnresolvedType",
			schema: schema.Schema{
				Name: "test_table",
				Columns: []schema.Column{
					{
						Name:    "unresolved_col",
						SQLType: "numeric(10,2)",
						Type:    "",
					},
				},
			},
			opts:    schema.RenderOptions{Force: false},
			wantErr: schema.ErrUnresolvedType,
		},
		{
			name: "unresolved type with force succeeds",
			schema: schema.Schema{
				Name: "test_table",
				Columns: []schema.Column{
					{
						Name:    "unresolved_col",
						SQLType: "numeric(10,2)",
						Type:    "",
					},
				},
			},
			opts: schema.RenderOptions{Force: true},
			checkResult: func(t *testing.T, cfg mockagen.MockagenConfig) {
				t.Helper()
				if len(cfg.Columns) != 1 {
					t.Fatalf("got %d columns, want 1", len(cfg.Columns))
				}
				if cfg.Columns[0].Name != "unresolved_col" {
					t.Errorf("column name = %q, want %q", cfg.Columns[0].Name, "unresolved_col")
				}
			},
		},
		{
			name: "foreign key renders warning comment",
			schema: schema.Schema{
				Name: "orders",
				Columns: []schema.Column{
					{
						Name:    "user_id",
						SQLType: "uuid",
						Type:    "GUID",
						References: &schema.ForeignKey{
							Table:  "users",
							Column: "id",
						},
					},
				},
			},
			checkResult: func(t *testing.T, cfg mockagen.MockagenConfig) {
				t.Helper()
				col := cfg.Columns[0]
				if !strings.Contains(col.Comment, "WARNING") {
					t.Errorf("comment %q should contain WARNING", col.Comment)
				}
				if !strings.Contains(col.Comment, "users.id") {
					t.Errorf("comment %q should mention referenced table and column users.id", col.Comment)
				}
			},
		},
		{
			name: "unique constraint renders warning comment",
			schema: schema.Schema{
				Name: "users",
				Columns: []schema.Column{
					{
						Name:    "email",
						SQLType: "varchar(255)",
						Type:    "Email Address",
						Unique:  true,
					},
				},
			},
			checkResult: func(t *testing.T, cfg mockagen.MockagenConfig) {
				t.Helper()
				col := cfg.Columns[0]
				if !strings.Contains(col.Comment, "WARNING") {
					t.Errorf("comment %q should contain WARNING", col.Comment)
				}
				if !strings.Contains(col.Comment, "unique constraint") {
					t.Errorf("comment %q should mention unique constraint", col.Comment)
				}
			},
		},
		{
			name: "both foreign key and unique render compound warning comment",
			schema: schema.Schema{
				Name: "user_profiles",
				Columns: []schema.Column{
					{
						Name:    "user_id",
						SQLType: "uuid",
						Type:    "GUID",
						Unique:  true,
						References: &schema.ForeignKey{
							Table:  "users",
							Column: "id",
						},
					},
				},
			},
			checkResult: func(t *testing.T, cfg mockagen.MockagenConfig) {
				t.Helper()
				col := cfg.Columns[0]
				if !strings.Contains(col.Comment, "WARNING") ||
					!strings.Contains(col.Comment, "users.id") ||
					!strings.Contains(col.Comment, "unique") {
					t.Errorf("comment %q should mention both warnings", col.Comment)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := tc.schema.Render(tc.opts)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected render error: %v", err)
			}
			if tc.checkResult != nil {
				tc.checkResult(t, cfg)
			}
		})
	}
}

// TestLoad_Errors guards against invalid format extensions and mismatched kinds.
func TestLoad_Errors(t *testing.T) {
	dir := t.TempDir()

	// Unsupported extension.
	unsupportedPath := filepath.Join(dir, "schema.toml")
	if err := os.WriteFile(unsupportedPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Load(unsupportedPath); !errors.Is(err, schema.ErrUnsupportedFormat) {
		t.Errorf("expected ErrUnsupportedFormat for .toml, got %v", err)
	}

	// Unknown kind.
	badKindPath := filepath.Join(dir, "bad_kind.yaml")
	badKindContent := `version: mockagen/v1
kind: NotASchema
name: test
`
	if err := os.WriteFile(badKindPath, []byte(badKindContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.Load(badKindPath); !errors.Is(err, schema.ErrUnknownKind) {
		t.Errorf("expected ErrUnknownKind, got %v", err)
	}
}

// TestRender_TypeValidation covers the gap between "has a type" and "has a
// type that generates": generation resolves a type by exact string lookup, so
// a typo or a stray space produces random text rather than an error. These
// cases pin that Render rejects both, and that a type requiring a value list
// cannot render without one - a bare "oneof:" tag makes faker error and
// generateFakes panic.
func TestRender_TypeValidation(t *testing.T) {
	col := func(c schema.Column) schema.Schema {
		return schema.Schema{Name: "t", Columns: []schema.Column{c}}
	}
	cases := []struct {
		name    string
		schema  schema.Schema
		opts    schema.RenderOptions
		wantErr error
		want    mockagen.MockagenColumn
	}{
		{
			name:    "unknown type is rejected",
			schema:  col(schema.Column{Name: "email", Type: "Email"}),
			wantErr: schema.ErrUnknownType,
		},
		{
			name:    "unknown type is rejected even with Force",
			schema:  col(schema.Column{Name: "email", Type: "Email"}),
			opts:    schema.RenderOptions{Force: true},
			wantErr: schema.ErrUnknownType,
		},
		{
			name:   "surrounding space is trimmed rather than missing the catalog",
			schema: col(schema.Column{Name: "id", Type: "  GUID  "}),
			want:   mockagen.MockagenColumn{Name: "id", Type: "GUID"},
		},
		{
			name:    "empty type is still unresolved",
			schema:  col(schema.Column{Name: "x", Type: ""}),
			wantErr: schema.ErrUnresolvedType,
		},
		{
			name:    "Custom List with no values is rejected",
			schema:  col(schema.Column{Name: "status", Type: "Custom List"}),
			wantErr: schema.ErrMissingValues,
		},
		{
			name: "Custom List falls back to introspected enum labels",
			schema: col(schema.Column{
				Name:       "status",
				Type:       "Custom List",
				EnumValues: []string{"active", "closed"},
			}),
			want: mockagen.MockagenColumn{
				Name:   "status",
				Type:   "Custom List",
				Values: []string{"active", "closed"},
			},
		},
		{
			name: "explicit values win over enum labels",
			schema: col(schema.Column{
				Name:       "status",
				Type:       "Custom List",
				Values:     []string{"chosen"},
				EnumValues: []string{"active", "closed"},
			}),
			want: mockagen.MockagenColumn{
				Name:   "status",
				Type:   "Custom List",
				Values: []string{"chosen"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := schema.Render(tc.schema, tc.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Render() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() unexpected error: %v", err)
			}
			if len(cfg.Columns) != 1 {
				t.Fatalf("got %d columns, want 1", len(cfg.Columns))
			}
			got := cfg.Columns[0]
			if got.Type != tc.want.Type {
				t.Errorf("Type = %q, want %q", got.Type, tc.want.Type)
			}
			if !slices.Equal(got.Values, tc.want.Values) {
				t.Errorf("Values = %q, want %q", got.Values, tc.want.Values)
			}
		})
	}
}

// TestRender_OrdinalOrder pins that introspected column order wins over slice
// order. The rendered spec is file_format "sql", and a bulk load maps CSV
// fields by position on mssql, so a schema that has been through a tool that
// reorders keys must still render in the database's own column order.
func TestRender_OrdinalOrder(t *testing.T) {
	cases := []struct {
		name string
		cols []schema.Column
		want []string
	}{
		{
			name: "ordinals reorder a shuffled column list",
			cols: []schema.Column{
				{Name: "third", Ordinal: 3, Type: "GUID"},
				{Name: "first", Ordinal: 1, Type: "GUID"},
				{Name: "second", Ordinal: 2, Type: "GUID"},
			},
			want: []string{"first", "second", "third"},
		},
		{
			name: "no ordinals keeps the authored order",
			cols: []schema.Column{
				{Name: "b", Type: "GUID"},
				{Name: "a", Type: "GUID"},
			},
			want: []string{"b", "a"},
		},
		{
			name: "identity columns are dropped after ordering",
			cols: []schema.Column{
				{Name: "email", Ordinal: 2, Type: "Email Address"},
				{Name: "seq", Ordinal: 1, Identity: true},
			},
			want: []string{"email"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := schema.Render(schema.Schema{Name: "t", Columns: tc.cols},
				schema.RenderOptions{})
			if err != nil {
				t.Fatalf("Render(): %v", err)
			}
			got := make([]string, 0, len(cfg.Columns))
			for _, c := range cfg.Columns {
				got = append(got, c.Name)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("column order = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExtensionCaseInsensitivity guards against rejecting a file for the case
// of its extension, which parses identically either way.
func TestExtensionCaseInsensitivity(t *testing.T) {
	s := schema.Schema{
		Version: "mockagen/v1",
		Kind:    "Schema",
		Name:    "users",
		Columns: []schema.Column{{Name: "id", SQLType: "uuid", Type: "GUID"}},
	}
	for _, name := range []string{"s.YAML", "s.YML", "s.JSON", "s.Yaml"} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), name)
			if err := schema.Save(p, s); err != nil {
				t.Fatalf("Save(%s): %v", name, err)
			}
			got, err := schema.Load(p)
			if err != nil {
				t.Fatalf("Load(%s): %v", name, err)
			}
			if got.Name != s.Name || len(got.Columns) != 1 {
				t.Errorf("round trip of %s lost content: %+v", name, got)
			}
		})
	}
}

// TestWarnings covers the constraints a schema records but generation cannot
// honour. Render writes them onto each column's Comment, which only reaches a
// user if the spec is serialized; Warnings is how a command shows them.
func TestWarnings(t *testing.T) {
	s := schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: "GUID", Unique: true},
			{Name: "org_id", Type: "GUID",
				References: &schema.ForeignKey{Table: "public.orgs", Column: "id"}},
			{Name: "seq", Identity: true, Unique: true},
			{Name: "plain", Type: "GUID"},
		},
	}
	got := s.Warnings()
	want := []string{
		`column "id": unique constraint cannot be guaranteed by generator`,
		`column "org_id": foreign key referencing public.orgs.id cannot be guaranteed by generator`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("Warnings() =\n%v\nwant\n%v", got, want)
	}

	// Render must report the same facts, so the two cannot drift.
	cfg, err := schema.Render(s, schema.RenderOptions{})
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	var commented int
	for _, c := range cfg.Columns {
		if strings.HasPrefix(c.Comment, "WARNING: ") {
			commented++
		}
	}
	if commented != len(want) {
		t.Errorf("Render() wrote %d warning comments, want %d", commented, len(want))
	}
}
