package importer

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/catdevman/mockagen/pkg/schema"
)

// fieldError is a row failure that names its target. Run turns it into a
// RowError; the column is what the rejects file has to show.
type fieldError struct {
	target string
	err    error
}

func (e *fieldError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	if e.target == "" {
		return e.err.Error()
	}
	return e.target + ": " + e.err.Error()
}

func (e *fieldError) Unwrap() error { return e.err }

// prepare resolves source names to positions and folds constants. Doing
// that per row would allocate a lookup, and a constant is the same bytes
// every time.
func (p *Plan) prepare(sch schema.Schema, m FieldMap) {
	p.names = map[string]int{}
	if len(m.Source.Fields) > 0 {
		for i, name := range m.Source.Fields {
			if _, ok := p.names[name]; !ok {
				p.names[name] = i
			}
		}
	} else {
		for _, idx := range m.Source.Indexes {
			name := strconv.Itoa(idx)
			if _, ok := p.names[name]; !ok {
				p.names[name] = idx
			}
		}
	}
	p.Target.Name = sch.Name
	p.Target.Dialect = sch.Source.Dialect
	p.Target.Table = sch.Source.Table
	if p.Target.Table == "" {
		p.Target.Table = sch.Name
	}
	p.binds = make([]binding, len(p.Write))
	p.Target.Columns = make([]Column, len(p.Write))
	for i, w := range p.Write {
		b := binding{
			field: w.Field,
			col:   w.Column,
			obj:   w.Field.Object != nil,
			idx:   p.positions(SourceNames(w.Field)),
			kind:  classify(w.Column.SQLType, w.Column.NumericScale),
		}
		p.Target.Columns[i] = sinkColumn(w.Column)
		if pureConstant(w.Field) {
			if lit, ok := precompute(w.Column, w.Field); ok {
				b.lit = lit
			}
		}
		p.binds[i] = b
	}
}

func (p *Plan) positions(names []string) []int {
	out := make([]int, len(names))
	for i, name := range names {
		idx, ok := p.names[name]
		if !ok {
			idx = -1
		}
		out[i] = idx
	}
	return out
}

func pureConstant(f Field) bool {
	if f.Object != nil {
		return objectPure(f.Object)
	}
	return f.Constant != nil && f.Source == "" && len(f.Sources) == 0 && len(f.Coalesce) == 0
}

func objectPure(obj *Object) bool {
	for i := range obj.Keys {
		k := &obj.Keys[i]
		if k.Source != "" || len(k.Sources) > 0 || len(k.Coalesce) > 0 {
			return false
		}
		if k.Object != nil && !objectPure(k.Object) {
			return false
		}
	}
	return true
}

func precompute(col schema.Column, f Field) (literal, bool) {
	canon, isNull, err := Apply(col, f, nil)
	if err != nil {
		return literal{}, false
	}
	if isNull {
		return literal{ok: true, kind: KindNull}, true
	}
	return litFromCanon(col, canon), true
}

func litFromCanon(col schema.Column, canon string) literal {
	switch classify(col.SQLType, col.NumericScale) {
	case KindInt:
		n, err := strconv.ParseInt(canon, 10, 64)
		if err != nil {
			return literal{ok: true, kind: KindText, text: []byte(canon)}
		}
		return literal{ok: true, kind: KindInt, num: n}
	case KindFloat:
		f, err := strconv.ParseFloat(canon, 64)
		if err != nil {
			return literal{ok: true, kind: KindText, text: []byte(canon)}
		}
		return literal{ok: true, kind: KindFloat, flt: f}
	case KindBool:
		n := int64(0)
		if canon == "true" {
			n = 1
		}
		return literal{ok: true, kind: KindBool, num: n}
	case KindTime:
		// RFC3339 with its offset. KindTime is a UTC instant and would
		// turn 15:04-07:00 into 22:04Z.
		return literal{ok: true, kind: KindText, text: []byte(canon)}
	default:
		return literal{ok: true, kind: KindText, text: []byte(canon)}
	}
}

func sinkColumn(c schema.Column) Column {
	out := Column{
		Name:       c.Name,
		Kind:       classify(c.SQLType, c.NumericScale),
		SQLType:    c.SQLType,
		Nullable:   c.Nullable,
		Identity:   c.Identity,
		HasDefault: c.Default != "",
		Scale:      -1,
		EnumValues: c.EnumValues,
	}
	if c.MaxLength != nil {
		out.MaxLength = *c.MaxLength
	}
	if c.NumericPrecision != nil {
		out.Precision = *c.NumericPrecision
	}
	if c.NumericScale != nil {
		out.Scale = *c.NumericScale
	}
	return out
}

