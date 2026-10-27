package introspect

import (
	"slices"
	"strings"
)

// parsedCheck is one single-column IN list a dialect's check text actually
// contained. ok=false from a parser means the expression was not that shape.
// JSON predicates (ISJSON, JSON_PATH_EXISTS, JSON_SCHEMA_VALID,
// jsonb_typeof) are not an IN list, so they come back empty rather than
// being turned into enum_values or a key list the catalog does not store.
type parsedCheck struct {
	column string
	values []string
}

// parsePostgresCheck reads the form pg_get_constraintdef emits for
// CHECK (x IN (...)): an ANY(ARRAY[...]) with a cast on every element.
// Anything else, including a JSON predicate, is not an enum.
func parsePostgresCheck(def string) (string, []string, bool) {
	s := strings.TrimSpace(def)
	p := sc{s: s}
	if p.eat("CHECK") {
		s = strings.TrimSpace(p.rest())
	}
	s = stripParenLayers(s)
	p = sc{s: s}
	col, ok := p.ident()
	if !ok || !p.eat("=") || !p.eat("ANY") || !p.eat("(") || !p.eat("ARRAY") || !p.eat("[") {
		return "", nil, false
	}
	var values []string
	for {
		p.skip()
		if p.starts("]") {
			p.i++
			break
		}
		val, ok := p.sqlString()
		if !ok {
			return "", nil, false
		}
		p.skip()
		if p.eat("::") && !p.typeName() {
			return "", nil, false
		}
		values = append(values, val)
		p.skip()
		if p.starts(",") {
			p.i++
			continue
		}
		if p.starts("]") {
			p.i++
			break
		}
		return "", nil, false
	}
	if !p.eat(")") {
		return "", nil, false
	}
	p.skip()
	if p.i != len(p.s) || len(values) == 0 {
		return "", nil, false
	}
	return col, values, true
}

// parseMySQLCheck reads a CHECK_CLAUSE. MySQL keeps IN, but stores charset
// introducers and backslash-escaped quotes, and it does not say which
// column the check belongs to, so the name has to come out of the text.
func parseMySQLCheck(def string) (string, []string, bool) {
	s := stripParenLayers(unescapeMySQL(def))
	p := sc{s: s}
	col, ok := p.ident()
	if !ok || !p.eat("in") || !p.eat("(") {
		return "", nil, false
	}
	var values []string
	for {
		p.skip()
		if p.starts(")") {
			p.i++
			break
		}
		p.skipIntroducer()
		val, ok := p.sqlString()
		if !ok {
			return "", nil, false
		}
		values = append(values, val)
		p.skip()
		if p.starts(",") {
			p.i++
			continue
		}
		if p.starts(")") {
			p.i++
			break
		}
		return "", nil, false
	}
	p.skip()
	if p.i != len(p.s) || len(values) == 0 {
		return "", nil, false
	}
	return col, values, true
}

// parseMySQLEnum reads the values out of COLUMN_TYPE. MySQL has no enum
// catalog; the labels live in the type spelling, and a quote inside a
// label is doubled, so splitting on commas corrupts "has, comma".
func parseMySQLEnum(columnType string) ([]string, bool) {
	s := strings.TrimSpace(columnType)
	if len(s) < 6 || !strings.EqualFold(s[:4], "enum") {
		return nil, false
	}
	rest := strings.TrimSpace(s[4:])
	if len(rest) < 2 || rest[0] != '(' || rest[len(rest)-1] != ')' {
		return nil, false
	}
	p := sc{s: rest[1 : len(rest)-1]}
	var values []string
	for {
		p.skip()
		if p.i >= len(p.s) {
			break
		}
		val, ok := p.sqlString()
		if !ok {
			return nil, false
		}
		values = append(values, val)
		p.skip()
		if p.starts(",") {
			p.i++
			continue
		}
		if p.i != len(p.s) {
			return nil, false
		}
		break
	}
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}

// parseMSSQLCheck reads the OR-chain sys.check_constraints returns for an
// IN list. The engine reverses declaration order and does not keep it, so
// the values are sorted: a set with a stable spelling, not a guess at the
// order the user wrote.
func parseMSSQLCheck(def string) (string, []string, bool) {
	parts := splitKeyword(stripParenLayers(def), "OR")
	if len(parts) == 0 {
		return "", nil, false
	}
	var column string
	var values []string
	for _, part := range parts {
		p := sc{s: stripParenLayers(part)}
		col, ok := p.ident()
		if !ok || !p.eat("=") {
			return "", nil, false
		}
		p.skip()
		if p.i+1 < len(p.s) && (p.s[p.i] == 'N' || p.s[p.i] == 'n') && p.s[p.i+1] == '\'' {
			p.i++
		}
		val, ok := p.sqlString()
		if !ok {
			return "", nil, false
		}
		p.skip()
		if p.i != len(p.s) {
			return "", nil, false
		}
		if column == "" {
			column = col
		} else if column != col {
			return "", nil, false
		}
		values = append(values, val)
	}
	if len(values) == 0 {
		return "", nil, false
	}
	// Byte order, not collation order. The labels are case-sensitive and
	// the original order is gone; this only has to be stable.
	slices.Sort(values)
	return column, values, true
}

