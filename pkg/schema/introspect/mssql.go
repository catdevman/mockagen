package introspect

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/catdevman/mockagen/pkg/schema"
)

// SQL Server has no column that holds the type spelling, so it is assembled
// here. max_length is in bytes (nvarchar(255) reports 510) and -1 means MAX;
// both are corrected in the expression rather than after the fact.
const mssqlColumnSQL = `
SELECT c.column_id AS ordinal,
       c.name      AS name,
       ty.name + CASE
         WHEN ty.name IN ('varchar', 'char', 'varbinary', 'binary')
           THEN '(' + IIF(c.max_length = -1, 'max',
                          CAST(c.max_length AS varchar(11))) + ')'
         WHEN ty.name IN ('nvarchar', 'nchar')
           THEN '(' + IIF(c.max_length = -1, 'max',
                          CAST(c.max_length / 2 AS varchar(11))) + ')'
         WHEN ty.name IN ('decimal', 'numeric')
           THEN '(' + CAST(c.precision AS varchar(11)) + ',' +
                      CAST(c.scale AS varchar(11)) + ')'
         WHEN ty.name IN ('datetime2', 'time', 'datetimeoffset')
           THEN '(' + CAST(c.scale AS varchar(11)) + ')'
         ELSE '' END AS sql_type,
       ty.name       AS base_type,
       CASE WHEN ty.name IN ('varchar', 'char', 'varbinary', 'binary')
                 AND c.max_length <> -1 THEN c.max_length
            WHEN ty.name IN ('nvarchar', 'nchar')
                 AND c.max_length <> -1 THEN c.max_length / 2
            ELSE NULL END AS max_length,
       CASE WHEN ty.name IN ('decimal', 'numeric') THEN c.precision
            ELSE NULL END AS numeric_precision,
       CASE WHEN ty.name IN ('decimal', 'numeric') THEN c.scale
            ELSE NULL END AS numeric_scale,
       c.is_nullable,
       c.is_identity,
       dc.definition                   AS default_expr,
       CAST(ep.value AS nvarchar(200)) AS comment
FROM sys.columns c
JOIN sys.objects o ON o.object_id = c.object_id
JOIN sys.schemas s ON s.schema_id = o.schema_id
JOIN sys.types ty  ON ty.user_type_id = c.user_type_id
LEFT JOIN sys.default_constraints dc
       ON dc.parent_object_id = c.object_id AND dc.parent_column_id = c.column_id
LEFT JOIN sys.extended_properties ep
       ON ep.major_id = c.object_id AND ep.minor_id = c.column_id
      AND ep.name = 'MS_Description'
WHERE s.name = @p1 AND o.name = @p2
ORDER BY c.column_id`

// Three catalogs, unioned into the shape postgres returns. identity is a
// reserved word, so the column flag stays is_identity in the column query;
// this one never selects it.
const mssqlConstraintSQL = `
SELECT kc.name                     AS name,
       IIF(kc.type = 'PK', 'p', 'u') AS kind,
       CAST(ic.key_ordinal AS int) AS position,
       col.name                    AS column_name,
       CAST(NULL AS sysname)       AS ref_schema,
       CAST(NULL AS sysname)       AS ref_table,
       CAST(NULL AS sysname)       AS ref_column,
       CAST(NULL AS nvarchar(max)) AS definition
FROM sys.key_constraints kc
JOIN sys.objects o        ON o.object_id = kc.parent_object_id
JOIN sys.schemas s        ON s.schema_id = o.schema_id
JOIN sys.index_columns ic ON ic.object_id = kc.parent_object_id
                         AND ic.index_id = kc.unique_index_id
JOIN sys.columns col      ON col.object_id = ic.object_id
                         AND col.column_id = ic.column_id
WHERE s.name = @p1 AND o.name = @p2
UNION ALL
SELECT fk.name, 'f', CAST(fkc.constraint_column_id AS int), pcol.name,
       rs.name, rt.name, rcol.name, CAST(NULL AS nvarchar(max))
FROM sys.foreign_keys fk
JOIN sys.objects o    ON o.object_id = fk.parent_object_id
JOIN sys.schemas s    ON s.schema_id = o.schema_id
JOIN sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
JOIN sys.columns pcol ON pcol.object_id = fkc.parent_object_id
                     AND pcol.column_id = fkc.parent_column_id
JOIN sys.objects rt   ON rt.object_id = fk.referenced_object_id
JOIN sys.schemas rs   ON rs.schema_id = rt.schema_id
JOIN sys.columns rcol ON rcol.object_id = fkc.referenced_object_id
                     AND rcol.column_id = fkc.referenced_column_id
WHERE s.name = @p1 AND o.name = @p2
UNION ALL
SELECT cc.name, 'c', 1, col.name,
       NULL, NULL, NULL, CAST(cc.definition AS nvarchar(max))
FROM sys.check_constraints cc
JOIN sys.objects o ON o.object_id = cc.parent_object_id
JOIN sys.schemas s ON s.schema_id = o.schema_id
LEFT JOIN sys.columns col ON col.object_id = cc.parent_object_id
                         AND col.column_id = cc.parent_column_id
WHERE s.name = @p1 AND o.name = @p2
ORDER BY kind, name, position`

// index_id 0 and 1 are the heap and the clustered index. Summing every
// partition would count each nonclustered index as another copy of the rows.
const mssqlTableSQL = `
SELECT s.name AS [schema], t.name AS name, SUM(p.rows) AS approx_rows
FROM sys.tables t
JOIN sys.schemas s    ON s.schema_id = t.schema_id
JOIN sys.partitions p ON p.object_id = t.object_id AND p.index_id IN (0, 1)
GROUP BY s.name, t.name
ORDER BY s.name, t.name`

func inspectMSSQL(ctx context.Context, q Querier, req Request) (schema.Schema, error) {
	args := []any{sql.Named("p1", req.Schema), sql.Named("p2", req.Table)}
	rows, err := q.Query(ctx, mssqlColumnSQL, args...)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("introspect: mssql columns: %w", err)
	}
	cols := make([]schema.Column, 0, len(rows))
	for _, row := range rows {
		ord, err := reqInt(row, "ordinal")
		if err != nil {
			return schema.Schema{}, err
		}
		name := text(row, "name")
		if name == "" {
			return schema.Schema{}, fmt.Errorf("introspect: mssql column without a name")
		}
		maxLen, err := optInt(row, "max_length")
		if err != nil {
			return schema.Schema{}, err
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
			MaxLength:        dropSentinelLength(maxLen),
			NumericPrecision: prec,
			NumericScale:     scale,
			Nullable:         truth(row, "is_nullable"),
			Comment:          text(row, "comment"),
			Ordinal:          ord,
			Identity:         truth(row, "is_identity"),
		}
		// Defaults come back parenthesised, and numerics doubly so:
		// ('active'), ((0.00)), (newid()). The outer layers are wrapping,
		// not part of the expression.
		if d := text(row, "default_expr"); d != "" {
			col.Default = stripParenLayers(d)
		}
		cols = append(cols, col)
	}
	cons, err := readConstraints(ctx, q, mssqlConstraintSQL, args...)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("introspect: mssql constraints: %w", err)
	}
	applyConstraints(cols, cons, req.Schema, parseMSSQLCheck)
	return finish("mssql", req, true, cols), nil
}
