package importer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/catdevman/mockagen/pkg/schema"
)

// Plan is a compiled field map: the columns an INSERT would write, in schema
// order. Omitted columns are absent so a database default fires; a mapped
// null is still in the list, and the sink writes an explicit NULL, which
// overrides a default on purpose.
//
// A non-empty diagnostic list means the plan must not be run. The plan is
// still returned so a caller can show every fault in one pass, including
// faults about columns that are not written.
type Plan struct {
	Write []Planned
	// Target is the sink's view of the write list. Omitted columns are
	// absent, so an INSERT names only the columns the plan fills.
	Target Target

	// binds, names, scratch, and cells are filled by prepare and reused
	// by Apply. Copying a Plan shares them; one Plan serves one Run.
	binds   []binding
	names   map[string]int
	scratch []byte
	hold    []byte
}

// binding is one written column with its source positions resolved.
// lit is set when the value does not depend on the row, so Apply copies
// bytes instead of running the chain again.
type binding struct {
	field Field
	col   schema.Column
	idx   []int
	lit   literal
	obj   bool
	// kind is classified once. classify walks the type spelling, and
	// strings.Fields inside that walk allocates; the type does not
	// change between rows.
	kind Kind
}

// literal is a value known at compile time. text is owned by the binding.
type literal struct {
	ok   bool
	kind Kind
	num  int64
	flt  float64
	text []byte
}

// Planned is one written column and the field that fills it.
type Planned struct {
	Column schema.Column
	Field  Field
}

// Compile checks m against sch before any row is read. Every fault is
// returned; the first one is not special. Import refuses a non-empty list.
// The map command writes the file anyway, because the file is what the next
// edit fixes.
func Compile(sch schema.Schema, m FieldMap) (*Plan, []Diagnostic) {
	cols := make(map[string]schema.Column, len(sch.Columns))
	for _, c := range sch.Columns {
		if _, ok := cols[c.Name]; !ok {
			cols[c.Name] = c
		}
	}
	knownSrc := sourceSet(m.Source)
	seen := map[string]bool{}
	handled := map[string]bool{}
	var diags []Diagnostic
	fieldsByTarget := map[string]Field{}

	for _, f := range m.Fields {
		if f.Target == "" {
			diags = append(diags, Diagnostic{Err: fmt.Errorf("%w: empty target", ErrNoOrigin)})
			continue
		}
		add := func(err error) {
			diags = append(diags, Diagnostic{Column: f.Target, Err: err})
		}
		if seen[f.Target] {
			add(ErrDuplicateTarget)
		}
		seen[f.Target] = true

		col, ok := cols[f.Target]
		if !ok {
			add(ErrStale)
			continue
		}
		// Counted before the identity rejection so the schema walk does not
		// also report the same column as unseen.
		handled[f.Target] = true
		if col.Identity {
			add(ErrIdentityTarget)
			continue
		}
		if err := originError(f); err != nil {
			add(err)
		}
		if knownSrc != nil {
			for _, name := range fieldSources(f) {
				if _, ok := knownSrc[name]; !ok {
					add(fmt.Errorf("%w: %s", ErrUnknownSource, name))
				}
			}
		}
		kind := classify(col.SQLType, col.NumericScale)
		if f.Format != "" && kind != KindTime {
			add(ErrFormatNotTime)
		}
		broken := false
		for i := range f.Transforms {
			if err := checkTransform(&f.Transforms[i]); err != nil {
				add(err)
				broken = true
			}
		}
		if err := chainKinds(f.Transforms); err != nil && !errors.Is(err, ErrUnknownTransform) {
			add(err)
			broken = true
		}
		if f.Object != nil {
			checkObjectTree(f.Object, add)
		}
		if !broken && f.Constant != nil {
			// A json column's constant is the document, not a string
			// that coercion would accept. "active" is legal text and
			// illegal JSON, and the failure is the map's, not row 1's.
			if isJSONDoc(col.SQLType) && !jsonValid(f.Constant) {
				add(fmt.Errorf("%w: constant is not valid JSON", ErrBadConstant))
			} else if _, _, err := Apply(col, f, nil); err != nil {
				add(fmt.Errorf("%w: %s", ErrBadConstant, err))
			}
		}
		fieldsByTarget[f.Target] = f
	}

	omitted := map[string]bool{}
	for _, name := range m.Omitted {
		if _, ok := cols[name]; !ok {
			diags = append(diags, Diagnostic{Column: name, Err: ErrStale})
			continue
		}
		omitted[name] = true
		col := cols[name]
		if !canOmit(col) {
			diags = append(diags, Diagnostic{Column: name, Err: ErrBadOmitted})
		}
	}

	plan := &Plan{}
	for _, col := range sch.Columns {
		if f, ok := fieldsByTarget[col.Name]; ok {
			plan.Write = append(plan.Write, Planned{Column: col, Field: f})
			continue
		}
		if omitted[col.Name] || handled[col.Name] {
			continue
		}
		if col.Identity || canOmit(col) {
			diags = append(diags, Diagnostic{Column: col.Name, Err: ErrUnseen})
			continue
		}
		diags = append(diags, Diagnostic{Column: col.Name, Err: ErrBlocker})
	}
	plan.prepare(sch, m)
	return plan, diags
}

