// Package schema models introspected database table schemas, separates
// structural facts from data-generation decisions, and translates schemas
// into mockagen generation specifications.
package schema

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/types"
	yaml "gopkg.in/yaml.v3"
)

// ErrUnresolvedType is returned by Render when a column has no generator
// type assigned and Force is not enabled. Prevents silent fallback to random
// strings when converting introspected schemas into generation specs.
var ErrUnresolvedType = errors.New("schema: unresolved column generator type")

// ErrUnknownType is returned by Render when a column names a generator type
// the catalog does not hold. Emptiness is not the only way to get random
// strings: generation looks a type up by exact string (mapToFaker[col.Type]),
// so a typo or a stray space misses the map, leaves the faker tag empty and
// silently fills the column with random text. Checking membership rather than
// emptiness is the whole reason the catalog was promoted out of package main.
var ErrUnknownType = errors.New("schema: unknown column generator type")

// ErrMissingValues is returned by Render when a column's type requires a
// value list and none is available. It is not cosmetic: a "Custom List" with
// no values renders the faker tag "oneof:" with no arguments, which faker
// rejects with ErrNotEnoughTagArguments, and generateFakes panics on a faker
// error - so the spec would not produce bad data, it would abort the run.
var ErrMissingValues = errors.New("schema: column type requires values")

// ErrUnsupportedFormat is returned when a file extension is not recognised
// as either YAML or JSON. Schema files are generated artifacts and support
// only YAML and JSON (TOML is excluded to avoid nested array complexity).
var ErrUnsupportedFormat = errors.New("schema: unsupported file format")

// ErrUnknownKind is returned when an envelope specifies a kind other than Schema.
var ErrUnknownKind = errors.New("schema: unknown kind")

// ForeignKey captures an outbound foreign key constraint referencing another
// table and column. Used by inference to guide relationship modeling.
type ForeignKey struct {
	Table  string `yaml:"table" json:"table"`
	Column string `yaml:"column" json:"column"`
}

// Source records provenance metadata indicating which database engine,
// database instance, and table the schema was introspected from.
type Source struct {
	Dialect    string    `yaml:"dialect" json:"dialect"`
	Database   string    `yaml:"database,omitempty" json:"database,omitempty"`
	Table      string    `yaml:"table" json:"table"`
	CapturedAt time.Time `yaml:"captured_at,omitempty" json:"captured_at,omitempty"`
}

// Column represents an introspected database column along with generator
// decisions. Fact fields are refreshed on re-introspect while decision fields
// are preserved across updates.
type Column struct {
	// Facts (introspected from database catalogs, refreshed on update).
	Name             string      `yaml:"name" json:"name"`
	SQLType          string      `yaml:"sql_type" json:"sql_type"`
	MaxLength        *int64      `yaml:"max_length,omitempty" json:"max_length,omitempty"`
	NumericPrecision *int64      `yaml:"numeric_precision,omitempty" json:"numeric_precision,omitempty"`
	NumericScale     *int64      `yaml:"numeric_scale,omitempty" json:"numeric_scale,omitempty"`
	Nullable         bool        `yaml:"nullable" json:"nullable"`
	PrimaryKey       bool        `yaml:"primary_key,omitempty" json:"primary_key,omitempty"`
	Unique           bool        `yaml:"unique,omitempty" json:"unique,omitempty"`
	Default          string      `yaml:"default,omitempty" json:"default,omitempty"`
	Identity         bool        `yaml:"identity,omitempty" json:"identity,omitempty"`
	EnumValues       []string    `yaml:"enum_values,omitempty,flow" json:"enum_values,omitempty"`
	References       *ForeignKey `yaml:"references,omitempty" json:"references,omitempty"`
	Comment          string      `yaml:"comment,omitempty" json:"comment,omitempty"`
	Ordinal          int         `yaml:"ordinal,omitempty" json:"ordinal,omitempty"`

	// Decisions (generator configuration chosen by rules or human review).
	Type           string   `yaml:"type" json:"type"`
	Values         []string `yaml:"values,omitempty,flow" json:"values,omitempty"`
	InferredBy     string   `yaml:"inferred_by,omitempty" json:"inferred_by,omitempty"`
	NullPercentage int64    `yaml:"null_percentage,omitempty" json:"null_percentage,omitempty"`
	Formula        string   `yaml:"formula,omitempty" json:"formula,omitempty"`
	Min            string   `yaml:"min,omitempty" json:"min,omitempty"`
	Max            string   `yaml:"max,omitempty" json:"max,omitempty"`
	Format         string   `yaml:"format,omitempty" json:"format,omitempty"`
	Blank          int64    `yaml:"blank,omitempty" json:"blank,omitempty"`
	Decimals       int64    `yaml:"decimals,omitempty" json:"decimals,omitempty"`
	SelectionStyle string   `yaml:"selectionStyle,omitempty" json:"selectionStyle,omitempty"`
	Distribution   string   `yaml:"distribution,omitempty" json:"distribution,omitempty"`
	StartPosition  int64    `yaml:"start_position,omitempty" json:"startPosition,omitempty"`
	EndPosition    int64    `yaml:"end_position,omitempty" json:"endPosition,omitempty"`
	Alignment      string   `yaml:"alignment,omitempty" json:"alignment,omitempty"`
	PadCharacter   rune     `yaml:"pad_character,omitempty" json:"padCharacter,omitempty"`
}