// Apply maps src, in source order, into dst, in write-list order. dst is
// reset, so a cell that produces null stays NULL. Omitted columns are not
// in dst; the sink's column list skips them.
func (p *Plan) Apply(src, dst *Record) error {
	dst.Reset(len(p.binds))
	dst.Line = src.Line
	for i := range p.binds {
		if err := p.applyOne(i, src, dst); err != nil {
			return &fieldError{target: p.binds[i].col.Name, err: err}
		}
	}
	return nil
}

func (p *Plan) applyOne(i int, src, dst *Record) error {
	b := &p.binds[i]
	if b.lit.ok {
		writeLit(dst, i, b.lit)
		return nil
	}
	if b.obj {
		return p.applyObject(b, src, dst, i)
	}
	return p.evalBytes(b, src, dst, i)
}

func writeLit(dst *Record, i int, lit literal) {
	switch lit.kind {
	case KindNull:
		dst.SetNull(i)
	case KindInt:
		dst.SetInt(i, lit.num)
	case KindFloat:
		dst.SetFloat(i, lit.flt)
	case KindBool:
		dst.SetBool(i, lit.num != 0)
	case KindRaw:
		dst.SetRaw(i, lit.text)
	default:
		dst.SetText(i, lit.text)
	}
}

func (p *Plan) applyObject(b *binding, src, dst *Record, i int) error {
	cells := make([]string, len(b.idx))
	for n, idx := range b.idx {
		if idx < 0 {
			return fmt.Errorf("%w: object", ErrUnknownSource)
		}
		if idx >= len(src.Values) || src.Values[idx].Kind == KindNull {
			cells[n] = ""
			continue
		}
		cells[n] = string(src.Bytes(idx))
	}
	canon, isNull, err := Apply(b.col, b.field, cells)
	if err != nil {
		return err
	}
	if isNull {
		dst.SetNull(i)
		return nil
	}
	return p.writeCanon(dst, i, b.col, canon)
}

func (p *Plan) writeCanon(dst *Record, i int, col schema.Column, canon string) error {
	writeLit(dst, i, litFromCanon(col, canon))
	return nil
}

func (p *Plan) evalBytes(b *binding, src, dst *Record, i int) error {
	kind := b.kind
	if originOf(b.field) == originCoalesce {
		return p.evalCoalesce(b, src, dst, i, kind)
	}
	raw, isNull, err := p.reduceBytes(b, src)
	if err != nil {
		return err
	}
	if isNull {
		return p.finishNull(b, dst, i, kind)
	}
	cur, err := p.chainBytes(b.field.Transforms, raw)
	if err != nil {
		return err
	}
	if listedBytes(cur, b.field.NullIf) {
		return p.finishNull(b, dst, i, kind)
	}
	return p.coerceBytes(b.col, kind, b.field.Format, cur, dst, i)
}

func (p *Plan) evalCoalesce(b *binding, src, dst *Record, i int, kind Kind) error {
	if len(b.idx) != len(b.field.Coalesce) {
		return fmt.Errorf("importer: coalesce %q: got %d values, want %d",
			b.field.Target, len(b.idx), len(b.field.Coalesce))
	}
	for _, idx := range b.idx {
		raw, isNull, err := p.cell(src, idx)
		if err != nil {
			return err
		}
		if isNull {
			continue
		}
		cur, err := p.chainBytes(b.field.Transforms, raw)
		if err != nil {
			return err
		}
		if listedBytes(cur, b.field.NullIf) {
			continue
		}
		return p.coerceBytes(b.col, kind, b.field.Format, cur, dst, i)
	}
	return p.finishNull(b, dst, i, kind)
}

func (p *Plan) reduceBytes(b *binding, src *Record) ([]byte, bool, error) {
	f := b.field
	switch originOf(f) {
	case originConstant:
		return unsafeBytes(*f.Constant), false, nil
	case originSource:
		if len(b.idx) != 1 {
			return nil, false, fmt.Errorf("importer: source %q: got %d values, want 1",
				f.Source, len(b.idx))
		}
		return p.cell(src, b.idx[0])
	case originJoin:
		if len(b.idx) != len(f.Sources) {
			return nil, false, fmt.Errorf("importer: join %q: got %d values, want %d",
				f.Target, len(b.idx), len(f.Sources))
		}
		p.scratch = p.scratch[:0]
		sep := ""
		if f.Join != nil {
			sep = *f.Join
		}
		first := true
		for _, idx := range b.idx {
			part, isNull, err := p.cell(src, idx)
			if err != nil {
				return nil, false, err
			}
			if isNull || len(part) == 0 {
				continue
			}
			if !first {
				p.scratch = append(p.scratch, sep...)
			}
			first = false
			p.scratch = append(p.scratch, part...)
		}
		return p.scratch, false, nil
	default:
		return nil, false, ErrNoOrigin
	}
}

