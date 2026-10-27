package importer

import (
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
)

// parquetSource reads one flat parquet file. Physical boolean, int32,
// int64, float, and double values are stored with SetBool/SetInt/SetFloat
// and bypass coercion; a timestamp logical type is an instant and is
// stored with SetTime (UTC); UTF-8/string and decimal logical types stay
// KindText because a float would drop scale; a date logical type is civil
// text `2006-01-02`, not an instant. Unknown physical types are a row
// error, not a silent string.
type parquetSource struct {
	f      *os.File
	r      *parquet.Reader
	cols   []pqCol
	fields []string
	line   int64
	// rows is reused across Next. ReadRows appends into the existing
	// backing array, and each value is copied into dst before the next
	// read overwrites the page buffer it aliases.
	rows []parquet.Row
}

// pqKind is how a leaf is stored on the record. It is not the parquet
// physical kind: a date and an int32 share a physical type, and only
// this split keeps the date as civil text instead of a day count.
type pqKind uint8

const (
	pqBool pqKind = iota + 1
	pqInt32
	pqInt64
	pqFloat32
	pqFloat64
	pqTime
	pqDate
	pqText
	pqDecimal
	pqUnknown
)

const (
	pqMilli = iota + 1
	pqMicro
	pqNano
)

// pqCol is one leaf. scale and unit stay zero unless the logical type
// needs them. phys is kept so a decimal reads its unscaled integer
// from the physical slot, which is not always an int64.
type pqCol struct {
	name  string
	kind  pqKind
	scale int32
	unit  byte
	phys  parquet.Kind
}

func openParquet(f *os.File) (*parquetSource, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// OpenFile returns an error for a bad footer. NewReader on an
	// arbitrary ReaderAt panics when it cannot size the input, which
	// would take down an import over one corrupt extract. NewReader on
	// the *File is safe once OpenFile has succeeded.
	pf, err := parquet.OpenFile(f, info.Size())
	if err != nil {
		return nil, err
	}
	cols, fields, err := parquetColumns(pf.Schema())
	if err != nil {
		return nil, err
	}
	return &parquetSource{
		f:      f,
		r:      parquet.NewReader(pf),
		cols:   cols,
		fields: fields,
		rows:   make([]parquet.Row, 1),
	}, nil
}

func parquetColumns(schema *parquet.Schema) ([]pqCol, []string, error) {
	top := schema.Fields()
	cols := make([]pqCol, 0, len(top))
	fields := make([]string, 0, len(top))
	for _, field := range top {
		if !field.Leaf() {
			return nil, nil, fmt.Errorf("importer: parquet group column %q", field.Name())
		}
		col, err := classifyParquet(field.Name(), field)
		if err != nil {
			return nil, nil, err
		}
		cols = append(cols, col)
		fields = append(fields, field.Name())
	}
	return cols, fields, nil
}

// classifyParquet checks the logical type before the physical kind.
// A date and a decimal are both stored as integers; classifying the
// physical kind first would turn the date into a day count and the
// decimal into a float, which drops the scale.
func classifyParquet(name string, node parquet.Node) (pqCol, error) {
	col := pqCol{name: name, phys: node.Type().Kind()}
	lt := node.Type().LogicalType()
	if lt != nil {
		switch {
		case lt.Decimal != nil:
			col.kind = pqDecimal
			col.scale = lt.Decimal.Scale
			return col, nil
		case lt.Date != nil:
			col.kind = pqDate
			return col, nil
		case lt.Timestamp != nil:
			col.kind = pqTime
			switch {
			case lt.Timestamp.Unit.Millis != nil:
				col.unit = pqMilli
			case lt.Timestamp.Unit.Micros != nil:
				col.unit = pqMicro
			case lt.Timestamp.Unit.Nanos != nil:
				col.unit = pqNano
			default:
				return col, fmt.Errorf("importer: parquet column %q has an unknown timestamp unit", name)
			}
			return col, nil
		case lt.UTF8 != nil || lt.Json != nil || lt.Bson != nil || lt.Enum != nil || lt.UUID != nil:
			col.kind = pqText
			return col, nil
		}
	}
	switch col.phys {
	case parquet.Boolean:
		col.kind = pqBool
	case parquet.Int32:
		col.kind = pqInt32
	case parquet.Int64:
		col.kind = pqInt64
	case parquet.Float:
		col.kind = pqFloat32
	case parquet.Double:
		col.kind = pqFloat64
	case parquet.ByteArray, parquet.FixedLenByteArray:
		// An unannotated byte array is still the characters. The
		// generator's string columns are UTF-8; treating the bytes as
		// a float or a silent empty string would drop them.
		col.kind = pqText
	default:
		col.kind = pqUnknown
	}
	return col, nil
}