// checkObjectTree rejects a leaf that has two origins, a format, a
// transform the chain cannot run, or a constant that is not valid UTF-8.
// A leaf constant is encoded as a JSON string; invalid UTF-8 would be
// rewritten or rejected by the parser, so it fails here, with the rest
// of the map, instead of on the first row.
func checkObjectTree(obj *Object, add func(error)) {
	if obj == nil {
		return
	}
	for i := range obj.Keys {
		k := &obj.Keys[i]
		f := k.asField()
		if err := originError(f); err != nil {
			add(err)
		}
		if f.Format != "" {
			add(ErrFormatNotTime)
		}
		for ti := range k.Transforms {
			if err := checkTransform(&k.Transforms[ti]); err != nil {
				add(err)
			}
		}
		if err := chainKinds(k.Transforms); err != nil && !errors.Is(err, ErrUnknownTransform) {
			add(err)
		}
		if k.Constant != nil && !utf8.ValidString(*k.Constant) {
			add(fmt.Errorf("%w: key %s: constant is not valid JSON", ErrBadConstant, k.Key))
		}
		if k.Object != nil {
			checkObjectTree(k.Object, add)
		}
	}
}

func canOmit(col schema.Column) bool {
	return col.Identity || col.Nullable || col.Default != ""
}

func originError(f Field) error {
	n := 0
	if f.Source != "" {
		n++
	}
	if len(f.Sources) > 0 {
		if f.Join == nil {
			return ErrSourcesNeedJoin
		}
		n++
	} else if f.Join != nil {
		return ErrSourcesNeedJoin
	}
	if len(f.Coalesce) > 0 {
		n++
	}
	if f.Constant != nil {
		n++
	}
	if f.Object != nil {
		n++
	}
	switch n {
	case 0:
		return ErrNoOrigin
	case 1:
		return nil
	default:
		return ErrTwoOrigins
	}
}

func fieldSources(f Field) []string {
	var names []string
	if f.Source != "" {
		names = append(names, f.Source)
	}
	names = append(names, f.Sources...)
	names = append(names, f.Coalesce...)
	if f.Object != nil {
		names = append(names, objectSources(f.Object)...)
	}
	return names
}

func objectSources(obj *Object) []string {
	var names []string
	for i := range obj.Keys {
		names = append(names, fieldSources(obj.Keys[i].asField())...)
	}
	return names
}

func sourceSet(spec SourceSpec) map[string]struct{} {
	if len(spec.Fields) == 0 && len(spec.Indexes) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(spec.Fields)+len(spec.Indexes))
	for _, name := range spec.Fields {
		set[name] = struct{}{}
	}
	for _, idx := range spec.Indexes {
		set[fmt.Sprintf("%d", idx)] = struct{}{}
	}
	return set
}

func checkTransform(t *Transform) error {
	switch t.Name {
	case "trim", "pad_left", "pad_right":
		if t.Name != "trim" && (t.Width < 1 || t.With == "") {
			return ErrBadPad
		}
		return nil
	case "case":
		switch t.Case {
		case "upper", "lower", "title":
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrUnknownCase, t.Case)
		}
	case "replace":
		if t.From == "" {
			return ErrEmptyReplace
		}
		return nil
	case "regex_replace":
		re, err := regexp.Compile(t.Pattern)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBadRegex, err)
		}
		t.re = re
		return nil
	case "map":
		if len(t.Accept) > 0 {
			return ErrUnfinishedLookup
		}
		return nil
	case "expr":
		return ErrExprReserved
	default:
		return fmt.Errorf("%w: %s", ErrUnknownTransform, t.Name)
	}
}

// classify maps a SQL type spelling onto a Kind. Unknown spellings stay text:
// a type the classifier has not met is still a column of characters, and
// refusing it would make a new database type unmappable. Scale zero on a
// numeric type is an integer because that is what numeric(12,0) stores;
// a bare "numeric" with no scale stays float so a fractional value is not
// rejected for a limit the schema never stated.
func classify(sqlType string, scale *int64) Kind {
	base := sqlBase(sqlType)
	switch {
	case base == "date" || base == "time" || strings.HasPrefix(base, "time ") ||
		strings.HasPrefix(base, "timestamp") || strings.HasPrefix(base, "datetime"):
		return KindTime
	case isIntType(base):
		return KindInt
	case isNumericType(base):
		sc := scale
		if sc == nil {
			if parsed, ok := scaleOf(schema.Column{SQLType: sqlType}); ok {
				sc = &parsed
			}
		}
		if sc != nil && *sc == 0 {
			return KindInt
		}
		return KindFloat
	case base == "bool" || base == "boolean" || base == "bit":
		return KindBool
	case isBinaryType(base):
		return KindBinary
	default:
		return KindText
	}
}

func sqlBase(sqlType string) string {
	t := strings.ToLower(strings.TrimSpace(sqlType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return strings.Join(strings.Fields(t), " ")
}

func isIntType(base string) bool {
	switch base {
	case "smallint", "integer", "int", "bigint", "tinyint", "mediumint",
		"serial", "bigserial", "smallserial", "int2", "int4", "int8",
		"serial2", "serial4", "serial8":
		return true
	default:
		return false
	}
}

func isNumericType(base string) bool {
	switch base {
	case "numeric", "decimal", "number", "dec", "real", "float", "float4",
		"float8", "double", "double precision", "money":
		return true
	default:
		return false
	}
}

func isBinaryType(base string) bool {
	switch base {
	case "bytea", "blob", "tinyblob", "mediumblob", "longblob", "binary",
		"varbinary", "image", "raw", "bit varying":
		return true
	default:
		return false
	}
}
