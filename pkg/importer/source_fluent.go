package importer

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"encoding/json/jsontext"
)

// fluentSource reads the lines fluentWriter writes: timestamp, tag, and
// one JSON object, split on the first two tabs. The timestamp and the tag
// are routing, not columns. The object uses the same cell rules as the
// json source, so a number stays the literal the file spelled.
type fluentSource struct {
	br     *bufio.Reader
	closer io.Closer
	fields []string
	line   int64
	buf    []byte

	peeked  bool
	peekOK  bool
	peekErr error
	held    Record
}

func openFluent(f io.ReadCloser) (*fluentSource, error) {
	s := &fluentSource{br: bufio.NewReader(f), closer: f}
	if err := s.peek(); err != nil && err != io.EOF {
		return nil, err
	}
	return s, nil
}

func (s *fluentSource) Fields() []string { return s.fields }

func (s *fluentSource) Width() (int, error) { return len(s.fields), nil }

func (s *fluentSource) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func (s *fluentSource) Next(dst *Record) (bool, error) {
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

func (s *fluentSource) peek() error {
	if s.peeked {
		return s.peekErr
	}
	s.peeked = true
	ok, err := s.read(&s.held)
	s.peekOK = ok
	s.peekErr = err
	return err
}

func (s *fluentSource) read(dst *Record) (bool, error) {
	var err error
	s.buf, err = readLine(s.br, s.buf)
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.line++
	parts := bytes.SplitN(s.buf, []byte("\t"), 3)
	if len(parts) != 3 {
		return false, fmt.Errorf("importer: line %d: fluent record is not tab-separated timestamp, tag, and json", s.line)
	}
	return fillJSONObject(dst, parts[2], &s.fields, s.line)
}

func fillJSONObject(dst *Record, raw []byte, fields *[]string, line int64) (bool, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	if dec.PeekKind() != '{' {
		return false, fmt.Errorf("importer: line %d: fluent record is not a JSON object", line)
	}
	if _, err := dec.ReadToken(); err != nil {
		return false, err
	}
	var keys []string
	var vals [][]byte
	var nulls []bool
	for dec.PeekKind() != '}' {
		if dec.PeekKind() == 0 {
			return false, fmt.Errorf("importer: line %d: fluent record is not a JSON object", line)
		}
		tok, err := dec.ReadToken()
		if err != nil {
			return false, err
		}
		key := strings.Clone(tok.String())
		val, err := dec.ReadValue()
		if err != nil {
			return false, err
		}
		text, isNull, err := jsonCell(val)
		if err != nil {
			return false, err
		}
		keys = append(keys, key)
		vals = append(vals, text)
		nulls = append(nulls, isNull)
	}
	if _, err := dec.ReadToken(); err != nil {
		return false, err
	}
	if _, err := dec.ReadToken(); err != io.EOF {
		if err == nil {
			return false, fmt.Errorf("importer: line %d: fluent record is not a JSON object", line)
		}
		return false, err
	}
	if *fields == nil {
		// make, not append onto nil: an empty object would stay nil
		// and the next line would redefine the columns.
		cloned := make([]string, len(keys))
		copy(cloned, keys)
		*fields = cloned
	}
	dst.Reset(len(*fields))
	dst.Line = line
	pos := map[string]int{}
	for i, k := range keys {
		pos[k] = i
	}
	for i, name := range *fields {
		j, ok := pos[name]
		if !ok || nulls[j] {
			continue
		}
		dst.SetText(i, vals[j])
	}
	return true, nil
}
