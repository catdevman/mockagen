package importer

import (
	"fmt"
	"io"

	yaml "gopkg.in/yaml.v3"
)

// yamlSource reads either one sequence of mappings, which is what
// yamlSeqWriter writes, or a stream of mapping documents. Scalar spelling
// comes from yaml.Node.Value, so a leading zero is not rewritten as an
// integer before coercion. A decoded any would have already done that.
type yamlSource struct {
	dec    *yaml.Decoder
	closer io.Closer
	fields []string
	line   int64

	// seq is the current sequence document. yaml.v3 yields one document
	// at a time, and the writer's file is one sequence, so the items have
	// to stay reachable until Next has walked them.
	seq  *yaml.Node
	seqi int

	peeked  bool
	peekOK  bool
	peekErr error
	held    Record
}

func openYAML(f io.ReadCloser) (*yamlSource, error) {
	s := &yamlSource{dec: yaml.NewDecoder(f), closer: f}
	if err := s.peek(); err != nil && err != io.EOF {
		return nil, err
	}
	return s, nil
}

func (s *yamlSource) Fields() []string { return s.fields }

func (s *yamlSource) Width() (int, error) { return len(s.fields), nil }

func (s *yamlSource) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func (s *yamlSource) Next(dst *Record) (bool, error) {
	if s.peeked {
		s.peeked = false
		if s.peekErr != nil {
			return false, s.peekErr
		}
		if !s.peekOK {
			return false, nil
		}
		copyRecord(dst, &s.held)
		return true, nil
	}
	return s.read(dst)
}

func (s *yamlSource) peek() error {
	if s.peeked {
		return s.peekErr
	}
	s.peeked = true
	ok, err := s.read(&s.held)
	s.peekOK = ok
	s.peekErr = err
	return err
}

func (s *yamlSource) read(dst *Record) (bool, error) {
	n, err := s.nextNode()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.fill(dst, n)
}

func (s *yamlSource) nextNode() (*yaml.Node, error) {
	if s.seq != nil {
		if s.seqi < len(s.seq.Content) {
			n := unwrapYAML(s.seq.Content[s.seqi])
			s.seqi++
			if n == nil || n.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("importer: line %d is not a mapping", yamlLine(n))
			}
			return n, nil
		}
		s.seq = nil
	}
	var doc yaml.Node
	if err := s.dec.Decode(&doc); err != nil {
		return nil, err
	}
	n := unwrapYAML(&doc)
	if n != nil && n.Kind == yaml.SequenceNode {
		s.seq = n
		s.seqi = 0
		return s.nextNode()
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("importer: line %d is not a mapping", yamlLine(n))
	}
	return n, nil
}

func (s *yamlSource) fill(dst *Record, n *yaml.Node) (bool, error) {
	if len(n.Content)%2 != 0 {
		return false, fmt.Errorf("importer: line %d mapping is malformed", n.Line)
	}
	keys := make([]string, 0, len(n.Content)/2)
	vals := make([]*yaml.Node, 0, len(n.Content)/2)
	seen := make(map[string]int, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		k := unwrapYAML(n.Content[i])
		v := unwrapYAML(n.Content[i+1])
		if k == nil || k.Kind != yaml.ScalarNode {
			return false, fmt.Errorf("importer: line %d key is not a scalar", yamlLine(k))
		}
		if _, ok := seen[k.Value]; ok {
			return false, fmt.Errorf("importer: line %d duplicate key %q", n.Line, k.Value)
		}
		seen[k.Value] = len(keys)
		keys = append(keys, k.Value)
		vals = append(vals, v)
	}
	if s.fields == nil {
		// make, not append onto nil: an empty mapping would stay nil
		// and the next row would be treated as the first.
		s.fields = make([]string, len(keys))
		copy(s.fields, keys)
	}
	known := make(map[string]struct{}, len(s.fields))
	for _, name := range s.fields {
		known[name] = struct{}{}
	}
	for _, k := range keys {
		if _, ok := known[k]; !ok {
			return false, fmt.Errorf("importer: line %d unexpected key %q", n.Line, k)
		}
	}
	line := int64(n.Line)
	if line == 0 {
		s.line++
		line = s.line
	}
	dst.Reset(len(s.fields))
	dst.Line = line
	for i, name := range s.fields {
		j, ok := seen[name]
		if !ok || yamlNull(vals[j]) {
			continue
		}
		v := vals[j]
		if v.Kind != yaml.ScalarNode {
			return false, fmt.Errorf("importer: line %d key %q is not a scalar", v.Line, name)
		}
		dst.SetText(i, []byte(v.Value))
	}
	return true, nil
}

func unwrapYAML(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// yamlNull is a !!null node, not an empty string. An empty scalar is
// a present value; treating it as null would collapse the two, which
// is the distinction the YAML writer keeps by quoting "".
func yamlNull(n *yaml.Node) bool {
	if n == nil || n.Kind == 0 || n.IsZero() {
		return true
	}
	return n.ShortTag() == "!!null"
}

func yamlLine(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	return n.Line
}
