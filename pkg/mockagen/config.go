package mockagen

// MockagenConfig -
type MockagenConfig struct {
	NumberOfRecords int              `yaml:"num_rows" json:"num_rows"`
	FileFormat      string           `yaml:"file_format" json:"file_format"`
	Name            string           `yaml:"name" json:"name"`
	IncludeHeader   bool             `yaml:"include_header" json:"include_header"`
	Columns         []MockagenColumn `yaml:"columns" json:"columns"`
}

// MockagenColumn -
type MockagenColumn struct {
	Name           string   `yaml:"name" json:"name"`
	NullPercentage int64    `yaml:"null_percentage" json:"null_percentage"`
	Type           string   `yaml:"type" json:"type"`
	Formula        string   `yaml:"formula" json:"formula"`
	Min            string   `yaml:"min" json:"min"`
	Max            string   `yaml:"max" json:"max"`
	Format         string   `yaml:"format" json:"format"`
	Blank          int64    `yaml:"blank" json:"blank"`
	Values         []string `yaml:"values" json:"values"`
	Decimals       int64    `yaml:"decimals" json:"decimals"`
	SelectionStyle string   `yaml:"selectionStyle" json:"selectionStyle"`
	Distribution   string   `yaml:"distribution" json:"distribution"`
	StartPosition  int64    `yaml:"start_position" json:"startPosition"`
	EndPosition    int64    `yaml:"end_position" json:"endPosition"`
	Alignment      string   `yaml:"alignment" json:"alignment"`
	PadCharacter   rune     `yaml:"pad_character" json:"padCharacter"`
}