// Schema represents a complete database table schema specification.
type Schema struct {
	Version string   `yaml:"version" json:"version"`
	Kind    string   `yaml:"kind" json:"kind"`
	Name    string   `yaml:"name" json:"name"`
	Source  Source   `yaml:"source" json:"source"`
	Columns []Column `yaml:"columns" json:"columns"`
}

// RenderOptions controls schema translation into mockagen generation specs.
type RenderOptions struct {
	// Force permits rendering even when one or more columns have unresolved
	// generator types.
	Force bool
}

// Render translates s into a mockagen.MockagenConfig, enforcing structural
// couplings: non-nullable columns have NullPercentage forced to 0, identity
// columns are omitted, unresolved types fail unless forced, and foreign-key
// or unique constraints emit warning comments since generation cannot
// guarantee cross-table or set-wide consistency.
func (s Schema) Render(opts RenderOptions) (mockagen.MockagenConfig, error) {
	return Render(s, opts)
}

// Render translates s into a mockagen.MockagenConfig.
func Render(s Schema, opts RenderOptions) (mockagen.MockagenConfig, error) {
	cfg := mockagen.MockagenConfig{
		Version:         s.Version,
		Kind:            "Spec",
		Name:            s.Name,
		Table:           s.Source.Table,
		Dialect:         s.Source.Dialect,
		FileFormat:      "sql",
		NumberOfRecords: 1000,
		IncludeHeader:   true,
	}

	// Column order is not cosmetic here: the rendered spec is file_format
	// "sql", and a bulk load maps CSV fields by position on mssql, so a
	// reordered column list produces a silently misaligned load rather than
	// an error. Ordinal is the database's own order, so prefer it wherever
	// introspection filled it in; a hand-written schema leaves it zero and
	// keeps the order its author wrote.
	ordered := slices.Clone(s.Columns)
	if slices.ContainsFunc(ordered, func(c Column) bool { return c.Ordinal != 0 }) {
		slices.SortStableFunc(ordered, func(a, b Column) int {
			return cmp.Compare(a.Ordinal, b.Ordinal)
		})
	}

	cols := make([]mockagen.MockagenColumn, 0, len(ordered))
	for _, c := range ordered {
		// Identity columns are generated by database sequence engines and must
		// be excluded from generation specs so INSERT statements omit them.
		if c.Identity {
			continue
		}

		// Trimmed, because a stray space is a miss in mapToFaker and so is
		// indistinguishable from a typo at generation time.
		colType := strings.TrimSpace(c.Type)
		if colType == "" {
			if !opts.Force {
				return mockagen.MockagenConfig{}, fmt.Errorf(
					"%w: column %q has unresolved generator type (use Force to render anyway)",
					ErrUnresolvedType, c.Name)
			}
		} else {
			t, ok := types.Lookup(colType)
			if !ok {
				// Not gated behind Force: Force exists to render a schema with
				// columns still to be decided, not to ship a type that cannot
				// generate. A rendered spec naming an unknown type is
				// indistinguishable from a working one until someone reads the
				// data.
				return mockagen.MockagenConfig{}, fmt.Errorf(
					"%w: column %q has type %q", ErrUnknownType, c.Name, colType)
			}
			if t.RequiresValues && len(c.Values) == 0 && len(c.EnumValues) == 0 {
				return mockagen.MockagenConfig{}, fmt.Errorf(
					"%w: column %q has type %q", ErrMissingValues, c.Name, colType)
			}
		}

		// A value list the human did not override falls back to the labels
		// introspection found. Re-introspecting refreshes enum_values and
		// leaves values alone, so without this a schema whose enum gained a
		// label renders the stale list - or none at all.
		values := c.Values
		if len(values) == 0 {
			values = c.EnumValues
		}

		col := mockagen.MockagenColumn{
			Name:           c.Name,
			Type:           colType,
			Formula:        c.Formula,
			Min:            c.Min,
			Max:            c.Max,
			Format:         c.Format,
			Blank:          c.Blank,
			Values:         values,
			Decimals:       c.Decimals,
			SelectionStyle: c.SelectionStyle,
			Distribution:   c.Distribution,
			StartPosition:  c.StartPosition,
			EndPosition:    c.EndPosition,
			Alignment:      c.Alignment,
			PadCharacter:   c.PadCharacter,
		}

		// Enforce nullable coupling: a non-nullable database column must never
		// generate NULL values regardless of human-configured percentage.
		if !c.Nullable {
			col.NullPercentage = 0
		} else {
			col.NullPercentage = c.NullPercentage
		}

		// Surface unhonoured database constraints with explicit warning
		// comments. Schema.Warnings reports the same list for callers that
		// need to show it rather than serialize it.
		if warnings := columnWarnings(c); len(warnings) > 0 {
			col.Comment = "WARNING: " + strings.Join(warnings, "; ")
		}

		cols = append(cols, col)
	}

	cfg.Columns = cols
	return cfg, nil
}

