package introspect

import (
	"context"
	"fmt"
	"slices"

	"github.com/catdevman/mockagen/pkg/schema"
)

// pg_catalog rather than information_schema: the latter reports the type
// as "character varying" with the length stripped, and format_type is what
// puts the server's own spelling in sql_type.
const postgresColumnSQL = `
SELECT a.attnum                             AS ordinal,
       a.attname                            AS name,
       format_type(a.atttypid, a.atttypmod) AS sql_type,
       t.typname                            AS base_type,
       t.typtype = 'e'                      AS is_enum,
       NOT a.attnotnull                     AS nullable,
       pg_get_expr(ad.adbin, ad.adrelid)    AS default_expr,
       a.attidentity                        AS identity,
       information_schema._pg_char_max_length(a.atttypid, a.atttypmod)
                                            AS max_length,
       information_schema._pg_numeric_precision(a.atttypid, a.atttypmod)
                                            AS numeric_precision,
       information_schema._pg_numeric_scale(a.atttypid, a.atttypmod)
                                            AS numeric_scale,
       col_description(a.attrelid, a.attnum) AS comment
FROM pg_attribute a
JOIN pg_class cl     ON cl.oid = a.attrelid
JOIN pg_namespace ns ON ns.oid = cl.relnamespace
JOIN pg_type t       ON t.oid = a.atttypid
LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
WHERE ns.nspname = $1 AND cl.relname = $2
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

const postgresConstraintSQL = `
SELECT con.conname                   AS name,
       con.contype                   AS kind,
       k.ord                         AS position,
       a.attname                     AS column_name,
       fns.nspname                   AS ref_schema,
       fcl.relname                   AS ref_table,
       fa.attname                    AS ref_column,
       pg_get_constraintdef(con.oid) AS definition
FROM pg_constraint con
JOIN pg_class cl     ON cl.oid = con.conrelid
JOIN pg_namespace ns ON ns.oid = cl.relnamespace
LEFT JOIN unnest(con.conkey)  WITH ORDINALITY AS k(attnum, ord)  ON true
LEFT JOIN pg_attribute a   ON a.attrelid = con.conrelid AND a.attnum = k.attnum
LEFT JOIN unnest(con.confkey) WITH ORDINALITY AS fk(attnum, ord) ON fk.ord = k.ord
LEFT JOIN pg_class fcl     ON fcl.oid = con.confrelid
LEFT JOIN pg_namespace fns ON fns.oid = fcl.relnamespace
LEFT JOIN pg_attribute fa  ON fa.attrelid = con.confrelid AND fa.attnum = fk.attnum
WHERE ns.nspname = $1 AND cl.relname = $2
ORDER BY con.contype, con.conname, k.ord`

// One type name per call. The verified query is `typname = ANY ($2)`, which
// needs a postgres array argument, and a []string is a pgx type. This
// package does not import a driver, so each enum type is its own round
// trip with the same joins and the same enumsortorder.
const postgresEnumSQL = `
SELECT e.enumlabel AS value
FROM pg_type t
JOIN pg_enum e      ON e.enumtypid = t.oid
JOIN pg_namespace n ON n.oid = t.typnamespace
WHERE n.nspname = $1 AND t.typname = $2
ORDER BY e.enumsortorder`

const postgresTableSQL = `
SELECT ns.nspname           AS schema,
       cl.relname           AS name,
       cl.reltuples::bigint AS approx_rows
FROM pg_class cl
JOIN pg_namespace ns ON ns.oid = cl.relnamespace
WHERE cl.relkind IN ('r', 'p')
  AND ns.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY ns.nspname, cl.relname`

func inspectPostgres(ctx context.Context, q Querier, req Request) (schema.Schema, error) {
	cols, enumTypes, err := postgresColumns(ctx, q, req)
	if err != nil {
		return schema.Schema{}, err
	}
	if err := postgresEnums(ctx, q, req, cols, enumTypes); err != nil {
		return schema.Schema{}, err
	}
	cons, err := readConstraints(ctx, q, postgresConstraintSQL, req.Schema, req.Table)
	if err != nil {
		return schema.Schema{}, fmt.Errorf("introspect: postgres constraints: %w", err)
	}
	applyConstraints(cols, cons, req.Schema, parsePostgresCheck)
	return finish("postgres", req, true, cols), nil
}

func postgresColumns(ctx context.Context, q Querier, req Request) ([]schema.Column, []string, error) {
	rows, err := q.Query(ctx, postgresColumnSQL, req.Schema, req.Table)
	if err != nil {
		return nil, nil, fmt.Errorf("introspect: postgres columns: %w", err)
	}
	cols := make([]schema.Column, 0, len(rows))
	enumTypes := make([]string, 0, len(rows))
	for _, row := range rows {
		ord, err := reqInt(row, "ordinal")
		if err != nil {
			return nil, nil, err
		}
		name := text(row, "name")
		if name == "" {
			return nil, nil, fmt.Errorf("introspect: postgres column without a name")
		}
		maxLen, err := optInt(row, "max_length")
		if err != nil {
			return nil, nil, err
		}
		prec, err := optInt(row, "numeric_precision")
		if err != nil {
			return nil, nil, err
		}
		scale, err := optInt(row, "numeric_scale")
		if err != nil {
			return nil, nil, err
		}
		col := schema.Column{
			Name:             name,
			SQLType:          text(row, "sql_type"),
			MaxLength:        dropSentinelLength(maxLen),
			NumericPrecision: prec,
			NumericScale:     scale,
			Nullable:         truth(row, "nullable"),
			Default:          text(row, "default_expr"),
			Comment:          text(row, "comment"),
			Ordinal:          ord,
		}
		// attidentity is blank, 'a' (ALWAYS) or 'd' (BY DEFAULT). The schema
		// fact is a bool, so both set it. BY DEFAULT would accept an
		// explicit value, and there is nowhere to record that; the importer
		// treats identity as "leave it out of the INSERT".
		if text(row, "identity") != "" {
			col.Identity = true
		}
		enumType := ""
		if truth(row, "is_enum") {
			enumType = text(row, "base_type")
		}
		cols = append(cols, col)
		enumTypes = append(enumTypes, enumType)
	}
	return cols, enumTypes, nil
}

// One round trip per distinct type name. The query takes a single
// typname because a []string would be a pgx array, and two columns of
// the same enum should not pay for that trip twice.
func postgresEnums(ctx context.Context, q Querier, req Request, cols []schema.Column, types []string) error {
	cache := map[string][]string{}
	for i, typ := range types {
		if typ == "" {
			continue
		}
		labels, ok := cache[typ]
		if !ok {
			rows, err := q.Query(ctx, postgresEnumSQL, req.Schema, typ)
			if err != nil {
				return fmt.Errorf("introspect: postgres enums: %w", err)
			}
			for _, row := range rows {
				labels = append(labels, text(row, "value"))
			}
			cache[typ] = labels
		}
		if len(labels) > 0 {
			cols[i].EnumValues = slices.Clone(labels)
		}
	}
	return nil
}
