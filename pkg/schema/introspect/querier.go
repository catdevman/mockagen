package introspect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Querier runs one catalog query and returns every row. One method, rather
// than a row iterator, so a test can hand back a literal and so this
// package never imports a driver: database/sql and the sqlite3 CLI both
// adapt to it from outside.
//
// A missing key is NULL. Values are text because the drivers do not agree
// on a Go type for the same SQL value (bool versus int versus []byte) and
// the catalog facts are all scalars.
type Querier interface {
	Query(ctx context.Context, query string, args ...any) ([]Row, error)
}

// Row is one catalog row keyed by the query's column alias.
type Row map[string]string

// SQLQuerier adapts database/sql. The driver is registered by the caller;
// importing one here would put pgx and go-mssqldb on every program that
// only wanted the schema types.
type SQLQuerier struct {
	DB *sql.DB
}

// Query executes query and stringifies each cell. NULL stays a missing key
// so a later reader cannot confuse it with an empty default.
func (s SQLQuerier) Query(ctx context.Context, query string, args ...any) ([]Row, error) {
	if s.DB == nil {
		return nil, errors.New("introspect: nil database")
	}
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		dest := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range dest {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := Row{}
		for i, name := range names {
			text, ok := stringify(dest[i])
			if ok {
				row[name] = text
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SQLiteCLI queries through the sqlite3 binary. There is no Go driver on
// purpose: modernc.org/sqlite is a transpiled library plus a libc, and that
// alone is comparable to the whole mockagen binary. -json is required; a
// shell that only prints aligned columns would need a second parser.
type SQLiteCLI struct {
	DB  string
	Bin string
}

// Query substitutes ? itself. The sqlite3 shell has no positional
// parameters, and pragma_table_info takes its table name as a literal in
// the statement the design verified.
func (s SQLiteCLI) Query(ctx context.Context, query string, args ...any) ([]Row, error) {
	bound, err := bindLiterals(query, args)
	if err != nil {
		return nil, err
	}
	bin := s.Bin
	if bin == "" {
		bin = "sqlite3"
	}
	cmd := exec.CommandContext(ctx, bin, "-json", "-batch", s.DB, bound)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("introspect: sqlite3: %w\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	var raw []map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("introspect: sqlite json: %w", err)
	}
	rows := make([]Row, len(raw))
	for i, src := range raw {
		row := Row{}
		for k, v := range src {
			text, ok := stringify(v)
			if ok {
				row[k] = text
			}
		}
		rows[i] = row
	}
	return rows, nil
}

// bindLiterals replaces ? placeholders that are not inside a string. The
// arguments are catalog names, quoted as SQL string literals.
func bindLiterals(query string, args []any) (string, error) {
	var b strings.Builder
	n := 0
	in := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if in {
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				in = false
			}
			continue
		}
		if c == '\'' {
			in = true
			b.WriteByte(c)
			continue
		}
		if c == '?' {
			if n >= len(args) {
				return "", errors.New("introspect: sqlite: not enough arguments")
			}
			lit, err := sqlLiteral(args[n])
			if err != nil {
				return "", err
			}
			b.WriteString(lit)
			n++
			continue
		}
		b.WriteByte(c)
	}
	if n != len(args) {
		return "", errors.New("introspect: sqlite: unused arguments")
	}
	return b.String(), nil
}

// sqlLiteral quotes a catalog name for the sqlite3 shell. nil is the
// keyword NULL, not the word "nil": a missing argument must not become
// a table of that name.
func sqlLiteral(v any) (string, error) {
	if v == nil {
		return "NULL", nil
	}
	s, ok := stringify(v)
	if !ok {
		return "NULL", nil
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

// stringify collapses the Go types the drivers return for one SQL
// value. A missing case shows up as a fact that differs by driver, and
// the golden YAML would then pin the driver instead of the server.
func stringify(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case int:
		return strconv.Itoa(x), true
	case int8:
		return strconv.FormatInt(int64(x), 10), true
	case int16:
		return strconv.FormatInt(int64(x), 10), true
	case int32:
		return strconv.FormatInt(int64(x), 10), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint:
		return strconv.FormatUint(uint64(x), 10), true
	case uint8:
		return strconv.FormatUint(uint64(x), 10), true
	case uint16:
		return strconv.FormatUint(uint64(x), 10), true
	case uint32:
		return strconv.FormatUint(uint64(x), 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case float32:
		return formatFloat(float64(x)), true
	case float64:
		return formatFloat(x), true
	case json.Number:
		return x.String(), true
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano), true
	case fmt.Stringer:
		return x.String(), true
	default:
		return fmt.Sprint(x), true
	}
}

// formatFloat prints a whole number as an integer. Catalog widths come
// back as float64, and "64.0" is not a width. Past 1e15 a float64 is no
// longer an integer, so those stay in decimal form.
func formatFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	if f == math.Trunc(f) && f <= 1e15 && f >= -1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
