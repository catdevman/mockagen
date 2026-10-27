package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/catdevman/mockagen/pkg/schema"
	yaml "gopkg.in/yaml.v3"
)

// schemaText is the column a leaf is checked against. A leaf has no
// sql_type of its own; the parent column's checks run on the document.
var schemaText = schema.Column{SQLType: "text"}

// Object is the object origin: several cells become one JSON document.
// Key order is the file order, because a Go map would reshuffle a document
// the next save. A null or blank leaf is omitted rather than stored as
// null, so a blank cell means the key is absent.
type Object struct {
	Keys []ObjectField
}

// ObjectField is one key. It has one origin, the same set a field has,
// and a nested Object is that origin rather than a seventh transform.
// Arrays and a JSON Schema on the column are out of scope until a file
// needs them.
type ObjectField struct {
	Key        string
	Source     string
	Sources    []string
	Join       *string
	Coalesce   []string
	Constant   *string
	Transforms []Transform
	Format     string
	NullIf     []string
	Default    *string
	Object     *Object
}

// asField views the key as a field so origin checks and Apply do not grow
// a second copy of those rules. Target is empty; the parent field owns it.
func (k ObjectField) asField() Field {
	return Field{
		Source:     k.Source,
		Sources:    k.Sources,
		Join:       k.Join,
		Coalesce:   k.Coalesce,
		Constant:   k.Constant,
		Object:     k.Object,
		Transforms: k.Transforms,
		Format:     k.Format,
		NullIf:     k.NullIf,
		Default:    k.Default,
	}
}

func (k ObjectField) body() objectBody {
	return objectBody{
		Source:     k.Source,
		Sources:    k.Sources,
		Join:       k.Join,
		Coalesce:   k.Coalesce,
		Constant:   k.Constant,
		Transforms: k.Transforms,
		Format:     k.Format,
		NullIf:     k.NullIf,
		Default:    k.Default,
		Object:     k.Object,
	}
}

// objectBody is the on-disk shape of one key. It exists so the codecs can
// use struct tags; ObjectField itself stays free of them because Key is
// the map key, not a field.
type objectBody struct {
	Source     string      `yaml:"source,omitempty" json:"source,omitempty"`
	Sources    []string    `yaml:"sources,omitempty" json:"sources,omitempty"`
	Join       *string     `yaml:"join,omitempty" json:"join,omitempty"`
	Coalesce   []string    `yaml:"coalesce,omitempty" json:"coalesce,omitempty"`
	Constant   *string     `yaml:"constant,omitempty" json:"constant,omitempty"`
	Transforms []Transform `yaml:"transforms,omitempty" json:"transforms,omitempty"`
	Format     string      `yaml:"format,omitempty" json:"format,omitempty"`
	NullIf     []string    `yaml:"null_if,omitempty" json:"null_if,omitempty"`
	Default    *string     `yaml:"default,omitempty" json:"default,omitempty"`
	Object     *Object     `yaml:"object,omitempty" json:"object,omitempty"`
}

func (b objectBody) field() ObjectField {
	return ObjectField{
		Source:     b.Source,
		Sources:    b.Sources,
		Join:       b.Join,
		Coalesce:   b.Coalesce,
		Constant:   b.Constant,
		Transforms: b.Transforms,
		Format:     b.Format,
		NullIf:     b.NullIf,
		Default:    b.Default,
		Object:     b.Object,
	}
}

func leafKey(k string) bool {
	switch k {
	case "source", "sources", "join", "coalesce", "constant", "transforms",
		"format", "null_if", "default", "object":
		return true
	default:
		return false
	}
}

// UnmarshalYAML reads an object in file order. A map[string] decode would
// lose the order the document is emitted in.
func (o *Object) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || len(n.Content)%2 != 0 {
		return fmt.Errorf("importer: object must be a map")
	}
	keys := make([]ObjectField, 0, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		name := n.Content[i].Value
		leaf, err := decodeObjectField(n.Content[i+1])
		if err != nil {
			return fmt.Errorf("importer: object key %s: %w", name, err)
		}
		leaf.Key = name
		keys = append(keys, leaf)
	}
	o.Keys = keys
	return nil
}

func decodeObjectField(n *yaml.Node) (ObjectField, error) {
	if n.Kind != yaml.MappingNode {
		return ObjectField{}, fmt.Errorf("importer: object leaf must be a map")
	}
	for i := 0; i < len(n.Content); i += 2 {
		k := n.Content[i].Value
		if !leafKey(k) {
			return ObjectField{}, fmt.Errorf("importer: unknown field %q", k)
		}
	}
	var body objectBody
	if err := n.Decode(&body); err != nil {
		return ObjectField{}, err
	}
	return body.field(), nil
}

// MarshalYAML writes keys in file order. yaml.Marshal of a Go map would
// sort them and a saved document would not match the one that was loaded.
func (o Object) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range o.Keys {
		raw, err := yaml.Marshal(k.body())
		if err != nil {
			return nil, err
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
			return nil, fmt.Errorf("importer: object key %s did not encode", k.Key)
		}
		n.Content = append(n.Content, strNode(k.Key), doc.Content[0])
	}
	return n, nil
}

// UnmarshalJSON keeps key order by walking tokens. encoding/json's map
// decode does not.
func (o *Object) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("importer: object must be a map")
	}
	var keys []ObjectField
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("importer: object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		leaf, err := decodeObjectFieldJSON(raw)
		if err != nil {
			return fmt.Errorf("importer: object key %s: %w", name, err)
		}
		leaf.Key = name
		keys = append(keys, leaf)
	}
	tok, err = dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '}' {
		return fmt.Errorf("importer: object must be a map")
	}
	o.Keys = keys
	return nil
}

