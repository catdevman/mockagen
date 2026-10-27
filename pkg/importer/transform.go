package importer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/catdevman/mockagen/pkg/schema"
)

// stepKind is the kind a chain step accepts and produces. Every shipped
// transform is text-to-text. The pair is what Compile checks, so a transform
// that leaves text cannot be added without a failing compile.
type stepKind int

const (
	stepText stepKind = iota
	stepOther
)

func transformStepKind(name string) (in, out stepKind, ok bool) {
	switch name {
	case "trim", "case", "replace", "regex_replace", "map", "pad_left", "pad_right":
		return stepText, stepText, true
	default:
		return 0, 0, false
	}
}

// chainKinds reports ErrChainKind when a step cannot read the value the
// previous step wrote. An unknown name is ErrUnknownTransform; Compile
// reports that on its own, and this check still refuses to pretend the
// chain lined up.
func chainKinds(steps []Transform) error {
	cur := stepText
	for _, s := range steps {
		in, out, ok := transformStepKind(s.Name)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknownTransform, s.Name)
		}
		if err := alignStep(cur, in, s.Name); err != nil {
			return err
		}
		cur = out
	}
	return nil
}

func alignStep(have, want stepKind, name string) error {
	if have != want {
		return fmt.Errorf("%w: %s", ErrChainKind, name)
	}
	return nil
}

// Apply runs one field: origin, string chain, null_if, coercion, default,
// then the schema's length, precision, and enum checks. inputs are the
// origin's raw cells in origin order. A constant ignores inputs.
//
// Coalesce runs the chain and null_if on each candidate and keeps the first
// value that is still non-null. A later candidate does not hide an error
// from an earlier one: a lookup miss is a broken row, not a blank.
func Apply(col schema.Column, f Field, inputs []string) (string, bool, error) {
	switch originOf(f) {
	case originCoalesce:
		cur, isNull, err := firstCoalesce(f, inputs)
		if err != nil {
			return "", false, err
		}
		if isNull {
			return finishNull(col, f)
		}
		return coerceAndCheck(col, f.Format, cur)
	default:
		raw, err := reduce(f, inputs)
		if err != nil {
			return "", false, err
		}
		cur, err := applyChain(f.Transforms, raw)
		if err != nil {
			return "", false, err
		}
		if listed(cur, f.NullIf) {
			return finishNull(col, f)
		}
		return coerceAndCheck(col, f.Format, cur)
	}
}

type origin int

const (
	originNone origin = iota
	originSource
	originJoin
	originCoalesce
	originConstant
	originBad
)

func originOf(f Field) origin {
	n := 0
	var which origin
	if f.Source != "" {
		n++
		which = originSource
	}
	// sources without join used to mean coalesce. That reading is rejected
	// so a missing join cannot silently change which cell is kept.
	switch {
	case len(f.Sources) > 0 && f.Join != nil:
		n++
		which = originJoin
	case len(f.Sources) > 0 || f.Join != nil:
		return originBad
	}
	if len(f.Coalesce) > 0 {
		n++
		which = originCoalesce
	}
	if f.Constant != nil {
		n++
		which = originConstant
	}
	if n != 1 {
		return originBad
	}
	return which
}

func reduce(f Field, inputs []string) (string, error) {
	switch originOf(f) {
	case originConstant:
		return *f.Constant, nil
	case originSource:
		if len(inputs) != 1 {
			return "", fmt.Errorf("importer: source %q: got %d values, want 1", f.Source, len(inputs))
		}
		return inputs[0], nil
	case originJoin:
		if len(inputs) != len(f.Sources) {
			return "", fmt.Errorf("importer: join %q: got %d values, want %d", f.Target, len(inputs), len(f.Sources))
		}
		sep := ""
		if f.Join != nil {
			sep = *f.Join
		}
		parts := make([]string, 0, len(inputs))
		for _, p := range inputs {
			// An empty part contributes no separator. "Ada" plus "" would
			// otherwise become "Ada " and survive trim only because trim
			// happens to be in the chain.
			if p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, sep), nil
	default:
		return "", ErrNoOrigin
	}
}

