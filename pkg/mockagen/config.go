package mockagen

// MockagenConfig -
type MockagenConfig struct {
	NumberOfRecords int              `yaml:"num_rows" json:"num_rows" toml:"num_rows"`
	FileFormat      string           `yaml:"file_format" json:"file_format" toml:"file_format"`
	Name            string           `yaml:"name" json:"name" toml:"name"`
	IncludeHeader   bool             `yaml:"include_header" json:"include_header" toml:"include_header"`
	Columns         []MockagenColumn `yaml:"columns" json:"columns" toml:"columns"`
}

// MockagenColumn -
type MockagenColumn struct {
	Name           string   `yaml:"name" json:"name" toml:"name"`
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
