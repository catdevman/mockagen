package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen/sqlgen"
)

// Column is one target the sink writes. Kind comes from sql_type, never
// from the generator type. Omitted columns are absent from the list, so a
// database default fires and a nullable omission is not an explicit NULL.
type Column struct {
	Name       string
	Kind       Kind
	SQLType    string
	Nullable   bool
	Identity   bool
	HasDefault bool
	MaxLength  int64
	Precision  int64
	Scale      int64
	EnumValues []string
}

// Target is the table a sink opens against.
type Target struct {
	Name    string
	Table   string
	Dialect string
	Columns []Column
}

// Sink consumes rows in Target.Columns order. The Record is reused by the
// caller, so a sink that keeps a row has to copy it.
type Sink interface {
	Open(ctx context.Context, t Target) error
	Write(ctx context.Context, r *Record) error
	Close(ctx context.Context) error
}

// RecordSink keeps every row. Tests use it to see NULL and an empty string
// without a file format in between. Write clones because the caller reuses
// the Record.
type RecordSink struct {
	Rows []Record
}

func (s *RecordSink) Open(context.Context, Target) error { return nil }

func (s *RecordSink) Write(_ context.Context, r *Record) error {
	s.Rows = append(s.Rows, r.clone())
	return nil
}

func (s *RecordSink) Close(context.Context) error { return nil }

// DiscardSink accepts rows and stores nothing. A dry run and the map
// screen's validate pane are this sink, so neither grows a second mapper.
type DiscardSink struct{}

func (DiscardSink) Open(context.Context, Target) error { return nil }

func (DiscardSink) Write(context.Context, *Record) error { return nil }

func (DiscardSink) Close(context.Context) error { return nil }

type batchSetter interface {
	SetBatch(n int)
}

// SQLSink writes multi-row INSERT statements. Values go through
// sqlgen.AppendValue, so NULL is NULL and a number is unquoted. The
// generator's writer quotes every column because it has no nulls.
type SQLSink struct {
	w      io.Writer
	d      sqlgen.Dialect
	prefix []byte
	buf    []byte
	cols   int
	rows   int
	batch  int
	want   int
	opened bool
}

// NewSQLSink writes INSERT statements to w. w is not closed.
func NewSQLSink(w io.Writer) *SQLSink { return &SQLSink{w: w} }

// SetBatch caps rows per statement. Zero leaves the dialect's limit,
// which depends on the column count.
func (s *SQLSink) SetBatch(n int) { s.want = n }

func (s *SQLSink) Open(_ context.Context, t Target) error {
	d, err := sqlgen.Lookup(t.Dialect)
	if err != nil {
		return err
	}
	s.d = d
	s.cols = len(t.Columns)
	s.batch = batchLimit(d, s.cols, s.want)
	s.rows = 0
	table := t.Table
	if table == "" {
		table = t.Name
	}
	s.prefix = s.prefix[:0]
	s.prefix = append(s.prefix, "INSERT INTO "...)
	s.prefix = d.AppendQualifiedIdent(s.prefix, table)
	s.prefix = append(s.prefix, " ("...)
	for i, col := range t.Columns {
		if i > 0 {
			s.prefix = append(s.prefix, ", "...)
		}
		s.prefix = d.AppendIdent(s.prefix, col.Name)
	}
	s.prefix = append(s.prefix, ") VALUES"...)
	s.opened = true
	return nil
}

func batchLimit(d sqlgen.Dialect, cols, want int) int {
	n := sqlgen.MaxRowsPerStatement
	if cols > 0 && d.MaxBoundParams > 0 {
		byParam := d.MaxBoundParams / cols
		if byParam < n {
			n = byParam
		}
	}
	if n < 1 {
		n = 1
	}
	if want > 0 && want < n {
		n = want
	}
	return n
}

func (s *SQLSink) Write(_ context.Context, r *Record) error {
	if !s.opened {
		return fmt.Errorf("importer: sql sink is not open")
	}
	s.buf = s.buf[:0]
	if s.rows == 0 {
		s.buf = append(s.buf, s.prefix...)
	} else {
		s.buf = append(s.buf, ',')
	}
	s.buf = append(s.buf, '\n', '\t', '(')
	for i := 0; i < s.cols; i++ {
		if i > 0 {
			s.buf = append(s.buf, ", "...)
		}
		var err error
		s.buf, err = s.appendVal(s.buf, r, i)
		if err != nil {
			return err
		}
	}
	s.buf = append(s.buf, ')')
	if _, err := s.w.Write(s.buf); err != nil {
		return err
	}
	s.rows++
	if s.rows >= s.batch {
		if _, err := s.w.Write(sqlStmtEnd); err != nil {
			return err
		}
		s.rows = 0
	}
	return nil
}

var sqlStmtEnd = []byte(";\n")

