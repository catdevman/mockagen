package mockagen

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/catdevman/mockagen/pkg/mockagen/types"
	yaml "gopkg.in/yaml.v3"
)

var (
	// ErrUnsupportedFileType indicates the file extension is not supported.
	ErrUnsupportedFileType = errors.New("unsupported file type")

	// ErrIssueOpeningFile indicates the config file could not be read.
	ErrIssueOpeningFile = errors.New("issue opening file")

	// ErrDecodingConfig indicates syntax or structure errors in the config.
	ErrDecodingConfig = errors.New("issue decoding config file")

	// ErrUnknownKind indicates an envelope kind other than Spec.
	ErrUnknownKind = errors.New("unknown config kind")

	// ErrUnknownColumnType indicates a column specifies a generator type
	// not registered in the types catalog.
	ErrUnknownColumnType = errors.New("unknown column type")

	// ErrMissingValues reports a column whose type needs a value list and
	// has none. Rejected at load because the alternative is a panic from a
	// generation worker: the faker tag becomes a bare "oneof:", which faker
	// refuses, and generateFakes turns a faker error into a panic.
	ErrMissingValues = errors.New("column type requires values")
)

// envelope captures the metadata envelope on configuration files without
// decoding the complete body.
type envelope struct {
	Version string `yaml:"version" json:"version" toml:"version"`
	Kind    string `yaml:"kind" json:"kind" toml:"kind"`
}

// Load reads a configuration file from p, inspects its {version, kind}
// envelope to verify the kind is a generation Spec, decodes the specification,
// and ensures every column type is registered in mockagen's type catalog.
func Load(p string) (MockagenConfig, error) {
	allowedExt := []string{".yaml", ".yml", ".json", ".toml"}
	ext := filepath.Ext(p)
	if !slices.Contains(allowedExt, ext) {
		return MockagenConfig{}, fmt.Errorf("%w: %s", ErrUnsupportedFileType, ext)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return MockagenConfig{}, fmt.Errorf("%w: %w", ErrIssueOpeningFile, err)
	}

	// Pass 1: decode envelope to determine kind.
	var env envelope
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &env); err != nil {
			return MockagenConfig{}, fmt.Errorf("%w: %w", ErrDecodingConfig, err)
		}
	case ".json":
		if err := json.Unmarshal(data, &env); err != nil {
			return MockagenConfig{}, fmt.Errorf("%w: %w", ErrDecodingConfig, err)
		}
	case ".toml":
		if _, err := toml.Decode(string(data), &env); err != nil {
			return MockagenConfig{}, fmt.Errorf("%w: %w", ErrDecodingConfig, err)
		}
	}

	if env.Kind != "" && !strings.EqualFold(env.Kind, "spec") {
		return MockagenConfig{}, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}

	// Pass 2: decode the concrete generation spec.
	var cfg MockagenConfig
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return MockagenConfig{}, fmt.Errorf("%w: %w", ErrDecodingConfig, err)
		}
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return MockagenConfig{}, fmt.Errorf("%w: %w", ErrDecodingConfig, err)
		}
	case ".toml":
		if _, err := toml.Decode(string(data), &cfg); err != nil {
			return MockagenConfig{}, fmt.Errorf("%w: %w", ErrDecodingConfig, err)
		}
	}

	if cfg.Kind == "" || strings.EqualFold(cfg.Kind, "spec") {
		cfg.Kind = "Spec"
	}

	// Guard against unrecognised generator types which would otherwise
	// silently degrade to random string generation.
	for _, col := range cfg.Columns {
		t, ok := types.Lookup(col.Type)
		if ok && t.RequiresValues && len(col.Values) == 0 {
			// Caught here rather than at generation: a "Custom List" with no
			// values builds the faker tag "oneof:" with no arguments, faker
			// rejects it, and generateFakes panics inside a worker - so the
			// user would get a stack trace where this CLI prints
			// "mockagen: ..." for every other bad config value.
			return MockagenConfig{}, fmt.Errorf(
				"%w: column %q has type %q but no values",
				ErrMissingValues, col.Name, col.Type)
		}
		if !ok {
			return MockagenConfig{}, fmt.Errorf("%w: column %q has unknown type %q",
				ErrUnknownColumnType, col.Name, col.Type)
		}
	}

	return cfg, nil
}

// LoadConfig wraps Load to preserve backwards-compatible panic semantics
// pinned by historical unit tests.
func LoadConfig(p string) MockagenConfig {
	cfg, err := Load(p)
	if err != nil {
		if errors.Is(err, ErrUnsupportedFileType) {
			_, file := filepath.Split(p)
			fmt.Println("File type was:", filepath.Ext(file))
			panic("Config files can only be yaml, json, or toml.")
		}
		if errors.Is(err, ErrIssueOpeningFile) {
			panic("Issue opening file.")
		}
		if errors.Is(err, ErrDecodingConfig) {
			panic("Issue decoding config file")
		}
		panic(err.Error())
	}
	return cfg
}