func firstCoalesce(f Field, inputs []string) (string, bool, error) {
	if len(inputs) != len(f.Coalesce) {
		return "", false, fmt.Errorf("importer: coalesce %q: got %d values, want %d", f.Target, len(inputs), len(f.Coalesce))
	}
	for _, in := range inputs {
		cur, err := applyChain(f.Transforms, in)
		if err != nil {
			return "", false, err
		}
		if !listed(cur, f.NullIf) {
			return cur, false, nil
		}
	}
	return "", true, nil
}

func finishNull(col schema.Column, f Field) (string, bool, error) {
	if f.Default == nil {
		return "", true, nil
	}
	// format describes the source file. A default is the value that lands
	// in the column, so it is read with the canonical parsers, not the
	// source layout. Using the layout here would reject a default of
	// 2006-01-02 on a column whose CSV dates are 01/02/2006.
	return coerceAndCheck(col, "", *f.Default)
}

func applyChain(steps []Transform, s string) (string, error) {
	cur := s
	for i := range steps {
		var err error
		cur, err = applyStep(&steps[i], cur)
		if err != nil {
			return "", err
		}
	}
	return cur, nil
}

func applyStep(t *Transform, s string) (string, error) {
	switch t.Name {
	case "trim":
		if t.HasCutset {
			return strings.Trim(s, t.Cutset), nil
		}
		return strings.TrimSpace(s), nil
	case "case":
		switch t.Case {
		case "upper":
			return strings.ToUpper(s), nil
		case "lower":
			return strings.ToLower(s), nil
		case "title":
			return titleCase(s), nil
		default:
			return "", fmt.Errorf("%w: %s", ErrUnknownCase, t.Case)
		}
	case "replace":
		if t.From == "" {
			return "", ErrEmptyReplace
		}
		return strings.ReplaceAll(s, t.From, t.To), nil
	case "regex_replace":
		re := t.re
		if re == nil {
			var err error
			re, err = regexp.Compile(t.Pattern)
			if err != nil {
				return "", fmt.Errorf("%w: %s", ErrBadRegex, err)
			}
		}
		return re.ReplaceAllString(s, t.To), nil
	case "map":
		if len(t.Accept) > 0 && len(t.Pairs) == 0 {
			return "", ErrUnfinishedLookup
		}
		for _, p := range t.Pairs {
			if p.From == s {
				return p.To, nil
			}
		}
		if t.LookupDefault != nil {
			return *t.LookupDefault, nil
		}
		return "", fmt.Errorf("%w: %q", ErrLookupMiss, s)
	case "pad_left", "pad_right":
		return pad(s, t.Width, t.With, t.Name == "pad_left")
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownTransform, t.Name)
	}
}

