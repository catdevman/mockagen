package importer

import (
	"math"
	"time"
	"unsafe"
)

// Kind is the SQL-shaped type of a Value. KindNull is the zero value so
// that a freshly reset Record is all-NULL: a short row and a missing JSON
// key then need no special case. Omitted targets are left off the record.
//
// KindBinary is not a stored record kind. bytea has no literal the file
// sinks can emit without inventing an encoding, so classify still reports
// it and Apply rejects the cell.
type Kind uint8

const (
	KindNull Kind = iota
	KindText
	KindInt
	KindFloat
	KindBool
	KindTime
	// KindRaw is text a sink must emit verbatim rather than quote. A
	// quoted now() would insert the characters "now()".
	KindRaw
	KindBinary
)

// kindName is the word an error uses for k. Kind's numeric value would
// say "2 does not match 4", which does not tell a bool from an int, and
// stringifying the cell would hide the mismatch the caller is reporting.
func kindName(k Kind) string {
	switch k {
	case KindNull:
		return "null"
	case KindText:
		return "text"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	case KindTime:
		return "time"
	case KindRaw:
		return "raw"
	case KindBinary:
		return "binary"
	default:
		return "unknown"
	}
}

// Value is one field of one row. Text is not stored here: it lives in the
// owning Record's arena, located by off and ln, because a slice into that
// arena would be invalidated by the append that grows it.
type Value struct {
	Kind    Kind
	num     int64
	flt     float64
	off, ln int32
}

// Record is one row: a fixed-width slice of Values plus the byte arena
// their text points into. Both are reused across every row of a run, so
// reading a row costs no allocations once the slices have grown.
type Record struct {
	Values []Value
	// Line is the 1-based position of this row in its source, carried so
	// an error or a sample can name where it came from. csv.Reader does
	// not report physical lines, so this is the record ordinal with the
	// header counted as line 1.
	Line int64

	arena []byte
}

// Reset prepares r to hold n all-NULL fields, reusing both backing arrays.
// A short row leaves the tail NULL because the tail stays zero.
func (r *Record) Reset(n int) {
	if cap(r.Values) < n {
		r.Values = make([]Value, n)
	} else {
		r.Values = r.Values[:n]
		clear(r.Values)
	}
	r.arena = r.arena[:0]
	r.Line = 0
}

// SetNull marks field i NULL. Reset already does this; a later step that
// decides the cell is null, after a text write, has to clear the kind or
// the sink would emit the text.
func (r *Record) SetNull(i int) {
	r.Values[i] = Value{}
}

// SetText copies b into the arena and points field i at it. The copy is
// the point: b is normally a window into the Source's read buffer, which
// the next read overwrites. An empty b is an empty string, not NULL.
func (r *Record) SetText(i int, b []byte) {
	r.setBytes(i, KindText, b)
}

// SetRaw copies b and marks it verbatim. Same copy as SetText; the kind
// is what stops a sink quoting it.
func (r *Record) SetRaw(i int, b []byte) {
	r.setBytes(i, KindRaw, b)
}

func (r *Record) setBytes(i int, kind Kind, b []byte) {
	if len(r.arena) > math.MaxInt32-len(b) {
		// off and ln are int32 because that is the Value layout. A row
		// that does not fit is a bug in the caller, not a data error the
		// rejects file can usefully name.
		panic("importer: record arena exceeds int32")
	}
	off := int32(len(r.arena))
	r.arena = append(r.arena, b...)
	r.Values[i] = Value{Kind: kind, off: off, ln: int32(len(b))}
}

// SetInt stores n in field i. Numbers stay out of the arena so a sink can
// emit digits without quoting and without a per-row string.
func (r *Record) SetInt(i int, n int64) {
	r.Values[i] = Value{Kind: KindInt, num: n}
}

// SetFloat stores f in field i, for the same reason as SetInt.
func (r *Record) SetFloat(i int, f float64) {
	r.Values[i] = Value{Kind: KindFloat, flt: f}
}

// SetBool stores v in field i. The numeric slot is 0 or 1 so a sink does
// not need a second field on Value.
func (r *Record) SetBool(i int, v bool) {
	n := int64(0)
	if v {
		n = 1
	}
	r.Values[i] = Value{Kind: KindBool, num: n}
}

// SetTime stores t as Unix nanoseconds. The design keeps the instant, not
// the offset: a timestamp literal that must keep its zone is stored with
// SetText as RFC3339 instead, because formatting here would shift it to UTC.
func (r *Record) SetTime(i int, t time.Time) {
	r.Values[i] = Value{Kind: KindTime, num: t.UnixNano()}
}

// Bytes returns field i's text. The result aliases the arena and is valid
// only until the next Next or Reset. The same contract sql.RawBytes and
// bufio.Scanner.Bytes carry, for the same reason. A NULL field returns nil;
// an empty string returns a zero-length slice.
func (r *Record) Bytes(i int) []byte {
	v := r.Values[i]
	if v.Kind != KindText && v.Kind != KindRaw {
		return nil
	}
	return r.arena[v.off : v.off+v.ln]
}

// Int returns the integer stored in field i.
func (r *Record) Int(i int) int64 { return r.Values[i].num }

// Float returns the float stored in field i.
func (r *Record) Float(i int) float64 { return r.Values[i].flt }

// Bool reports the boolean stored in field i.
func (r *Record) Bool(i int) bool { return r.Values[i].num != 0 }

// Arg converts field i to something database/sql accepts. The one place a
// text field allocates, because database/sql copies its arguments anyway.
// A file sink never calls it.
func (r *Record) Arg(i int) any {
	v := r.Values[i]
	switch v.Kind {
	case KindNull:
		return nil
	case KindInt:
		return v.num
	case KindBool:
		return v.num != 0
	case KindFloat:
		return v.flt
	case KindTime:
		return time.Unix(0, v.num).UTC()
	case KindText, KindRaw:
		// Copy. The arena is reused on the next row, and database/sql
		// may hold the argument until Exec returns.
		return string(r.Bytes(i))
	default:
		return nil
	}
}

// clone copies the row, including the arena, so a sample outlives the next
// Reset. The steady-state path does not call it.
func (r Record) clone() Record {
	out := Record{
		Line:   r.Line,
		Values: append([]Value(nil), r.Values...),
		arena:  append([]byte(nil), r.arena...),
	}
	return out
}

// unsafeString views b as a string without copying. Callers must not keep
// the string after b is overwritten. The parsers do not retain it, and a
// copy would be an allocation on every numeric cell.
func unsafeString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}
