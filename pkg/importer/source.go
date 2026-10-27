package importer

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"encoding/json/jsontext"
)

// Source yields the rows of one input. Next fills dst rather than returning
// a Record, so a run holds one row however many rows the file has.
type Source interface {
	// Fields names the columns in positional order. Nil for a headerless
	// source; that file is addressed by the indexes stored on the map.
	Fields() []string
	// Next fills dst with the next row. False means end of input. dst is
	// reset first, so a short row leaves its tail NULL.
	Next(dst *Record) (bool, error)
	Close() error
}

// SliceSource is a Source backed by memory. A nil entry is NULL and a
// pointer to an empty string is empty, so a test can tell them apart
// without a file.
type SliceSource struct {
	fields []string
	rows   [][]*string
	i      int
}

// NewSliceSource returns a Source over rows. fields may be nil for a
// headerless slice; each row is then addressed by index.
func NewSliceSource(fields []string, rows [][]*string) *SliceSource {
	return &SliceSource{fields: append([]string(nil), fields...), rows: rows}
}

func (s *SliceSource) Fields() []string { return s.fields }

func (s *SliceSource) Next(dst *Record) (bool, error) {
	if s.i >= len(s.rows) {
		return false, nil
	}
	row := s.rows[s.i]
	s.i++
	n := len(s.fields)
	if n == 0 {
		n = len(row)
	}
	dst.Reset(n)
	dst.Line = int64(s.i)
	for i := 0; i < n && i < len(row); i++ {
		if row[i] == nil {
			continue
		}
		dst.SetText(i, []byte(*row[i]))
	}
	return true, nil
}

func (s *SliceSource) Close() error { return nil }

func (s *SliceSource) Width() (int, error) {
	if len(s.fields) > 0 {
		return len(s.fields), nil
	}
	if len(s.rows) == 0 {
		return 0, nil
	}
	return len(s.rows[0]), nil
}

// OpenSource opens path as the format spec names. csv and tsv are delimited
// text. json is one array; jsonl is one object per line. Other formats are
// a later task, and an encoding other than utf-8 is refused rather than
// misread.
func OpenSource(path string, spec SourceSpec) (Source, error) {
	if spec.Encoding != "" && !strings.EqualFold(spec.Encoding, "utf-8") && !strings.EqualFold(spec.Encoding, "utf8") {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedEncoding, spec.Encoding)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	src, err := newSource(f, spec)
	if err != nil {
		f.Close()
		return nil, err
	}
	return src, nil
}

func newSource(f *os.File, spec SourceSpec) (Source, error) {
	format := strings.ToLower(spec.Format)
	if format == "" {
		switch strings.ToLower(extOf(f.Name())) {
		case ".json":
			format = "json"
		case ".jsonl":
			format = "jsonl"
		case ".tsv":
			format = "tsv"
		default:
			format = "csv"
		}
	}
	switch format {
	case "csv", "tsv":
		return openCSV(f, spec, format)
	case "json", "jsonl":
		return openJSON(f, spec, format == "json")
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownFormat, spec.Format)
	}
}

func extOf(path string) string {
	i := strings.LastIndex(path, ".")
	if i < 0 {
		return ""
	}
	return path[i:]
}

// MatchHeader compares src to the header or indexes the map pinned.
// A mismatch names both sides. The output file must not be created until
// this returns nil: a swapped pair would otherwise load into the wrong
// columns and look successful.
func MatchHeader(spec SourceSpec, src Source) error {
	if spec.HasHeader || len(spec.Fields) > 0 {
		got := src.Fields()
		if !slices.Equal(got, spec.Fields) {
			return fmt.Errorf("%w: file has %s, map has %s",
				ErrHeaderMismatch, formatList(got), formatList(spec.Fields))
		}
		return nil
	}
	w, err := widthOf(src)
	if err != nil {
		return err
	}
	for _, idx := range spec.Indexes {
		if idx < 0 || idx >= w {
			return fmt.Errorf("%w: index %d, width %d", ErrShortRow, idx, w)
		}
	}
	return nil
}

func formatList(names []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Quote(n))
	}
	b.WriteByte(']')
	return b.String()
}

type widther interface {
	Width() (int, error)
}

func widthOf(src Source) (int, error) {
	if w, ok := src.(widther); ok {
		return w.Width()
	}
	return len(src.Fields()), nil
}

type csvSource struct {
	r      *csv.Reader
	closer io.Closer
	fields []string
	width  int
	line   int64

	peeked  bool
	peekOK  bool
	peekErr error
	held    Record
}

func openCSV(f *os.File, spec SourceSpec, format string) (*csvSource, error) {
	r := csv.NewReader(f)
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	comma, err := delimiter(spec, format)
	if err != nil {
		return nil, err
	}
	r.Comma = comma
	s := &csvSource{r: r, closer: f}
	if spec.HasHeader {
		hdr, err := r.Read()
		if err != nil {
			return nil, err
		}
		s.fields = append([]string(nil), hdr...)
		s.width = len(hdr)
		s.line = 1
	}
	return s, nil
}

func delimiter(spec SourceSpec, format string) (rune, error) {
	if format == "tsv" && spec.Delimiter == "" {
		return '\t', nil
	}
	if spec.Delimiter == "" || spec.Delimiter == "," {
		return ',', nil
	}
	if spec.Delimiter == `\t` || spec.Delimiter == "\t" {
		return '\t', nil
	}
	r, size := utf8.DecodeRuneInString(spec.Delimiter)
	if r == utf8.RuneError || size != len(spec.Delimiter) {
		return 0, fmt.Errorf("importer: delimiter %q is not one character", spec.Delimiter)
	}
	return r, nil
}

