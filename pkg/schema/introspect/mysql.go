package introspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/catdevman/mockagen/pkg/schema"
)

// COLUMN_TYPE, not DATA_TYPE. DATA_TYPE strips the length and the enum
// labels, and sql_type is the spelling the server itself stores.
const mysqlColumnSQL = `
SELECT ORDINAL_POSITION         AS ordinal,
       COLUMN_NAME              AS name,
       COLUMN_TYPE              AS sql_type,
       DATA_TYPE                AS base_type,
       IS_NULLABLE = 'YES'      AS nullable,
       COLUMN_DEFAULT           AS default_expr,
       EXTRA                    AS extra,
       CHARACTER_MAXIMUM_LENGTH AS max_length,
       NUMERIC_PRECISION        AS numeric_precision,
       NUMERIC_SCALE            AS numeric_scale,
       COLUMN_COMMENT           AS comment
FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
ORDER BY ORDINAL_POSITION`

// CHECK rows do not name their column in KEY_COLUMN_USAGE. The column
// has to be read out of CHECK_CLAUSE, which is why the parser exists.
const mysqlConstraintSQL = `
SELECT tc.CONSTRAINT_NAME          AS name,
       tc.CONSTRAINT_TYPE          AS kind,
       kcu.ORDINAL_POSITION        AS position,
       kcu.COLUMN_NAME             AS column_name,
       kcu.REFERENCED_TABLE_SCHEMA AS ref_schema,
       kcu.REFERENCED_TABLE_NAME   AS ref_table,
       kcu.REFERENCED_COLUMN_NAME  AS ref_column,
       cc.CHECK_CLAUSE             AS definition
FROM information_schema.TABLE_CONSTRAINTS tc
LEFT JOIN information_schema.KEY_COLUMN_USAGE kcu
       ON kcu.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
      AND kcu.CONSTRAINT_NAME   = tc.CONSTRAINT_NAME
      AND kcu.TABLE_NAME        = tc.TABLE_NAME
LEFT JOIN information_schema.CHECK_CONSTRAINTS cc
       ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
      AND cc.CONSTRAINT_NAME   = tc.CONSTRAINT_NAME
WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ?
ORDER BY tc.CONSTRAINT_TYPE, tc.CONSTRAINT_NAME, kcu.ORDINAL_POSITION`

// schema is reserved, so the alias is quoted. A raw string is delimited
// by backticks and cannot contain one, hence the concatenation.
const mysqlTableSQL = `
SELECT TABLE_SCHEMA AS ` + "`schema`" + `, TABLE_NAME AS name, TABLE_ROWS AS approx_rows
FROM information_schema.TABLES
WHERE TABLE_TYPE = 'BASE TABLE'
  AND TABLE_SCHEMA NOT IN ('mysql', 'information_schema',
                           'performance_schema', 'sys')
ORDER BY TABLE_SCHEMA, TABLE_NAME`

func inspectMySQL(ctx context.Context, q Querier, req Request) (schema.Schema, error) {
	rows, err := q.Query(ctx, mysqlColumnSQL, req.Schema, req.Table)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("introspect: mysql columns: %w", err)
	}
	cols := make([]schema.Column, 0, len(rows))
	for _, row := range rows {
		ord, err := reqInt(row, "ordinal")
		if err != nil {
			return schema.Schema{}, err
		}
		name := text(row, "name")
		if name == "" {
			return schema.Schema{}, fmt.Errorf("introspect: mysql column without a name")
		}
		base := text(row, "base_type")
		var maxLen *int64
		// CHARACTER_MAXIMUM_LENGTH on an enum is the longest label, not a
		// declared width. Carrying it would cap generated values at that
		// length and still not describe the type.
		if !strings.EqualFold(base, "enum") {
			maxLen, err = optInt(row, "max_length")
			if err != nil {
				return schema.Schema{}, err
			}
			maxLen = dropSentinelLength(maxLen)
		}
		prec, err := optInt(row, "numeric_precision")
		if err != nil {
			return schema.Schema{}, err
		}
		scale, err := optInt(row, "numeric_scale")
		if err != nil {
			return schema.Schema{}, err
		}
		col := schema.Column{
			Name:             name,
			SQLType:          text(row, "sql_type"),
			MaxLength:        maxLen,
			NumericPrecision: prec,
			NumericScale:     scale,
			Nullable:         truth(row, "nullable"),
			Default:          text(row, "default_expr"),
			Comment:          text(row, "comment"),
			Ordinal:          ord,
		}
		if strings.Contains(strings.ToLower(text(row, "extra")), "auto_increment") {
			col.Identity = true
		}
		if strings.EqualFold(base, "enum") {
			if values, ok := parseMySQLEnum(col.SQLType); ok {
				col.EnumValues = values
			}
		}
		cols = append(cols, col)
	}
	cons, err := readConstraints(ctx, q, mysqlConstraintSQL, req.Schema, req.Table)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("introspect: mysql constraints: %w", err)
	}
	// MySQL's table name is the database, which is already Source.Database,
	// so the table is not qualified a second time.
	applyConstraints(cols, cons, req.Schema, parseMySQLCheck)
	return finish("mysql", req, false, cols), nil
}