func (s *parquetSource) Fields() []string { return s.fields }

func (s *parquetSource) Width() (int, error) { return len(s.fields), nil }

func (s *parquetSource) Close() error {
	var err error
	if s.r != nil {
		err = s.r.Close()
	}
	if s.f != nil {
		if e := s.f.Close(); err == nil {
			err = e
		}
	}
	return err
}

func (s *parquetSource) Next(dst *Record) (bool, error) {
	s.rows = s.rows[:1]
	n, err := s.r.ReadRows(s.rows)
	if n == 0 {
		if err == nil || err == io.EOF {
			return false, nil
		}
		return false, err
	}
	if err != nil && err != io.EOF {
		return false, err
	}
	s.line++
	row := s.rows[0]
	if len(row) != len(s.cols) {
		return false, fmt.Errorf("importer: line %d has %d parquet values, want %d", s.line, len(row), len(s.cols))
	}
	dst.Reset(len(s.cols))
	dst.Line = s.line
	for i, v := range row {
		if err := s.setValue(dst, i, v); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *parquetSource) setValue(dst *Record, i int, v parquet.Value) error {
	if v.IsNull() {
		return nil
	}
	col := s.cols[i]
	switch col.kind {
	case pqBool:
		dst.SetBool(i, v.Boolean())
	case pqInt32:
		dst.SetInt(i, int64(v.Int32()))
	case pqInt64:
		dst.SetInt(i, v.Int64())
	case pqFloat32:
		dst.SetFloat(i, float64(v.Float()))
	case pqFloat64:
		dst.SetFloat(i, v.Double())
	case pqTime:
		tm, err := parquetTime(v.Int64(), col.unit)
		if err != nil {
			return fmt.Errorf("importer: line %d column %q: %w", s.line, col.name, err)
		}
		dst.SetTime(i, tm)
	case pqDate:
		var tmp [16]byte
		days := int64(v.Int32())
		b := time.Unix(days*86400, 0).UTC().AppendFormat(tmp[:0], "2006-01-02")
		dst.SetText(i, b)
	case pqText:
		dst.SetText(i, v.ByteArray())
	case pqDecimal:
		text, err := decimalText(v, col.phys, col.scale)
		if err != nil {
			return fmt.Errorf("importer: line %d column %q: %w", s.line, col.name, err)
		}
		dst.SetText(i, []byte(text))
	default:
		return fmt.Errorf("importer: line %d column %q has unknown physical type %s", s.line, col.name, v.Kind())
	}
	return nil
}

func parquetTime(n int64, unit byte) (time.Time, error) {
	switch unit {
	case pqMilli:
		return time.UnixMilli(n).UTC(), nil
	case pqMicro:
		return time.UnixMicro(n).UTC(), nil
	case pqNano:
		return time.Unix(0, n).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unknown timestamp unit")
	}
}

func decimalText(v parquet.Value, phys parquet.Kind, scale int32) (string, error) {
	var n big.Int
	switch phys {
	case parquet.Int32:
		n.SetInt64(int64(v.Int32()))
	case parquet.Int64:
		n.SetInt64(v.Int64())
	case parquet.ByteArray, parquet.FixedLenByteArray:
		b := v.ByteArray()
		if len(b) == 0 {
			n.SetInt64(0)
			break
		}
		if b[0]&0x80 != 0 {
			tmp := make([]byte, len(b))
			for i := range b {
				tmp[i] = ^b[i]
			}
			n.SetBytes(tmp)
			n.Add(&n, big.NewInt(1))
			n.Neg(&n)
		} else {
			n.SetBytes(b)
		}
	default:
		return "", fmt.Errorf("decimal physical type %s", phys)
	}
	return formatUnscaled(&n, int(scale)), nil
}

// formatUnscaled keeps every scale digit, including trailing zeros.
// 100 at scale 2 is 1.00. Dropping the zeros would still parse as
// the same number and would no longer be the text the column stored.
func formatUnscaled(n *big.Int, scale int) string {
	neg := n.Sign() < 0
	abs := n
	if neg {
		abs = new(big.Int).Neg(n)
	}
	if scale < 0 {
		s := abs.String() + strings.Repeat("0", -scale)
		if neg {
			return "-" + s
		}
		return s
	}
	if scale == 0 {
		if neg {
			return "-" + abs.String()
		}
		return abs.String()
	}
	digits := abs.String()
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	i := len(digits) - scale
	s := digits[:i] + "." + digits[i:]
	if neg {
		return "-" + s
	}
	return s
}
