package introspect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen/sqlgen"
	"github.com/catdevman/mockagen/pkg/schema"
)

// ErrUnknownDialect means the name is not one of the four catalogs this
// package knows how to read. An empty name is the same error: sqlgen.Lookup
// treats empty as its default dialect, and querying postgres because the
// flag was forgotten would write a file about the wrong engine.
var ErrUnknownDialect = errors.New("introspect: unknown dialect")

// Request is one table to read. Schema is the namespace the catalog query
// filters on (postgres schema, mysql database, mssql schema) and is empty
// for sqlite, which has none. Database is provenance only; it is copied
// onto the file and is not used to find the table.
type Request struct {
	Dialect  string
	Schema   string
	Table    string
	Database string
	// At is captured_at. The zero time means now, so a caller cannot stamp
	// a file with year one by forgetting the field.
	At time.Time
}

// Inspect reads one table into a schema file's facts. Generator decisions
// are left empty: inference is a later pass, and writing a type here would
// be a guess this package has not made.
func Inspect(ctx context.Context, q Querier, req Request) (schema.Schema, error) {
	if q == nil {
		return schema.Schema{}, errors.New("introspect: nil querier")
	}
	dialect, err := canonicalDialect(req.Dialect)
	if err != nil {
		return schema.Schema{}, err
	}
	if strings.TrimSpace(req.Table) == "" {
		return schema.Schema{}, errors.New("introspect: table is required")
	}
	if dialect != "sqlite" && strings.TrimSpace(req.Schema) == "" {
		return schema.Schema{}, fmt.Errorf("introspect: %s requires a schema", dialect)
	}
	req.Table = strings.TrimSpace(req.Table)
	req.Schema = strings.TrimSpace(req.Schema)
	switch dialect {
	case "postgres":
		return inspectPostgres(ctx, q, req)
	case "mysql":
		return inspectMySQL(ctx, q, req)
	case "mssql":
		return inspectMSSQL(ctx, q, req)
	case "sqlite":
		return inspectSQLite(ctx, q, req)
	default:
		return schema.Schema{}, fmt.Errorf("%w: %s", ErrUnknownDialect, dialect)
	}
}

func canonicalDialect(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%w: empty", ErrUnknownDialect)
	}
	d, err := sqlgen.Lookup(name)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnknownDialect, err)
	}
	switch d.Name {
	case "postgres", "mysql", "mssql", "sqlite":
		return d.Name, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownDialect, name)
	}
}

// finish stamps provenance and orders columns by ordinal. Ordinal is the
// database's own order; leaving the slice in whatever order a driver
// returned the rows would make two reads of one table disagree.
func finish(dialect string, req Request, qualify bool, cols []schema.Column) schema.Schema {
	slices.SortStableFunc(cols, func(a, b schema.Column) int {
		return cmp.Compare(a.Ordinal, b.Ordinal)
	})
	at := req.At
	if at.IsZero() {
		at = time.Now()
	}
	// One spelling for the instant. yaml.v3 writes the offset it is given,
	// so a caller in +02:00 and a caller in Z would otherwise produce
	// different files for the same moment.
	at = at.UTC()
	table := req.Table
	if qualify && req.Schema != "" {
		table = req.Schema + "." + req.Table
	}
	return schema.Schema{
		Version: "mockagen/v1",
		Kind:    "Schema",
		Name:    req.Table,
		Source: schema.Source{
			Dialect:    dialect,
			Database:   req.Database,
			Table:      table,
			CapturedAt: at,
		},
		Columns: cols,
	}
}

// constraint is one catalog row. A composite arrives as several rows
// that share a name; they are grouped before a column is marked unique,
// because one of the columns in a pair is not itself unique.
type constraint struct {
	name       string
	kind       string
	column     string
	refSchema  string
	refTable   string
	refColumn  string
	definition string
}