// chainBytes copies between hold and scratch. A step's output often
// aliases one of those two, and append onto the same array is not a
// defined copy when the ranges overlap.
func (p *Plan) chainBytes(steps []Transform, in []byte) ([]byte, error) {
	if len(steps) == 0 {
		return in, nil
	}
	p.hold = append(p.hold[:0], in...)
	cur := p.hold
	for i := range steps {
		out, err := p.stepBytes(&steps[i], cur)
		if err != nil {
			return nil, err
		}
		if sliceAlias(out, p.hold) {
			p.scratch = append(p.scratch[:0], out...)
			out = p.scratch
		}
		if i == len(steps)-1 {
			return out, nil
		}
		p.hold = append(p.hold[:0], out...)
		cur = p.hold
	}
	return cur, nil
}

func sliceAlias(a, b []byte) bool {
	if len(a) == 0 || cap(b) == 0 {
		return false
	}
	as := uintptr(unsafe.Pointer(unsafe.SliceData(a)))
	bs := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	be := bs + uintptr(cap(b))
	return as >= bs && as < be
}

func (p *Plan) stepBytes(t *Transform, in []byte) ([]byte, error) {
	switch t.Name {
	case "trim":
		if t.HasCutset {
			return bytes.Trim(in, t.Cutset), nil
		}
		return bytes.TrimSpace(in), nil
	case "case":
		return p.caseBytes(t.Case, in)
	case "replace":
		return p.replaceBytes(t, in)
	case "regex_replace":
		return p.regexBytes(t, in)
	case "map":
		return p.mapBytes(t, in)
	case "pad_left", "pad_right":
		return p.padBytes(t, in)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownTransform, t.Name)
	}
}

func (p *Plan) caseBytes(mode string, in []byte) ([]byte, error) {
	p.scratch = p.scratch[:0]
	switch mode {
	case "upper", "lower", "title":
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownCase, mode)
	}
	start := true
	for i := 0; i < len(in); {
		r, size := utf8.DecodeRune(in[i:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("importer: value is not valid UTF-8")
		}
		i += size
		switch mode {
		case "upper":
			r = unicode.ToUpper(r)
		case "lower":
			r = unicode.ToLower(r)
		case "title":
			if unicode.IsSpace(r) {
				start = true
			} else if start {
				r = unicode.ToUpper(r)
				start = false
			} else {
				r = unicode.ToLower(r)
			}
		}
		p.scratch = utf8.AppendRune(p.scratch, r)
	}
	return p.scratch, nil
}

func (p *Plan) replaceBytes(t *Transform, in []byte) ([]byte, error) {
	if t.From == "" {
		return nil, ErrEmptyReplace
	}
	from := unsafeBytes(t.From)
	p.scratch = p.scratch[:0]
	rest := in
	for {
		i := bytes.Index(rest, from)
		if i < 0 {
			p.scratch = append(p.scratch, rest...)
			break
		}
		p.scratch = append(p.scratch, rest[:i]...)
		p.scratch = append(p.scratch, t.To...)
		rest = rest[i+len(from):]
	}
	return p.scratch, nil
}

func (p *Plan) regexBytes(t *Transform, in []byte) ([]byte, error) {
	re := t.re
	if re == nil {
		compiled, err := regexp.Compile(t.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrBadRegex, err)
		}
		re = compiled
	}
	// ReplaceAll allocates. Regex is off the steady-state benchmark; the
	// result is copied into scratch so the next step does not keep it.
	out := re.ReplaceAll(in, unsafeBytes(t.To))
	p.scratch = append(p.scratch[:0], out...)
	return p.scratch, nil
}

func (p *Plan) mapBytes(t *Transform, in []byte) ([]byte, error) {
	if len(t.Accept) > 0 && len(t.Pairs) == 0 {
		return nil, ErrUnfinishedLookup
	}
	for _, pair := range t.Pairs {
		if len(in) == len(pair.From) && unsafeString(in) == pair.From {
			return unsafeBytes(pair.To), nil
		}
	}
	if t.LookupDefault != nil {
		return unsafeBytes(*t.LookupDefault), nil
	}
	return nil, fmt.Errorf("%w: %q", ErrLookupMiss, unsafeString(in))
}