func decodeObjectFieldJSON(raw []byte) (ObjectField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body objectBody
	if err := dec.Decode(&body); err != nil {
		return ObjectField{}, err
	}
	return body.field(), nil
}

// MarshalJSON writes keys in file order.
func (o Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k.Key)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(k.body())
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// SourceNames lists the source columns f reads, in the order Apply expects
// their cells. Object keys are included depth-first so a preview can pass
// one flat list.
func SourceNames(f Field) []string {
	return fieldSources(f)
}

func isJSONDoc(sqlType string) bool {
	switch sqlBase(sqlType) {
	case "json", "jsonb":
		return true
	default:
		return false
	}
}

func jsonValid(s *string) bool {
	if s == nil {
		return false
	}
	return json.Valid([]byte(*s))
}

type cursor struct {
	in []string
	i  int
}

func (c *cursor) take() (string, error) {
	if c.i >= len(c.in) {
		return "", fmt.Errorf("importer: object leaf is missing a source value")
	}
	s := c.in[c.i]
	c.i++
	return s, nil
}

// assembleObject builds one document and parses it. The parse is what makes
// a bad constant or a bad row fail as JSON rather than as a string the
// database will reject later, with a worse message.
func assembleObject(obj *Object, inputs []string) (string, error) {
	if obj == nil {
		return "", ErrNoOrigin
	}
	c := cursor{in: inputs}
	buf, err := writeObject(&c, nil, obj)
	if err != nil {
		return "", err
	}
	if !json.Valid(buf) {
		return "", fmt.Errorf("importer: assembled value is not valid JSON")
	}
	return string(buf), nil
}

func writeObject(c *cursor, dst []byte, obj *Object) ([]byte, error) {
	dst = append(dst, '{')
	first := true
	for i := range obj.Keys {
		raw, omit, err := evalLeaf(c, &obj.Keys[i])
		if err != nil {
			return dst, err
		}
		if omit {
			continue
		}
		if !first {
			dst = append(dst, ',')
		}
		first = false
		var encErr error
		dst, encErr = appendJSONString(dst, []byte(obj.Keys[i].Key))
		if encErr != nil {
			return dst, encErr
		}
		dst = append(dst, ':')
		dst = append(dst, raw...)
	}
	return append(dst, '}'), nil
}

// evalLeaf runs one key's chain. The column is text because a leaf is a
// JSON string, not the parent column: coercing a leaf as jsonb would wrap
// the document in a string or reject a plain word.
func evalLeaf(c *cursor, k *ObjectField) ([]byte, bool, error) {
	if k.Object != nil {
		if k.Source != "" || len(k.Sources) > 0 || len(k.Coalesce) > 0 || k.Constant != nil {
			return nil, false, ErrTwoOrigins
		}
		raw, err := writeObject(c, nil, k.Object)
		if err != nil {
			return nil, false, err
		}
		if len(k.Transforms) == 0 && len(k.NullIf) == 0 && k.Default == nil {
			if bytes.Equal(raw, []byte("{}")) {
				return nil, true, nil
			}
			return raw, false, nil
		}
		// The chain runs on the nested document as text. An unchanged
		// result stays a JSON object; a chain that rewrites it is stored
		// as a string, which is what the chain produced.
		got, isNull, err := Apply(schemaText, Field{
			Source:     "nested",
			Transforms: k.Transforms,
			NullIf:     k.NullIf,
			Default:    k.Default,
		}, []string{string(raw)})
		if err != nil {
			return nil, false, err
		}
		if isNull || got == "" {
			return nil, true, nil
		}
		if got == string(raw) {
			return raw, false, nil
		}
		buf, err := appendJSONString(nil, []byte(got))
		if err != nil {
			return nil, false, err
		}
		return buf, false, nil
	}
	f := k.asField()
	var inputs []string
	switch originOf(f) {
	case originConstant:
	case originSource:
		s, err := c.take()
		if err != nil {
			return nil, false, err
		}
		inputs = []string{s}
	case originJoin:
		inputs = make([]string, len(k.Sources))
		for i := range inputs {
			s, err := c.take()
			if err != nil {
				return nil, false, err
			}
			inputs[i] = s
		}
	case originCoalesce:
		inputs = make([]string, len(k.Coalesce))
		for i := range inputs {
			s, err := c.take()
			if err != nil {
				return nil, false, err
			}
			inputs[i] = s
		}
	default:
		return nil, false, ErrNoOrigin
	}
	got, isNull, err := Apply(schemaText, f, inputs)
	if err != nil {
		return nil, false, err
	}
	if isNull || got == "" {
		return nil, true, nil
	}
	buf, err := appendJSONString(nil, []byte(got))
	if err != nil {
		return nil, false, err
	}
	return buf, false, nil
}

// appendJSONString appends s as a JSON string. Invalid UTF-8 is an error
// rather than U+FFFD: a replacement would store a different character than
// the file held, and the row can be rejected instead.
func appendJSONString(dst, s []byte) ([]byte, error) {
	dst = append(dst, '"')
	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
			i++
		case '\b':
			dst = append(dst, '\\', 'b')
			i++
		case '\f':
			dst = append(dst, '\\', 'f')
			i++
		case '\n':
			dst = append(dst, '\\', 'n')
			i++
		case '\r':
			dst = append(dst, '\\', 'r')
			i++
		case '\t':
			dst = append(dst, '\\', 't')
			i++
		default:
			if c < 0x20 {
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0x0f])
				i++
				continue
			}
			r, size := utf8.DecodeRune(s[i:])
			if r == utf8.RuneError && size == 1 {
				return dst, fmt.Errorf("importer: value is not valid UTF-8")
			}
			dst = append(dst, s[i:i+size]...)
			i += size
		}
	}
	return append(dst, '"'), nil
}

const hexDigits = "0123456789abcdef"
