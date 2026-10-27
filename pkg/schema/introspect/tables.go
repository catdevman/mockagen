package introspect

import (
	"context"
	"fmt"
)

// Table is one relation the catalog is willing to show. ApproxRows is nil
// when the engine has no estimate. Postgres reports -1, not 0, for a table
// that has never been analysed; showing that as zero rows is a lie about a
// full table. SQLite has no estimate at all, and counting would be a full
// scan on the table-picker screen.
type Table struct {
	Schema     string
	Name       string
	ApproxRows *int64
}

// Tables lists base tables for the dialect's table picker. It does not open
// a connection; the caller passes the same Querier Inspect uses.
func Tables(ctx context.Context, q Querier, dialect string) ([]Table, error) {
	if q == nil {
		return nil, fmt.Errorf("introspect: nil querier")
	}
	name, err := canonicalDialect(dialect)
	if err != nil {
		return nil, err
	}
	var query string
	switch name {
	case "postgres":
		query = postgresTableSQL
	case "mysql":
		query = mysqlTableSQL
	case "mssql":
		query = mssqlTableSQL
	case "sqlite":
		query = sqliteTableSQL
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownDialect, name)
	}
	rows, err := q.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("introspect: %s tables: %w", name, err)
	}
	out := make([]Table, 0, len(rows))
	for _, row := range rows {
		tbl := Table{
			Schema: text(row, "schema"),
			Name:   text(row, "name"),
		}
		if name == "sqlite" {
			out = append(out, tbl)
			continue
		}
		n, err := optInt(row, "approx_rows")
		if err != nil {
			return nil, err
		}
		// -1 is postgres's "never analysed", not a row count. Any other
		// negative is the same kind of sentinel and is not a count either.
		if n != nil && *n >= 0 {
			tbl.ApproxRows = n
		}
		out = append(out, tbl)
	}
	return out, nil
}

// readConstraints drops a kind it does not know. Treating an
// unrecognised constraint as a check would attach its text to a column.
func readConstraints(ctx context.Context, q Querier, query string, args ...any) ([]constraint, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]constraint, 0, len(rows))
	for _, row := range rows {
		kind := normKind(text(row, "kind"))
		if kind == "" {
			continue
		}
		out = append(out, constraint{
			name:       text(row, "name"),
			kind:       kind,
			column:     text(row, "column_name"),
			refSchema:  text(row, "ref_schema"),
			refTable:   text(row, "ref_table"),
			refColumn:  text(row, "ref_column"),
			definition: text(row, "definition"),
		})
	}
	return out, nil
}