func (s *csvSource) Fields() []string { return s.fields }

func (s *csvSource) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func (s *csvSource) Width() (int, error) {
	if s.width > 0 {
		return s.width, nil
	}
	if err := s.peek(); err != nil {
		return 0, err
	}
	if !s.peekOK {
		return 0, nil
	}
	return len(s.held.Values), nil
}

func (s *csvSource) Next(dst *Record) (bool, error) {
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

func (s *csvSource) peek() error {
	if s.peeked {
		return s.peekErr
	}
	s.peeked = true
	ok, err := s.read(&s.held)
	s.peekOK = ok
	s.peekErr = err
	return err
}

func (s *csvSource) read(dst *Record) (bool, error) {
	rec, err := s.r.Read()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.line++
	width := s.width
	if width == 0 {
		width = len(rec)
		s.width = width
	}
	if len(rec) > width {
		return false, fmt.Errorf("importer: line %d has %d fields, want %d", s.line, len(rec), width)
	}
	dst.Reset(width)
	dst.Line = s.line
	for i, cell := range rec {
		// Copy. ReuseRecord hands back the reader's buffer on the next
		// Read, and SetText is what keeps this row.
		dst.SetText(i, []byte(cell))
	}
	return true, nil
}

func copyRecord(dst, src *Record) {
	dst.Reset(len(src.Values))
	dst.Line = src.Line
	for i, v := range src.Values {
		switch v.Kind {
		case KindNull:
		case KindInt:
			dst.SetInt(i, v.num)
		case KindFloat:
			dst.SetFloat(i, v.flt)
		case KindBool:
			dst.SetBool(i, v.num != 0)
		case KindTime:
			dst.Values[i] = v
		case KindRaw:
			dst.SetRaw(i, src.Bytes(i))
		default:
			dst.SetText(i, src.Bytes(i))
		}
	}
}

type jsonSource struct {
	dec    *jsontext.Decoder
	closer io.Closer
	fields []string
	array  bool
	line   int64

	peeked  bool
	peekOK  bool
	peekErr error
	held    Record
	closed  bool
}

func openJSON(f *os.File, spec SourceSpec, array bool) (*jsonSource, error) {
	s := &jsonSource{dec: jsontext.NewDecoder(f), closer: f, array: array}
	if array {
		tok, err := s.dec.ReadToken()
		if err == io.EOF {
			return s, nil
		}
		if err != nil {
			return nil, err
		}
		if tok.Kind() != '[' {
			return nil, fmt.Errorf("importer: json input must be an array")
		}
	}
	if err := s.peek(); err != nil && err != io.EOF {
		return nil, err
	}
	return s, nil
}

func (s *jsonSource) Fields() []string { return s.fields }

func (s *jsonSource) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func (s *jsonSource) Width() (int, error) { return len(s.fields), nil }

func (s *jsonSource) Next(dst *Record) (bool, error) {
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

func (s *jsonSource) peek() error {
	if s.peeked {
		return s.peekErr
	}
	s.peeked = true
	ok, err := s.read(&s.held)
	s.peekOK = ok
	s.peekErr = err
	return err
}

func (s *jsonSource) read(dst *Record) (bool, error) {
	if s.closed {
		return false, nil
	}
	kind := s.dec.PeekKind()
	if kind == jsontext.KindInvalid || (s.array && kind == ']') {
		if s.array && kind == ']' {
			if _, err := s.dec.ReadToken(); err != nil {
				return false, err
			}
		}
		s.closed = true
		return false, nil
	}
	if kind != '{' {
		return false, fmt.Errorf("importer: line %d is not a JSON object", s.line+1)
	}
	if _, err := s.dec.ReadToken(); err != nil {
		return false, err
	}
	var keys []string
	var vals [][]byte
	var nulls []bool
	for s.dec.PeekKind() != '}' {
		tok, err := s.dec.ReadToken()
		if err != nil {
			return false, err
		}
		// Clone. The token aliases the decoder buffer, which the next
		// ReadValue overwrites.
		key := strings.Clone(tok.String())
		raw, err := s.dec.ReadValue()
		if err != nil {
			return false, err
		}
		text, isNull, err := jsonCell(raw)
		if err != nil {
			return false, err
		}
		keys = append(keys, key)
		vals = append(vals, text)
		nulls = append(nulls, isNull)
	}
	if _, err := s.dec.ReadToken(); err != nil {
		return false, err
	}
	s.line++
	if s.fields == nil {
		s.fields = append([]string(nil), keys...)
	}
	dst.Reset(len(s.fields))
	dst.Line = s.line
	pos := map[string]int{}
	for i, k := range keys {
		pos[k] = i
	}
	for i, name := range s.fields {
		j, ok := pos[name]
		if !ok || nulls[j] {
			continue
		}
		dst.SetText(i, vals[j])
	}
	return true, nil
}

func jsonCell(raw jsontext.Value) ([]byte, bool, error) {
	v := bytes.TrimSpace(raw)
	if bytes.Equal(v, []byte("null")) {
		return nil, true, nil
	}
	if len(v) > 0 && v[0] == '"' {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, false, err
		}
		return []byte(s), false, nil
	}
	return append([]byte(nil), v...), false, nil
}