// titleCase uppercases the first rune of each word and lowercases the rest.
// golang.org/x/text/cases would do this with a language tag, and one case
// fold is not worth taking that module on directly.
func titleCase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	start := true
	for _, r := range s {
		if unicode.IsSpace(r) {
			b.WriteRune(r)
			start = true
			continue
		}
		if start {
			b.WriteRune(unicode.ToUpper(r))
			start = false
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func pad(s string, width int, with string, left bool) (string, error) {
	if width < 1 || with == "" {
		return "", ErrBadPad
	}
	have := utf8.RuneCountInString(s)
	if have >= width {
		return s, nil
	}
	need := width - have
	fill := repeatRunes(with, need)
	if left {
		return fill + s, nil
	}
	return s + fill, nil
}

func repeatRunes(with string, n int) string {
	runes := []rune(with)
	out := make([]rune, n)
	for i := range out {
		out[i] = runes[i%len(runes)]
	}
	return string(out)
}

func listed(s string, list []string) bool {
	for _, item := range list {
		if s == item {
			return true
		}
	}
	return false
}

func coerceAndCheck(col schema.Column, format, s string) (string, bool, error) {
	kind := classify(col.SQLType, col.NumericScale)
	canon, err := coerce(kind, format, s)
	if err != nil {
		return "", false, err
	}
	if err := checkFacts(col, kind, canon); err != nil {
		return "", false, err
	}
	return canon, false, nil
}

func coerce(kind Kind, format, s string) (string, error) {
	switch kind {
	case KindText:
		return s, nil
	case KindInt:
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(n, 10), nil
	case KindFloat:
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return "", err
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	case KindBool:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "t", "1", "yes":
			return "true", nil
		case "false", "f", "0", "no":
			return "false", nil
		default:
			return "", fmt.Errorf("invalid boolean %q", s)
		}
	case KindTime:
		return coerceTime(format, s)
	case KindBinary:
		return "", fmt.Errorf("binary value %q", s)
	default:
		return s, nil
	}
}

// coerceTime canonicalises to RFC3339 and keeps the offset it was given.
// A timestamp without time zone stores that clock; shifting it to UTC
// would turn 15:04-07:00 into 22:04Z. A date-only parse is already UTC,
// so that result still ends in Z.
func coerceTime(format, s string) (string, error) {
	s = strings.TrimSpace(s)
	layouts := []string{format}
	if format == "" {
		layouts = []string{time.RFC3339, time.RFC3339Nano, "2006-01-02"}
	}
	var last error
	for _, layout := range layouts {
		tm, err := time.Parse(layout, s)
		if err != nil {
			last = err
			continue
		}
		return tm.Format(time.RFC3339), nil
	}
	if format != "" {
		return "", last
	}
	return "", fmt.Errorf("invalid time %q", s)
}

func checkFacts(col schema.Column, kind Kind, canon string) error {
	if col.MaxLength != nil && kind == KindText {
		if n := utf8.RuneCountInString(canon); int64(n) > *col.MaxLength {
			return fmt.Errorf("length %d exceeds %d", n, *col.MaxLength)
		}
	}
	if len(col.EnumValues) > 0 && !listed(canon, col.EnumValues) {
		return fmt.Errorf("%q is not an allowed value", canon)
	}
	if kind == KindInt || kind == KindFloat {
		if err := checkNumeric(canon, col); err != nil {
			return err
		}
	}
	return nil
}

func checkNumeric(canon string, col schema.Column) error {
	intDigits, fracDigits, err := decimalDigits(canon)
	if err != nil {
		return err
	}
	scale, hasScale := scaleOf(col)
	prec, hasPrec := precisionOf(col)
	if hasScale && int64(fracDigits) > scale {
		return fmt.Errorf("scale %d exceeds %d", fracDigits, scale)
	}
	if hasPrec && hasScale {
		if scale < 0 {
			scale = 0
		}
		if int64(intDigits) > prec-scale {
			return fmt.Errorf("precision %d exceeds %d", intDigits+fracDigits, prec)
		}
		return nil
	}
	if hasPrec && int64(intDigits+fracDigits) > prec {
		return fmt.Errorf("precision %d exceeds %d", intDigits+fracDigits, prec)
	}
	return nil
}

func precisionOf(col schema.Column) (int64, bool) {
	if col.NumericPrecision != nil {
		return *col.NumericPrecision, true
	}
	return parseTypeNum(col.SQLType, 0)
}

func scaleOf(col schema.Column) (int64, bool) {
	if col.NumericScale != nil {
		return *col.NumericScale, true
	}
	return parseTypeNum(col.SQLType, 1)
}

// parseTypeNum reads one side of numeric(12,2). idx 0 is precision, 1 is
// scale. A bare "numeric" has neither, and the caller treats that as "no
// limit" rather than scale 0, which would reject every fractional value.
func parseTypeNum(sqlType string, idx int) (int64, bool) {
	t := strings.ToLower(sqlType)
	open := strings.IndexByte(t, '(')
	if open < 0 {
		return 0, false
	}
	inner := t[open+1:]
	if c := strings.IndexByte(inner, ')'); c >= 0 {
		inner = inner[:c]
	}
	parts := strings.Split(inner, ",")
	if idx >= len(parts) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(parts[idx]), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func decimalDigits(s string) (intDigits, fracDigits int, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return 0, 0, fmt.Errorf("empty number")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return 0, 0, fmt.Errorf("invalid number %q", s)
	}
	intPart := strings.TrimLeft(parts[0], "0")
	intDigits = len(intPart)
	if len(parts) == 2 {
		fracDigits = len(strings.TrimRight(parts[1], "0"))
	}
	return intDigits, fracDigits, nil
}