// appendVal writes one literal. It does not call Dialect.AppendValue:
// that takes any, and boxing a []byte or a float allocates on every
// cell. The branches below are the same ones AppendValue would take.
func (s *SQLSink) appendVal(dst []byte, r *Record, i int) ([]byte, error) {
	if i >= len(r.Values) {
		return append(dst, "NULL"...), nil
	}
	v := r.Values[i]
	switch v.Kind {
	case KindNull:
		return append(dst, "NULL"...), nil
	case KindInt:
		return strconv.AppendInt(dst, v.num, 10), nil
	case KindFloat:
		if math.IsNaN(v.flt) || math.IsInf(v.flt, 0) {
			return dst, fmt.Errorf("sqlgen: unsupported float value: %v", v.flt)
		}
		return strconv.AppendFloat(dst, v.flt, 'g', -1, 64), nil
	case KindBool:
		if v.num != 0 {
			return append(dst, s.d.TrueLiteral...), nil
		}
		return append(dst, s.d.FalseLiteral...), nil
	case KindTime:
		return appendSQLInstant(dst, s.d, v.num), nil
	case KindRaw:
		return append(dst, r.Bytes(i)...), nil
	default:
		b := r.Bytes(i)
		if bytes.IndexByte(b, 0) >= 0 {
			return dst, sqlgen.ErrEmbeddedNUL
		}
		return sqlgen.AppendString(dst, s.d, b), nil
	}
}

// appendSQLInstant quotes a KindTime value. Bytes is nil for that kind,
// so the default arm would emit an empty string. The buffer stays in
// this function: appendVal is on the steady-state path, and an escaped
// array there would allocate on every text cell. CSV text times stay
// KindText; this is only the parquet instant.
func appendSQLInstant(dst []byte, d sqlgen.Dialect, nanos int64) []byte {
	var tmp [40]byte
	b := time.Unix(0, nanos).UTC().AppendFormat(tmp[:0], time.RFC3339Nano)
	return sqlgen.AppendString(dst, d, b)
}

func (s *SQLSink) Close(context.Context) error {
	if !s.opened || s.rows == 0 {
		return nil
	}
	s.rows = 0
	_, err := s.w.Write(sqlStmtEnd)
	return err
}

// CSVSink writes RFC 4180 with every field quoted. An unquoted empty field
// is NULL and a quoted empty field is an empty string: postgres COPY and
// mssql BULK INSERT disagree with mysql on which of those an unquoted empty
// means, and quoting only the empty string is what keeps them apart.
type CSVSink struct {
	w      io.Writer
	header bool
	cols   []string
	buf    []byte
	opened bool
}

// NewCSVSink writes to w. header emits a column-name row first.
func NewCSVSink(w io.Writer, header bool) *CSVSink {
	return &CSVSink{w: w, header: header}
}

func (s *CSVSink) Open(_ context.Context, t Target) error {
	s.cols = s.cols[:0]
	for _, c := range t.Columns {
		s.cols = append(s.cols, c.Name)
	}
	s.opened = true
	if !s.header {
		return nil
	}
	s.buf = s.buf[:0]
	for i, name := range s.cols {
		if i > 0 {
			s.buf = append(s.buf, ',')
		}
		s.buf = appendQuoted(s.buf, unsafeBytes(name))
	}
	s.buf = append(s.buf, '\n')
	_, err := s.w.Write(s.buf)
	return err
}

func (s *CSVSink) Write(_ context.Context, r *Record) error {
	if !s.opened {
		return fmt.Errorf("importer: csv sink is not open")
	}
	s.buf = s.buf[:0]
	for i := range s.cols {
		if i > 0 {
			s.buf = append(s.buf, ',')
		}
		s.buf = appendCSVValue(s.buf, r, i)
	}
	s.buf = append(s.buf, '\n')
	_, err := s.w.Write(s.buf)
	return err
}

func (s *CSVSink) Close(context.Context) error { return nil }

func appendCSVValue(dst []byte, r *Record, i int) []byte {
	if i >= len(r.Values) || r.Values[i].Kind == KindNull {
		return dst
	}
	v := r.Values[i]
	switch v.Kind {
	case KindInt:
		var tmp [32]byte
		b := strconv.AppendInt(tmp[:0], v.num, 10)
		return appendQuoted(dst, b)
	case KindFloat:
		var tmp [64]byte
		b := strconv.AppendFloat(tmp[:0], v.flt, 'f', -1, 64)
		return appendQuoted(dst, b)
	case KindBool:
		if v.num != 0 {
			return appendQuoted(dst, []byte("true"))
		}
		return appendQuoted(dst, []byte("false"))
	case KindTime:
		return appendCSVInstant(dst, v.num)
	default:
		return appendQuoted(dst, r.Bytes(i))
	}
}

