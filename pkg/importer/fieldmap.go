// Package importer maps a source file onto a table schema and, later, loads
// the rows. The field map is the contract between those two: which source
// field feeds which column, and what happens to the value on the way.
package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// FieldMap is a generated description of one source file against one schema.
// It is YAML or JSON because a list of single-key transform maps is a bad
// TOML shape, and because these files are written by the tool.
type FieldMap struct {
	Version  string         `yaml:"version" json:"version"`
	Kind     string         `yaml:"kind" json:"kind"`
	Name     string         `yaml:"name" json:"name"`
	Schema   string         `yaml:"schema" json:"schema"`
	Source   SourceSpec     `yaml:"source" json:"source"`
	Fields   []Field        `yaml:"fields,omitempty" json:"fields,omitempty"`
	Omitted  []string       `yaml:"omitted,omitempty" json:"omitted,omitempty"`
	Unmapped UnmappedPolicy `yaml:"unmapped" json:"unmapped"`
	OnError  string         `yaml:"on_error" json:"on_error"`
}

// SourceSpec is the shape the map was built against. Fields, in order, is
// the header Propose saw. Import compares a later file to that list before
// reading a row, so a swapped pair fails at open instead of loading values
// into the wrong columns. A headerless file stores Indexes and leaves Fields
// empty; the numbers are the only names those columns have.
type SourceSpec struct {
	Format    string   `yaml:"format" json:"format"`
	HasHeader bool     `yaml:"has_header" json:"has_header"`
	Delimiter string   `yaml:"delimiter,omitempty" json:"delimiter,omitempty"`
	Encoding  string   `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	Fields    []string `yaml:"fields,omitempty,flow" json:"fields,omitempty"`
	Indexes   []int    `yaml:"indexes,omitempty,flow" json:"indexes,omitempty"`
}

// UnmappedPolicy says what a source column with no target means. Target
// columns are not governed here: a schema column is mapped, omitted, or
// unseen, and that status comes from the schema's nullability.
type UnmappedPolicy struct {
	Source string `yaml:"source" json:"source"`
}

// Field is one target column's value. Exactly one origin is set: Source,
// Sources plus Join, Coalesce, Constant, or Object. Join is a pointer so
// an empty separator (concatenate with nothing) survives omitempty, which
// would drop a plain empty string and turn a legal join into
// ErrSourcesNeedJoin.
type Field struct {
	Target     string      `yaml:"target" json:"target"`
	Source     string      `yaml:"source,omitempty" json:"source,omitempty"`
	Sources    []string    `yaml:"sources,omitempty" json:"sources,omitempty"`
	Join       *string     `yaml:"join,omitempty" json:"join,omitempty"`
	Coalesce   []string    `yaml:"coalesce,omitempty" json:"coalesce,omitempty"`
	Constant   *string     `yaml:"constant,omitempty" json:"constant,omitempty"`
	Object     *Object     `yaml:"object,omitempty" json:"object,omitempty"`
	Transforms []Transform `yaml:"transforms,omitempty" json:"transforms,omitempty"`
	Format     string      `yaml:"format,omitempty" json:"format,omitempty"`
	NullIf     []string    `yaml:"null_if,omitempty" json:"null_if,omitempty"`
	Default    *string     `yaml:"default,omitempty" json:"default,omitempty"`
}

// Transform is one chain step. The YAML form is a single-key map, so the
// editor can treat the chain as a list with one argument slot per step.
// Re is filled by Compile and ignored by the codecs; Apply compiles again
// when it is nil so a test can run a transform without a Plan.
type Transform struct {
	Name string

	// Trim. HasCutset distinguishes `trim: {}` (whitespace) from
	// `trim: {cutset: ""}`, which trims nothing.
	Cutset    string
	HasCutset bool

	// Case is upper, lower, or title.
	Case string

	// Replace and regex_replace. Pattern is the regex; From is the literal.
	From    string
	To      string
	Pattern string

	// Map. Pairs is the finished lookup, in file order. Accept is the
	// seeded `to` list and means the lookup is unfinished. LookupDefault
	// is the pair default, distinct from Field.Default.
	Pairs         []Pair
	Accept        []string
	LookupDefault *string

	// Pad. Left is pad_left; right is the other one.
	Left  bool
	Width int
	With  string

	re *regexp.Regexp
}

// Pair is one incoming code and the value that replaces it.
type Pair struct {
	From string
	To   string
}

// Diagnostic is one compile failure. Column is empty when the fault is not
// about a single target. Err is a sentinel, sometimes wrapped with the
// pattern or the value that failed.
type Diagnostic struct {
	Column string
	Err    error
}

// Error prefixes the column so a multi-column report can be read straight
// off stderr.
func (d Diagnostic) Error() string {
	if d.Column == "" {
		return d.Err.Error()
	}
	return fmt.Sprintf("column %q: %s", d.Column, d.Err.Error())
}

// Unmarshal decodes a field map. ext selects YAML or JSON and is lowercased
// first: a file named Map.YAML is the same file as map.yaml, and rejecting
// it for its spelling would be an error about nothing. Unknown keys are
// rejected so a removed key such as `required` cannot sit in the file
// looking like it still does something.
func Unmarshal(data []byte, ext string) (FieldMap, error) {
	var m FieldMap
	switch strings.ToLower(ext) {
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&m); err != nil {
			return FieldMap{}, err
		}
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			return FieldMap{}, err
		}
	default:
		return FieldMap{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
	if m.Kind != "" && !strings.EqualFold(m.Kind, "fieldmap") {
		return FieldMap{}, fmt.Errorf("%w: %q", ErrUnknownKind, m.Kind)
	}
	if m.Kind == "" {
		m.Kind = "FieldMap"
	}
	return m, nil
}

// Marshal encodes m. The extension is lowercased first for the same reason
// as Unmarshal.
func Marshal(m FieldMap, ext string) ([]byte, error) {
	switch strings.ToLower(ext) {
	case ".yaml", ".yml":
		return yaml.Marshal(&m)
	case ".json":
		return json.MarshalIndent(&m, "", "  ")
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

// Load reads a field map from path. The whole file is read because a map is
// config-sized; the data file it describes is not, and is streamed elsewhere.
func Load(path string) (FieldMap, error) {
	ext := strings.ToLower(filepath.Ext(path))
	allowed := []string{".yaml", ".yml", ".json"}
	if !slices.Contains(allowed, ext) {
		return FieldMap{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FieldMap{}, err
	}
	return Unmarshal(data, ext)
}

// Save writes m to path, creating no parent directories. Callers that take a
// path from a flag create the directory themselves, the same way generation
// does, so a typo in the directory fails in one place.
func Save(path string, m FieldMap) error {
	ext := strings.ToLower(filepath.Ext(path))
	data, err := Marshal(m, ext)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// SchemaRef makes the schema path relative to the written map, so the
// two files can move together. stdout has no directory, so the path is
// kept as the user typed it. Rel fails when the paths are on different
// volumes; the absolute path is then what a later load can still open.
func SchemaRef(outPath, schemaPath string) string {
	if outPath == "" {
		return schemaPath
	}
	absSchema, err := filepath.Abs(schemaPath)
	if err != nil {
		return schemaPath
	}
	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return schemaPath
	}
	rel, err := filepath.Rel(filepath.Dir(absOut), absSchema)
	if err != nil {
		return absSchema
	}
	return rel
}

// ResolveSchema joins schemaRef to the directory of the map file. An absolute
// schemaRef is returned as written. The map file is the stable location; the
// process working directory is not, and a relative schema path resolved
// against it breaks as soon as the command is run from somewhere else.
func ResolveSchema(mapFile, schemaRef string) string {
	if schemaRef == "" || filepath.IsAbs(schemaRef) {
		return schemaRef
	}
	return filepath.Join(filepath.Dir(mapFile), schemaRef)
}

// UnmarshalYAML reads one chain step. A transform is a single-key map on
// purpose; decoding it through map[string]any would reorder the lookup pairs
// and collapse the one argument slot the chain editor is built around.
func (t *Transform) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return fmt.Errorf("%w: transform must be a single-key map", ErrUnknownTransform)
	}
	key := n.Content[0].Value
	val := n.Content[1]
	parsed, err := decodeTransform(key, val)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// MarshalYAML writes the single-key form. Lookup pairs are emitted in file
// order; a Go map would shuffle them and the golden YAML would flap.
func (t Transform) MarshalYAML() (any, error) {
	val, err := t.yamlValue()
	if err != nil {
		return nil, err
	}
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			strNode(t.Name),
			val,
		},
	}, nil
}

func (t Transform) yamlValue() (*yaml.Node, error) {
	switch t.Name {
	case "trim":
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if t.HasCutset {
			m.Content = []*yaml.Node{strNode("cutset"), strNode(t.Cutset)}
		}
		return m, nil
	case "case":
		return strNode(t.Case), nil
	case "replace":
		return mappingNode(
			strNode("from"), strNode(t.From),
			strNode("to"), strNode(t.To),
		), nil
	case "regex_replace":
		return mappingNode(
			strNode("pattern"), strNode(t.Pattern),
			strNode("to"), strNode(t.To),
		), nil
	case "map":
		return t.yamlLookup(), nil
	case "pad_left", "pad_right":
		return mappingNode(
			strNode("to"), intNode(t.Width),
			strNode("with"), strNode(t.With),
		), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownTransform, t.Name)
	}
}

func (t Transform) yamlLookup() *yaml.Node {
	if len(t.Accept) > 0 && len(t.Pairs) == 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, v := range t.Accept {
			seq.Content = append(seq.Content, strNode(v))
		}
		return mappingNode(strNode("to"), seq)
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for _, p := range t.Pairs {
		m.Content = append(m.Content, strNode(p.From), strNode(p.To))
	}
	if t.LookupDefault != nil {
		m.Content = append(m.Content, strNode("default"), strNode(*t.LookupDefault))
	}
	return m
}

// UnmarshalJSON reads the same single-key object as UnmarshalYAML. JSON
// object order is preserved by walking tokens; decoding into a map would
// scramble a lookup.
func (t *Transform) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%w: transform must be a single-key map", ErrUnknownTransform)
	}
	if !dec.More() {
		return fmt.Errorf("%w: transform must be a single-key map", ErrUnknownTransform)
	}
	keyTok, err := dec.Token()
	if err != nil {
		return err
	}
	key, ok := keyTok.(string)
	if !ok {
		return fmt.Errorf("%w: transform must be a single-key map", ErrUnknownTransform)
	}
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("%w: transform must be a single-key map", ErrUnknownTransform)
	}
	parsed, err := decodeTransformJSON(key, raw)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// MarshalJSON writes one key. Pairs are written in order so a saved JSON map
// round-trips the lookup a person edited.
func (t Transform) MarshalJSON() ([]byte, error) {
	val, err := t.jsonValue()
	if err != nil {
		return nil, err
	}
	key, err := json.Marshal(t.Name)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(key)+len(val)+3)
	out = append(out, '{')
	out = append(out, key...)
	out = append(out, ':')
	out = append(out, val...)
	out = append(out, '}')
	return out, nil
}

func (t Transform) jsonValue() ([]byte, error) {
	switch t.Name {
	case "trim":
		if t.HasCutset {
			return json.Marshal(struct {
				Cutset string `json:"cutset"`
			}{t.Cutset})
		}
		return []byte("{}"), nil
	case "case":
		return json.Marshal(t.Case)
	case "replace":
		return json.Marshal(struct {
			From string `json:"from"`
			To   string `json:"to"`
		}{t.From, t.To})
	case "regex_replace":
		return json.Marshal(struct {
			Pattern string `json:"pattern"`
			To      string `json:"to"`
		}{t.Pattern, t.To})
	case "map":
		return t.jsonLookup()
	case "pad_left", "pad_right":
		return json.Marshal(struct {
			To   int    `json:"to"`
			With string `json:"with"`
		}{t.Width, t.With})
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownTransform, t.Name)
	}
}

func (t Transform) jsonLookup() ([]byte, error) {
	if len(t.Accept) > 0 && len(t.Pairs) == 0 {
		body, err := json.Marshal(t.Accept)
		if err != nil {
			return nil, err
		}
		return []byte(`{"to":` + string(body) + `}`), nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range t.Pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(p.From)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(p.To)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	if t.LookupDefault != nil {
		if len(t.Pairs) > 0 {
			b.WriteByte(',')
		}
		vb, err := json.Marshal(*t.LookupDefault)
		if err != nil {
			return nil, err
		}
		b.WriteString(`"default":`)
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func decodeTransform(key string, val *yaml.Node) (Transform, error) {
	if key == "expr" {
		return Transform{}, ErrExprReserved
	}
	switch key {
	case "trim":
		return decodeTrimYAML(val)
	case "case":
		if val.Kind != yaml.ScalarNode {
			return Transform{}, fmt.Errorf("%w: case", ErrUnknownCase)
		}
		return Transform{Name: "case", Case: val.Value}, nil
	case "replace":
		var body struct {
			From string `yaml:"from"`
			To   string `yaml:"to"`
		}
		if err := val.Decode(&body); err != nil {
			return Transform{}, err
		}
		return Transform{Name: "replace", From: body.From, To: body.To}, nil
	case "regex_replace":
		var body struct {
			Pattern string `yaml:"pattern"`
			To      string `yaml:"to"`
		}
		if err := val.Decode(&body); err != nil {
			return Transform{}, err
		}
		return Transform{Name: "regex_replace", Pattern: body.Pattern, To: body.To}, nil
	case "map":
		return decodeLookupYAML(val)
	case "pad_left", "pad_right":
		var body struct {
			To   int    `yaml:"to"`
			With string `yaml:"with"`
		}
		if err := val.Decode(&body); err != nil {
			return Transform{}, err
		}
		return Transform{Name: key, Left: key == "pad_left", Width: body.To, With: body.With}, nil
	default:
		return Transform{}, fmt.Errorf("%w: %s", ErrUnknownTransform, key)
	}
}

func decodeTrimYAML(val *yaml.Node) (Transform, error) {
	t := Transform{Name: "trim"}
	if val.Kind != yaml.MappingNode {
		return Transform{}, fmt.Errorf("%w: trim", ErrUnknownTransform)
	}
	if len(val.Content) == 0 {
		return t, nil
	}
	if len(val.Content) != 2 || val.Content[0].Value != "cutset" {
		return Transform{}, fmt.Errorf("%w: trim", ErrUnknownTransform)
	}
	t.HasCutset = true
	t.Cutset = val.Content[1].Value
	return t, nil
}

func decodeLookupYAML(val *yaml.Node) (Transform, error) {
	if val.Kind != yaml.MappingNode {
		return Transform{}, fmt.Errorf("%w: map", ErrUnknownTransform)
	}
	t := Transform{Name: "map"}
	for i := 0; i+1 < len(val.Content); i += 2 {
		k := val.Content[i]
		v := val.Content[i+1]
		if k.Value == "to" && v.Kind == yaml.SequenceNode {
			for _, item := range v.Content {
				t.Accept = append(t.Accept, item.Value)
			}
			continue
		}
		if k.Value == "default" && v.Kind == yaml.ScalarNode {
			s := v.Value
			t.LookupDefault = &s
			continue
		}
		if v.Kind != yaml.ScalarNode {
			return Transform{}, fmt.Errorf("%w: map", ErrUnknownTransform)
		}
		t.Pairs = append(t.Pairs, Pair{From: k.Value, To: v.Value})
	}
	return t, nil
}

func decodeTransformJSON(key string, raw json.RawMessage) (Transform, error) {
	if key == "expr" {
		return Transform{}, ErrExprReserved
	}
	switch key {
	case "trim":
		var body struct {
			Cutset *string `json:"cutset"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			return Transform{}, fmt.Errorf("%w: trim", ErrUnknownTransform)
		}
		t := Transform{Name: "trim"}
		if body.Cutset != nil {
			t.HasCutset = true
			t.Cutset = *body.Cutset
		}
		return t, nil
	case "case":
		var mode string
		if err := json.Unmarshal(raw, &mode); err != nil {
			return Transform{}, fmt.Errorf("%w: case", ErrUnknownCase)
		}
		return Transform{Name: "case", Case: mode}, nil
	case "replace":
		var body struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return Transform{}, err
		}
		return Transform{Name: "replace", From: body.From, To: body.To}, nil
	case "regex_replace":
		var body struct {
			Pattern string `json:"pattern"`
			To      string `json:"to"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return Transform{}, err
		}
		return Transform{Name: "regex_replace", Pattern: body.Pattern, To: body.To}, nil
	case "map":
		return decodeLookupJSON(raw)
	case "pad_left", "pad_right":
		var body struct {
			To   int    `json:"to"`
			With string `json:"with"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return Transform{}, err
		}
		return Transform{Name: key, Left: key == "pad_left", Width: body.To, With: body.With}, nil
	default:
		return Transform{}, fmt.Errorf("%w: %s", ErrUnknownTransform, key)
	}
}

func decodeLookupJSON(raw json.RawMessage) (Transform, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return Transform{}, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return Transform{}, fmt.Errorf("%w: map", ErrUnknownTransform)
	}
	t := Transform{Name: "map"}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return Transform{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return Transform{}, fmt.Errorf("%w: map", ErrUnknownTransform)
		}
		if key == "to" {
			var accept []string
			if err := dec.Decode(&accept); err != nil {
				return Transform{}, fmt.Errorf("%w: map", ErrUnknownTransform)
			}
			t.Accept = accept
			continue
		}
		var val string
		if err := dec.Decode(&val); err != nil {
			return Transform{}, fmt.Errorf("%w: map", ErrUnknownTransform)
		}
		if key == "default" {
			s := val
			t.LookupDefault = &s
			continue
		}
		t.Pairs = append(t.Pairs, Pair{From: key, To: val})
	}
	return t, nil
}

func strNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func intNode(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", v)}
}

func mappingNode(pairs ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: pairs}
}