// applyConstraints folds catalog constraint rows onto columns.
//
// unique is set only for a single-column unique constraint. A composite
// unique covers the pair, and marking either column unique because of it
// would make inference treat a non-unique column as a key.
//
// A check that is not a single-column IN list is ignored. That is what
// keeps ISJSON, JSON_PATH_EXISTS, JSON_SCHEMA_VALID and jsonb_typeof from
// becoming enum_values. An enum catalog that already filled the column
// wins over the check: the catalog has the declared order and the check
// text, on mssql, does not.
func applyConstraints(cols []schema.Column, cons []constraint, tableSchema string, parse func(string) (string, []string, bool)) {
	type group struct {
		kind string
		cols []string
		refs []schema.ForeignKey
		def  string
	}
	var order []string
	groups := map[string]*group{}
	for _, c := range cons {
		if c.kind == "" {
			continue
		}
		key := c.kind + "\x00" + c.name
		g, ok := groups[key]
		if !ok {
			g = &group{kind: c.kind}
			groups[key] = g
			order = append(order, key)
		}
		if c.definition != "" {
			g.def = c.definition
		}
		if c.column == "" {
			continue
		}
		g.cols = append(g.cols, c.column)
		if c.refTable != "" {
			g.refs = append(g.refs, schema.ForeignKey{
				Table:  qualifyRef(c.refSchema, tableSchema, c.refTable),
				Column: c.refColumn,
			})
		}
	}
	for _, key := range order {
		g := groups[key]
		switch g.kind {
		case "p":
			for _, name := range g.cols {
				if i := colIndex(cols, name); i >= 0 {
					cols[i].PrimaryKey = true
				}
			}
		case "u":
			if len(g.cols) == 1 {
				if i := colIndex(cols, g.cols[0]); i >= 0 {
					cols[i].Unique = true
				}
			}
		case "f":
			if len(g.cols) == 0 || len(g.cols) != len(g.refs) {
				continue
			}
			for n, name := range g.cols {
				i := colIndex(cols, name)
				if i < 0 || cols[i].References != nil {
					continue
				}
				ref := g.refs[n]
				cols[i].References = &ref
			}
		case "c":
			if parse == nil || g.def == "" {
				continue
			}
			col, values, ok := parse(g.def)
			if !ok {
				continue
			}
			// The catalog and the text disagree about which column this
			// is. Attaching the values would put an enum on the wrong
			// field, which is worse than leaving it unresolved.
			if len(g.cols) == 1 && g.cols[0] != col {
				continue
			}
			attachEnum(cols, col, values)
		}
	}
}

// attachEnum does not replace labels already stored. The enum catalog
// is applied first and has declaration order; a CHECK, on mssql, does not.
func attachEnum(cols []schema.Column, column string, values []string) {
	if column == "" || len(values) == 0 {
		return
	}
	i := colIndex(cols, column)
	if i < 0 || len(cols[i].EnumValues) > 0 {
		return
	}
	cols[i].EnumValues = slices.Clone(values)
}

// colIndex matches the catalog spelling exactly. Folding would merge two
// quoted columns that differ only by case, which postgres allows.
func colIndex(cols []schema.Column, name string) int {
	for i := range cols {
		if cols[i].Name == name {
			return i
		}
	}
	return -1
}

// qualifyRef leaves a same-schema reference as the bare table. ForeignKey
// has no schema field; a cross-schema reference still has to say which
// schema, or two tables with the same name become indistinguishable.
func qualifyRef(refSchema, tableSchema, refTable string) string {
	if refSchema == "" || strings.EqualFold(refSchema, tableSchema) {
		return refTable
	}
	return refSchema + "." + refTable
}

// normKind accepts both spellings the catalogs use. Postgres returns
// p/u/f/c from contype; information_schema returns PRIMARY KEY.
func normKind(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "P", "PRIMARY KEY", "PRIMARY":
		return "p"
	case "U", "UNIQUE":
		return "u"
	case "F", "FOREIGN KEY":
		return "f"
	case "C", "CHECK":
		return "c"
	default:
		return ""
	}
}

// field folds the key. The drivers do not agree on the case of an
// alias, and a miss is a NULL fact rather than an error.
func field(row Row, name string) (string, bool) {
	if v, ok := row[name]; ok {
		return v, true
	}
	for k, v := range row {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

func text(row Row, name string) string {
	s, ok := field(row, name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// truth accepts the flag spellings the engines and drivers return:
// 1, t, true, yes. Anything else, including a missing cell, is false,
// which is also what a NULL flag means.
func truth(row Row, name string) bool {
	s, ok := field(row, name)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y":
		return true
	default:
		return false
	}
}

// optInt treats a missing key as NULL, not zero. Scale 0 is a real
// scale; a type with no scale has to stay nil or the two look the same.
func optInt(row Row, name string) (*int64, error) {
	s, ok := field(row, name)
	if !ok || strings.TrimSpace(s) == "" {
		return nil, nil
	}
	n, err := parseIntString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("introspect: %s: %w", name, err)
	}
	return &n, nil
}

// reqInt is for a field a column row cannot lack. A NULL ordinal is
// "not introspected", sorts ahead of every real column, and then
// disappears under omitempty.
func reqInt(row Row, name string) (int, error) {
	n, err := optInt(row, name)
	if err != nil {
		return 0, err
	}
	if n == nil {
		return 0, fmt.Errorf("introspect: missing %s", name)
	}
	return int(*n), nil
}

// parseIntString accepts a trailing ".0". sqlite -json and some drivers
// hand an integer catalog column back as a decimal string.
func parseIntString(s string) (int64, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, fmt.Errorf("not an integer: %s", s)
	}
	return int64(f), nil
}

// dropSentinelLength omits a negative length. SQL Server stores MAX as -1
// in sys.columns; the assembler already turns that into NULL, and a
// negative that still arrives is not a width.
func dropSentinelLength(n *int64) *int64 {
	if n != nil && *n < 0 {
		return nil
	}
	return n
}