// parseSQLiteChecks finds CHECK (x IN (...)) clauses in the CREATE TABLE
// text sqlite_master stores. SQLite is the one engine that keeps the
// clause as written, so declaration order survives. A check that is not
// an IN list is ignored, which is how a JSON predicate stays out of
// enum_values.
func parseSQLiteChecks(ddl string) []parsedCheck {
	var out []parsedCheck
	i := 0
	for i < len(ddl) {
		switch ddl[i] {
		case '\'':
			i = skipString(ddl, i)
		case '"', '`':
			i = skipDelimited(ddl, i, ddl[i])
		case '[':
			i = skipDelimited(ddl, i, ']')
		case '-':
			if i+1 < len(ddl) && ddl[i+1] == '-' {
				for i < len(ddl) && ddl[i] != '\n' {
					i++
				}
				continue
			}
			i++
		case '/':
			if i+1 < len(ddl) && ddl[i+1] == '*' {
				i += 2
				for i+1 < len(ddl) && !(ddl[i] == '*' && ddl[i+1] == '/') {
					i++
				}
				if i+1 < len(ddl) {
					i += 2
				} else {
					i = len(ddl)
				}
				continue
			}
			i++
		default:
			if isIdentStart(ddl[i]) && (i == 0 || !isIdentChar(ddl[i-1])) {
				j := i + 1
				for j < len(ddl) && isIdentChar(ddl[j]) {
					j++
				}
				if strings.EqualFold(ddl[i:j], "CHECK") {
					rest := strings.TrimLeft(ddl[j:], " \t\r\n")
					if strings.HasPrefix(rest, "(") {
						inner, n, ok := balanced(rest)
						if ok {
							if col, vals, ok := parseInList(inner); ok {
								out = append(out, parsedCheck{column: col, values: vals})
							}
						}
						trimmed := len(ddl[j:]) - len(rest)
						i = j + trimmed + n
						continue
					}
				}
				i = j
				continue
			}
			i++
		}
	}
	return out
}

// parseInList reads "col IN ('a', 'b')" with the parentheses already
// removed. Leftover AND/OR is a different predicate and not an enum.
func parseInList(expr string) (string, []string, bool) {
	p := sc{s: stripParenLayers(expr)}
	col, ok := p.ident()
	if !ok || !p.eat("IN") || !p.eat("(") {
		return "", nil, false
	}
	var values []string
	for {
		p.skip()
		if p.starts(")") {
			p.i++
			break
		}
		val, ok := p.sqlString()
		if !ok {
			return "", nil, false
		}
		values = append(values, val)
		p.skip()
		if p.starts(",") {
			p.i++
			continue
		}
		if p.starts(")") {
			p.i++
			break
		}
		return "", nil, false
	}
	p.skip()
	if p.i != len(p.s) || len(values) == 0 {
		return "", nil, false
	}
	return col, values, true
}

// unescapeMySQL turns the backslash escapes CHECK_CLAUSE stores into the
// characters the IN-list parser understands. The introducer is left for
// the parser: it is not part of the label.
func unescapeMySQL(s string) string {
	return strings.ReplaceAll(s, `\'`, `'`)
}

// stripParenLayers removes wrapping parentheses, but not the call
// parentheses of sysutcdatetime() or a ")" that lives inside a string.
// SQL Server wraps defaults in one layer and numeric defaults in two.
func stripParenLayers(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' && wrapsFully(s) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// wrapsFully reports whether the outer parentheses enclose the whole
// string. (sysutcdatetime()) is one layer; a ")" inside a default
// string is not the closer, and a trailing extra character means the
// outer pair is not wrapping.
func wrapsFully(s string) bool {
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
			if depth == 0 && i != len(s)-1 {
				return false
			}
		}
	}
	return depth == 0
}