// appendCSVInstant quotes a KindTime instant the way text is quoted.
// The format buffer is local for the same reason as appendSQLInstant:
// this switch's other arms are the steady-state write.
func appendCSVInstant(dst []byte, nanos int64) []byte {
	var tmp [40]byte
	b := time.Unix(0, nanos).UTC().AppendFormat(tmp[:0], time.RFC3339Nano)
	return appendQuoted(dst, b)
}

func appendQuoted(dst, field []byte) []byte {
	dst = append(dst, '"')
	for _, c := range field {
		if c == '"' {
			dst = append(dst, '"', '"')
		} else {
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// JSONSink writes one JSON object per line. Null and an empty string stay
// distinct, which a CSV row cannot show without the quoting rule above.
type JSONSink struct {
	w      io.Writer
	cols   []string
	buf    []byte
	opened bool
}

// NewJSONSink writes JSONL to w.
func NewJSONSink(w io.Writer) *JSONSink { return &JSONSink{w: w} }

func (s *JSONSink) Open(_ context.Context, t Target) error {
	s.cols = s.cols[:0]
	for _, c := range t.Columns {
		s.cols = append(s.cols, c.Name)
	}
	s.opened = true
	return nil
}

func (s *JSONSink) Write(_ context.Context, r *Record) error {
	if !s.opened {
		return fmt.Errorf("importer: json sink is not open")
	}
	s.buf = append(s.buf[:0], '{')
	for i, name := range s.cols {
		if i > 0 {
			s.buf = append(s.buf, ',')
		}
		var err error
		s.buf, err = appendJSONString(s.buf, unsafeBytes(name))
		if err != nil {
			return err
		}
		s.buf = append(s.buf, ':')
		s.buf, err = appendJSONField(s.buf, r, i)
		if err != nil {
			return err
		}
	}
	s.buf = append(s.buf, '}', '\n')
	_, err := s.w.Write(s.buf)
	return err
}

func (s *JSONSink) Close(context.Context) error { return nil }

func appendJSONField(dst []byte, r *Record, i int) ([]byte, error) {
	if i >= len(r.Values) || r.Values[i].Kind == KindNull {
		return append(dst, "null"...), nil
	}
	v := r.Values[i]
	switch v.Kind {
	case KindInt:
		return strconv.AppendInt(dst, v.num, 10), nil
	case KindFloat:
		return strconv.AppendFloat(dst, v.flt, 'f', -1, 64), nil
	case KindBool:
		if v.num != 0 {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case KindTime:
		return appendJSONInstant(dst, v.num)
	default:
		return appendJSONString(dst, r.Bytes(i))
	}
}

// appendJSONInstant quotes a KindTime instant as a JSON string. Boxing
// the time as any would allocate, and Bytes would encode nothing.
func appendJSONInstant(dst []byte, nanos int64) ([]byte, error) {
	var tmp [40]byte
	b := time.Unix(0, nanos).UTC().AppendFormat(tmp[:0], time.RFC3339Nano)
	return appendJSONString(dst, b)
}

// BulkSink writes a quoted CSV and the dialect's load script. The pair is
// what mode bulk means: the CSV is the data, and the script is the command
// that loads it, with the caveats the generator already spells out.
type BulkSink struct {
	csv    *CSVSink
	script io.Writer
	name   string
	header bool
}

// NewBulkSink writes data to csv and the load script to script. name is the
// bare CSV file name the script mentions, resolved next to the script when
// it is run. header must match the script's HEADER / IGNORE / FIRSTROW.
func NewBulkSink(csv, script io.Writer, name string, header bool) *BulkSink {
	return &BulkSink{csv: NewCSVSink(csv, header), script: script, name: name, header: header}
}

func (s *BulkSink) Open(ctx context.Context, t Target) error {
	d, err := sqlgen.Lookup(t.Dialect)
	if err != nil {
		return err
	}
	table := t.Table
	if table == "" {
		table = t.Name
	}
	// sqlite's .import treats a dotted name as a literal table name and
	// loads every row into a table that did not exist. Insert mode can
	// still address an ATTACHed database.
	if d.Name == "sqlite" && strings.Contains(table, ".") {
		return fmt.Errorf("sql dialect sqlite with mode bulk cannot load into qualified table %q: "+
			"sqlite's .import would create a table of that literal name - use the bare table name", table)
	}
	if err := s.csv.Open(ctx, t); err != nil {
		return err
	}
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = c.Name
	}
	_, err = s.script.Write(sqlgen.BulkLoadScript(d, table, cols, s.name, s.header))
	return err
}

func (s *BulkSink) Write(ctx context.Context, r *Record) error {
	return s.csv.Write(ctx, r)
}

func (s *BulkSink) Close(ctx context.Context) error { return s.csv.Close(ctx) }
