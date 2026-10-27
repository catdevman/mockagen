package importer

import (
	"bufio"
	"fmt"
	"io"
	"slices"
)

// fixedSource slices each record itself. go-fixedwidth reads positions
// from struct tags and ignores a tag it cannot parse, and the map's
// positions are data, not tags on a generated struct.
type fixedSource struct {
	br     *bufio.Reader
	closer io.Closer
	cols   []FixedCol
	fields []string
	width  int
	line   int64
	buf    []byte
}

func openFixed(f io.ReadCloser, spec SourceSpec) (*fixedSource, error) {
	cols, width, err := validateFixedLayout(spec)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return &fixedSource{
		br:     bufio.NewReader(f),
		closer: f,
		cols:   cols,
		fields: names,
		width:  width,
	}, nil
}

// validateFixedLayout rejects an empty layout, a bad range, a repeated
// name, or an overlap. A gap is left alone: the writer can leave a span
// unused, and that span is not a column. Fields, when the map pinned
// them, have to be these names in this order or a later row would be
// read into the wrong column.
func validateFixedLayout(spec SourceSpec) ([]FixedCol, int, error) {
	if len(spec.Fixed) == 0 {
		return nil, 0, fmt.Errorf("importer: source.fixed is required for a fixed-width file")
	}
	names := make([]string, len(spec.Fixed))
	seen := make(map[string]struct{}, len(spec.Fixed))
	width := 0
	for i, c := range spec.Fixed {
		if c.Name == "" {
			return nil, 0, fmt.Errorf("importer: fixed column name is empty")
		}
		if _, ok := seen[c.Name]; ok {
			return nil, 0, fmt.Errorf("importer: fixed column %q is duplicated", c.Name)
		}
		seen[c.Name] = struct{}{}
		if c.Start < 1 || c.End < c.Start {
			return nil, 0, fmt.Errorf("importer: fixed column %q has start %d end %d", c.Name, c.Start, c.End)
		}
		names[i] = c.Name
		if c.End > width {
			width = c.End
		}
	}
	for i := range spec.Fixed {
		for j := i + 1; j < len(spec.Fixed); j++ {
			a, b := spec.Fixed[i], spec.Fixed[j]
			if a.Start <= b.End && b.Start <= a.End {
				return nil, 0, fmt.Errorf("importer: fixed columns %q and %q overlap", a.Name, b.Name)
			}
		}
	}
	if len(spec.Fields) > 0 && !slices.Equal(spec.Fields, names) {
		return nil, 0, fmt.Errorf("importer: source.fields %s does not match fixed columns %s",
			formatList(spec.Fields), formatList(names))
	}
	return append([]FixedCol(nil), spec.Fixed...), width, nil
}

func (s *fixedSource) Fields() []string { return s.fields }

func (s *fixedSource) Width() (int, error) { return len(s.fields), nil }

func (s *fixedSource) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func (s *fixedSource) Next(dst *Record) (bool, error) {
	var err error
	s.buf, err = readLine(s.br, s.buf)
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.line++
	// The pad is part of the field. Stripping it would hide a value that
	// only differed by trailing spaces, and the layout width is the check.
	if len(s.buf) != s.width {
		return false, fmt.Errorf("importer: line %d has length %d, want width %d", s.line, len(s.buf), s.width)
	}
	dst.Reset(len(s.cols))
	dst.Line = s.line
	for i, c := range s.cols {
		dst.SetText(i, s.buf[c.Start-1:c.End])
	}
	return true, nil
}