func (p *Plan) padBytes(t *Transform, in []byte) ([]byte, error) {
	if t.Width < 1 || t.With == "" {
		return nil, ErrBadPad
	}
	have := utf8.RuneCount(in)
	if have >= t.Width {
		return in, nil
	}
	need := t.Width - have
	with := []rune(t.With)
	p.scratch = p.scratch[:0]
	writeFill := func() {
		for n := 0; n < need; n++ {
			p.scratch = utf8.AppendRune(p.scratch, with[n%len(with)])
		}
	}
	if t.Name == "pad_left" || t.Left {
		writeFill()
		p.scratch = append(p.scratch, in...)
	} else {
		p.scratch = append(p.scratch, in...)
		writeFill()
	}
	return p.scratch, nil
}

func (p *Plan) finishNull(b *binding, dst *Record, i int, kind Kind) error {
	if b.field.Default == nil {
		dst.SetNull(i)
		return nil
	}
	// format is the source layout. A default is already the column value.
	return p.coerceBytes(b.col, kind, "", unsafeBytes(*b.field.Default), dst, i)
}

func (p *Plan) cell(src *Record, idx int) ([]byte, bool, error) {
	if idx < 0 {
		return nil, false, fmt.Errorf("%w", ErrUnknownSource)
	}
	if idx >= len(src.Values) || src.Values[idx].Kind == KindNull {
		return nil, true, nil
	}
	if src.Values[idx].Kind != KindText && src.Values[idx].Kind != KindRaw {
		return nil, true, nil
	}
	b := src.Bytes(idx)
	if b == nil {
		return []byte{}, false, nil
	}
	return b, false, nil
}

func listedBytes(b []byte, list []string) bool {
	for _, item := range list {
		if len(b) == len(item) && unsafeString(b) == item {
			return true
		}
	}
	return false
}

func (p *Plan) coerceBytes(col schema.Column, kind Kind, format string, s []byte, dst *Record, i int) error {
	switch kind {
	case KindInt:
		n, err := parseIntBytes(s)
		if err != nil {
			return err
		}
		p.scratch = strconv.AppendInt(p.scratch[:0], n, 10)
		if err := checkNumeric(unsafeString(p.scratch), col); err != nil {
			return err
		}
		if err := checkEnum(col, p.scratch); err != nil {
			return err
		}
		dst.SetInt(i, n)
		return nil
	case KindFloat:
		f, err := strconv.ParseFloat(unsafeString(bytes.TrimSpace(s)), 64)
		if err != nil {
			return err
		}
		p.scratch = strconv.AppendFloat(p.scratch[:0], f, 'f', -1, 64)
		if err := checkNumeric(unsafeString(p.scratch), col); err != nil {
			return err
		}
		if err := checkEnum(col, p.scratch); err != nil {
			return err
		}
		dst.SetFloat(i, f)
		return nil
	case KindBool:
		switch unsafeString(bytes.TrimSpace(s)) {
		case "true", "t", "1", "yes":
			dst.SetBool(i, true)
		case "false", "f", "0", "no":
			dst.SetBool(i, false)
		default:
			return fmt.Errorf("invalid boolean %q", unsafeString(s))
		}
		return nil
	case KindTime:
		canon, err := coerceTime(format, unsafeString(bytes.TrimSpace(s)))
		if err != nil {
			return err
		}
		if err := checkEnum(col, unsafeBytes(canon)); err != nil {
			return err
		}
		dst.SetText(i, unsafeBytes(canon))
		return nil
	case KindBinary:
		return fmt.Errorf("binary value %q", unsafeString(s))
	default:
		if col.MaxLength != nil {
			if n := utf8.RuneCount(s); int64(n) > *col.MaxLength {
				return fmt.Errorf("length %d exceeds %d", n, *col.MaxLength)
			}
		}
		if err := checkEnum(col, s); err != nil {
			return err
		}
		dst.SetText(i, s)
		return nil
	}
}

func checkEnum(col schema.Column, canon []byte) error {
	if len(col.EnumValues) == 0 {
		return nil
	}
	if listedBytes(canon, col.EnumValues) {
		return nil
	}
	return fmt.Errorf("%q is not an allowed value", unsafeString(canon))
}

func parseIntBytes(b []byte) (int64, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return 0, fmt.Errorf("invalid integer %q", "")
	}
	return strconv.ParseInt(unsafeString(b), 10, 64)
}

func unsafeBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