// splitKeyword splits on a bare keyword at depth zero, outside strings.
// mssql's rewritten IN list is "a = 'x' OR a = 'y'", and a label can
// itself contain the letters OR.
func splitKeyword(s, word string) []string {
	var parts []string
	depth := 0
	inStr := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && hasKeywordAt(s, i, word) {
				parts = append(parts, s[start:i])
				i += len(word) - 1
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// hasKeywordAt matches a keyword that is not the middle of an
// identifier. A label containing the letters OR must not split the chain.
func hasKeywordAt(s string, i int, word string) bool {
	if i+len(word) > len(s) || !strings.EqualFold(s[i:i+len(word)], word) {
		return false
	}
	if i > 0 && isIdentChar(s[i-1]) {
		return false
	}
	end := i + len(word)
	if end < len(s) && isIdentChar(s[end]) {
		return false
	}
	return true
}

// sc is a cursor over one check expression. The four engines rewrite
// CHECK differently, and none of those shapes is worth a SQL parser
// dependency.
type sc struct {
	s string
	i int
}

func (p *sc) rest() string { return p.s[p.i:] }

func (p *sc) skip() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *sc) starts(lit string) bool {
	return strings.HasPrefix(p.s[p.i:], lit)
}

func (p *sc) eat(lit string) bool {
	p.skip()
	if p.i+len(lit) > len(p.s) || !strings.EqualFold(p.s[p.i:p.i+len(lit)], lit) {
		return false
	}
	if lit != "" && isIdentStart(lit[0]) {
		end := p.i + len(lit)
		if end < len(p.s) && isIdentChar(p.s[end]) {
			return false
		}
	}
	p.i += len(lit)
	return true
}

func (p *sc) ident() (string, bool) {
	p.skip()
	if p.i >= len(p.s) {
		return "", false
	}
	switch p.s[p.i] {
	case '"':
		return p.quoted('"')
	case '`':
		return p.quoted('`')
	case '[':
		return p.quoted(']')
	default:
		if !isIdentStart(p.s[p.i]) {
			return "", false
		}
		start := p.i
		p.i++
		for p.i < len(p.s) && isIdentChar(p.s[p.i]) {
			p.i++
		}
		return p.s[start:p.i], true
	}
}

func (p *sc) quoted(close byte) (string, bool) {
	if p.i >= len(p.s) {
		return "", false
	}
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		if p.s[p.i] == close {
			if p.i+1 < len(p.s) && p.s[p.i+1] == close {
				b.WriteByte(close)
				p.i += 2
				continue
			}
			p.i++
			return b.String(), true
		}
		b.WriteByte(p.s[p.i])
		p.i++
	}
	return "", false
}

func (p *sc) sqlString() (string, bool) {
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != '\'' {
		return "", false
	}
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		if p.s[p.i] == '\'' {
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				b.WriteByte('\'')
				p.i += 2
				continue
			}
			p.i++
			return b.String(), true
		}
		b.WriteByte(p.s[p.i])
		p.i++
	}
	return "", false
}

func (p *sc) typeName() bool {
	p.skip()
	if p.i < len(p.s) && p.s[p.i] == '"' {
		_, ok := p.quoted('"')
		return ok
	}
	if p.i >= len(p.s) || !isIdentStart(p.s[p.i]) {
		return false
	}
	for {
		p.i++
		for p.i < len(p.s) && isIdentChar(p.s[p.i]) {
			p.i++
		}
		if p.i < len(p.s) && p.s[p.i] == '.' {
			p.i++
			if p.i >= len(p.s) || !isIdentStart(p.s[p.i]) {
				return false
			}
			continue
		}
		return true
	}
}

// skipIntroducer drops a leading _utf8mb4 (or any other charset
// introducer) so it does not become part of the label.
func (p *sc) skipIntroducer() {
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != '_' {
		return
	}
	j := p.i + 1
	if j >= len(p.s) || !isIdentStart(p.s[j]) {
		return
	}
	j++
	for j < len(p.s) && isIdentChar(p.s[j]) {
		j++
	}
	k := j
	for k < len(p.s) && (p.s[k] == ' ' || p.s[k] == '\t') {
		k++
	}
	if k < len(p.s) && p.s[k] == '\'' {
		p.i = k
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func skipString(s string, i int) int {
	i++
	for i < len(s) {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

func skipDelimited(s string, i int, close byte) int {
	i++
	for i < len(s) {
		if s[i] == close {
			if i+1 < len(s) && s[i+1] == close {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

// balanced returns the inside of a parenthesised group and the number of
// bytes consumed, including the parentheses. Strings are not depth.
func balanced(s string) (string, int, bool) {
	if s == "" || s[0] != '(' {
		return "", 0, false
	}
	depth := 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], i + 1, true
			}
		}
	}
	return "", 0, false
}
