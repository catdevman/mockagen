package mockagen

// MockagenConfig specifies how mock data is generated and formatted.
type MockagenConfig struct {
	// Version identifies the schema format version (e.g. mockagen/v1).
	Version string `yaml:"version" json:"version" toml:"version"`
	// Kind discriminates the file type (Spec, Schema, FieldMap) so that
	// envelope decoders dispatch to the correct concrete schema structure.
	Kind            string `yaml:"kind" json:"kind" toml:"kind"`
	NumberOfRecords int    `yaml:"num_rows" json:"num_rows" toml:"num_rows"`
	FileFormat      string `yaml:"file_format" json:"file_format" toml:"file_format"`
	Name            string `yaml:"name" json:"name" toml:"name"`
	IncludeHeader   bool   `yaml:"include_header" json:"include_header" toml:"include_header"`
	// Tag is the fluentd tag written on every line by the "fluent" file
	// format, and is ignored by every other format. Defaults to the config
	// name when empty.
	Tag string `yaml:"tag" json:"tag" toml:"tag"`
	// Dialect selects which database's spelling the "sql" file format
	// writes - postgres, mysql, sqlite or mssql - and is ignored by every
	// other format. Defaults to postgres when empty.
	Dialect string `yaml:"dialect" json:"dialect" toml:"dialect"`
	// Mode selects what the "sql" file format writes: "insert" (the default)
	// for INSERT statements, or "bulk" for a CSV data file plus a script
	// that bulk-loads it with the dialect's native loader. Ignored by every
	// other format. Deliberately not named sql_mode, which is a real MySQL
	// server variable.
	Mode string `yaml:"mode" json:"mode" toml:"mode"`
	// Table is the table the "sql" file format inserts into, optionally
	// schema-qualified ("dbo.users"). Defaults to the config name with its
	// spaces turned into underscores.
	Table   string           `yaml:"table" json:"table" toml:"table"`
	Columns []MockagenColumn `yaml:"columns" json:"columns" toml:"columns"`
}

// MockagenColumn specifies how values for an individual column are generated.
type MockagenColumn struct {
	Name string `yaml:"name" json:"name" toml:"name"`
	// Comment carries warnings or notes about constraints the generator
	// cannot guarantee (such as foreign key or unique constraints).
	Comment        string   `yaml:"comment,omitempty" json:"comment,omitempty" toml:"comment,omitempty"`
	NullPercentage int64    `yaml:"null_percentage" json:"null_percentage" toml:"null_percentage"`
	Type           string   `yaml:"type" json:"type" toml:"type"`
	Formula        string   `yaml:"formula" json:"formula" toml:"formula"`
	Min            string   `yaml:"min" json:"min" toml:"min"`
	Max            string   `yaml:"max" json:"max" toml:"max"`
	Format         string   `yaml:"format" json:"format" toml:"format"`
	Blank          int64    `yaml:"blank" json:"blank" toml:"blank"`
	Values         []string `yaml:"values" json:"values" toml:"values"`
	Decimals       int64    `yaml:"decimals" json:"decimals" toml:"decimals"`
	SelectionStyle string   `yaml:"selectionStyle" json:"selectionStyle" toml:"selectionStyle"`
	Distribution   string   `yaml:"distribution" json:"distribution" toml:"distribution"`
	StartPosition  int64    `yaml:"start_position" json:"startPosition" toml:"start_position"`
	EndPosition    int64    `yaml:"end_position" json:"endPosition" toml:"end_position"`
	Alignment      string   `yaml:"alignment" json:"alignment" toml:"alignment"`
	PadCharacter   rune     `yaml:"pad_character" json:"padCharacter" toml:"pad_character"`
}