// Warnings reports the constraints this schema records but generation cannot
// honour, one line per column, so a caller can show them.
//
// Render also writes these onto each column's Comment, which carries them
// into a rendered spec file - but nothing reads Comment during generation, so
// a user who renders and generates in one step would never see them. This is
// how the schema command surfaces them instead.
func (s Schema) Warnings() []string {
	var out []string
	for _, c := range s.Columns {
		if c.Identity {
			continue
		}
		for _, w := range columnWarnings(c) {
			out = append(out, fmt.Sprintf("column %q: %s", c.Name, w))
		}
	}
	return out
}

// columnWarnings lists what generation cannot guarantee for c. Shared so that
// Warnings and Render cannot drift into reporting different things.
func columnWarnings(c Column) []string {
	var warnings []string
	if c.References != nil {
		warnings = append(warnings, fmt.Sprintf(
			"foreign key referencing %s.%s cannot be guaranteed by generator",
			c.References.Table, c.References.Column))
	}
	if c.Unique {
		warnings = append(warnings, "unique constraint cannot be guaranteed by generator")
	}
	return warnings
}

// Unmarshal parses raw schema data in YAML or JSON format determined by ext.
// The extension is lowercased first: a file named Schema.YAML parses exactly
// like one named schema.yaml, and rejecting it for its spelling would be an
// error about nothing.
func Unmarshal(data []byte, ext string) (Schema, error) {
	switch strings.ToLower(ext) {
	case ".yaml", ".yml":
		var s Schema
		if err := yaml.Unmarshal(data, &s); err != nil {
			return Schema{}, err
		}
		if s.Kind != "" && !strings.EqualFold(s.Kind, "schema") {
			return Schema{}, fmt.Errorf("%w: %q", ErrUnknownKind, s.Kind)
		}
		return s, nil
	case ".json":
		var s Schema
		if err := json.Unmarshal(data, &s); err != nil {
			return Schema{}, err
		}
		if s.Kind != "" && !strings.EqualFold(s.Kind, "schema") {
			return Schema{}, fmt.Errorf("%w: %q", ErrUnknownKind, s.Kind)
		}
		return s, nil
	default:
		return Schema{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

// Marshal encodes s as formatted YAML or JSON based on ext, which is
// lowercased first for the reason given on Unmarshal.
func Marshal(s Schema, ext string) ([]byte, error) {
	switch strings.ToLower(ext) {
	case ".yaml", ".yml":
		return yaml.Marshal(&s)
	case ".json":
		return json.MarshalIndent(&s, "", "  ")
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

// Load reads and parses a schema file from path p.
func Load(p string) (Schema, error) {
	ext := strings.ToLower(filepath.Ext(p))
	allowed := []string{".yaml", ".yml", ".json"}
	if !slices.Contains(allowed, ext) {
		return Schema{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Schema{}, err
	}
	return Unmarshal(data, ext)
}

// Save serializes s and writes it to path p.
func Save(p string, s Schema) error {
	ext := strings.ToLower(filepath.Ext(p))
	data, err := Marshal(s, ext)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
