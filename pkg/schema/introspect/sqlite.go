package introspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/catdevman/mockagen/pkg/schema"
)

// cid is 0-based. Column.Ordinal is 1-based, and zero means "not
// introspected": a stored 0 would sort ahead of every real column and then
// disappear under omitempty. The addition is the whole conversion.
const sqliteColumnSQL = `
SELECT cid AS ordinal, name, type AS sql_type,
       "notnull", dflt_value AS default_expr, pk
FROM pragma_table_info(?)
ORDER BY cid`

const sqliteIndexSQL = `
SELECT il.name AS index_name, il.origin, il.[unique],
       ii.seqno AS position, ii.name AS column_name
FROM pragma_index_list(?) il
JOIN pragma_index_info(il.name) ii
WHERE il.[unique] = 1
ORDER BY il.name, ii.seqno`

const sqliteFKSQL = `
SELECT id, seq AS position, "table" AS ref_table,
       "from" AS column_name, "to" AS ref_column
FROM pragma_foreign_key_list(?)`

// There is no check catalog. sqlite_master.sql is the CREATE TABLE
// text, and it is the only place an IN list still has declaration order.
const sqliteMasterSQL = `
SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`

// sqlite_% is the engine's own tables. Listing them would offer
// sqlite_sequence in the table picker.
const sqliteTableSQL = `
SELECT name FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
ORDER BY name`

func inspectSQLite(ctx context.Context, q Querier, req Request) (schema.Schema, error) {
	rows, err := q.Query(ctx, sqliteColumnSQL, req.Table)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("introspect: sqlite columns: %w", err)
	}
	cols := make([]schema.Column, 0, len(rows))
	var pkNamed []string
	for _, row := range rows {
		ord, err := reqInt(row, "ordinal")
		if err != nil {
			return schema.Schema{}, err
		}
		name := text(row, "name")
		if name == "" {
			return schema.Schema{}, fmt.Errorf("introspect: sqlite column without a name")
		}
		pk, err := reqInt(row, "pk")
		if err != nil {
			return schema.Schema{}, err
		}
		col := schema.Column{
			Name:     name,
			SQLType:  text(row, "sql_type"),
			Nullable: !truth(row, "notnull"),
			Default:  text(row, "default_expr"),
			// cid is 0-based; see sqliteColumnSQL.
			Ordinal: ord + 1,
		}
		if pk > 0 {
			col.PrimaryKey = true
			pkNamed = append(pkNamed, name)
		}
		cols = append(cols, col)
	}
	// INTEGER PRIMARY KEY is the rowid and autoincrements whether or not
	// the AUTOINCREMENT keyword was written. A keyword scan also matches a
	// column named autoincrement and a comment that mentions it, so the
	// pragma is the test: declared type exactly INTEGER, and the only
	// primary-key column. INT and INTEGER(n) are not the rowid.
	if len(pkNamed) == 1 {
		if i := colIndex(cols, pkNamed[0]); i >= 0 && isSQLiteRowID(cols[i].SQLType) {
			cols[i].Identity = true
		}
	}
	if err := sqliteUniques(ctx, q, req, cols); err != nil {
		return schema.Schema{}, err
	}
	if err := sqliteFKs(ctx, q, req, cols); err != nil {
		return schema.Schema{}, err
	}
	if err := sqliteChecks(ctx, q, req, cols); err != nil {
		return schema.Schema{}, err
	}
	return finish("sqlite", req, false, cols), nil
}

func isSQLiteRowID(sqlType string) bool {
	return strings.EqualFold(strings.TrimSpace(sqlType), "INTEGER")
}

func sqliteUniques(ctx context.Context, q Querier, req Request, cols []schema.Column) error {
	rows, err := q.Query(ctx, sqliteIndexSQL, req.Table)
	if err != nil {
		return fmt.Errorf("introspect: sqlite indexes: %w", err)
	}
	type idx struct {
		origin string
		cols   []string
	}
	var order []string
	groups := map[string]*idx{}
	for _, row := range rows {
		name := text(row, "index_name")
		g, ok := groups[name]
		if !ok {
			g = &idx{origin: text(row, "origin")}
			groups[name] = g
			order = append(order, name)
		}
		if col := text(row, "column_name"); col != "" {
			g.cols = append(g.cols, col)
		}
	}
	for _, name := range order {
		g := groups[name]
		// origin pk is the primary key's own index. primary_key already
		// came from pragma_table_info; marking it unique as well would
		// double-count the same constraint.
		if g.origin == "pk" || len(g.cols) != 1 {
			continue
		}
		if i := colIndex(cols, g.cols[0]); i >= 0 {
			cols[i].Unique = true
		}
	}
	return nil
}

func sqliteFKs(ctx context.Context, q Querier, req Request, cols []schema.Column) error {
	rows, err := q.Query(ctx, sqliteFKSQL, req.Table)
	if err != nil {
		return fmt.Errorf("introspect: sqlite foreign keys: %w", err)
	}
	for _, row := range rows {
		name := text(row, "column_name")
		i := colIndex(cols, name)
		if i < 0 || cols[i].References != nil {
			continue
		}
		refTable := text(row, "ref_table")
		refCol := text(row, "ref_column")
		if refTable == "" || refCol == "" {
			continue
		}
		cols[i].References = &schema.ForeignKey{Table: refTable, Column: refCol}
	}
	return nil
}

func sqliteChecks(ctx context.Context, q Querier, req Request, cols []schema.Column) error {
	rows, err := q.Query(ctx, sqliteMasterSQL, req.Table)
	if err != nil {
		return fmt.Errorf("introspect: sqlite ddl: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	for _, chk := range parseSQLiteChecks(text(rows[0], "sql")) {
		attachEnum(cols, chk.column, chk.values)
	}
	return nil
}
