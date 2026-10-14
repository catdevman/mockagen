// Package sqlgen provides database-specific SQL dialect definitions,
// identifier quoting, string and value literal formatting, parameter
// placeholders, and bulk-load script generation for mockagen.
package sqlgen

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// MaxRowsPerStatement caps the number of rows batched into a single multi-row
// INSERT statement.
//
// 1000 is not a round number chosen for comfort: it is SQL Server's hard
// limit on the rows a table value constructor may carry, so raising this
// breaks mssql outright no matter how much headroom the other three have.
// It also sits well beneath SQLite's variable limit, MySQL's
// max_allowed_packet and PostgreSQL's 1GB message ceiling.
const MaxRowsPerStatement = 1000

// DefaultDialect is what an empty dialect field resolves to. Postgres'
// double-quoted identifier syntax and standard string quoting make it the most
// portable default across SQL tools.
const DefaultDialect = "postgres"

// PlaceholderStyle specifies the syntax used by a database dialect for bound
// query parameters ($1, ?, or @p1).
type PlaceholderStyle string

const (
	PlaceholderDollar   PlaceholderStyle = "$1"
	PlaceholderQuestion PlaceholderStyle = "?"
	PlaceholderAtP      PlaceholderStyle = "@p1"
)

// Raw represents a pre-formatted SQL fragment that is appended verbatim by
// AppendValue rather than escaped as a literal.
type Raw string

// ErrEmbeddedNUL is returned by AppendValue when a string or byte slice
// contains a NUL byte (\x00). The importer rejects NULs rather than silently
// dropping them (as the generator does) to prevent undetectable data loss.
var ErrEmbeddedNUL = errors.New("sqlgen: embedded NUL byte in literal")

// Dialect encodes the syntactic divergences among the supported SQL database
// engines: identifier quoting, string escaping, parameter placeholders,
// parameter limits, and bulk-load scripts.
type Dialect struct {
	Name string
	// IdentOpen and IdentClose bracket a quoted identifier. All four
	// dialects escape a literal IdentClose inside the identifier by doubling
	// it. Identifiers are always quoted to avoid collision with keywords.
	IdentOpen  byte
	IdentClose byte
	// EscapeBackslash indicates that backslash is an escape character inside
	// a string literal (MySQL with default NO_BACKSLASH_ESCAPES off).
	EscapeBackslash bool
	// UnicodePrefix is written before a literal containing non-ASCII text.
	// SQL Server requires N'...' for nvarchar literals to prevent silent
	// replacement of characters with "?" during ingestion.
	UnicodePrefix string
	// PlaceholderStyle describes how parameterized queries format placeholders.
	PlaceholderStyle PlaceholderStyle
	// MaxBoundParams is the maximum number of query parameters supported by
	// a prepared statement in this dialect (e.g. 65535 postgres, 2100 mssql).
	MaxBoundParams int
	// TrueLiteral and FalseLiteral spell a boolean value. SQL Server has no
	// boolean type and no TRUE/FALSE keywords - a BIT column takes 1 and 0,
	// and an INSERT carrying the bare word TRUE is rejected by the server
	// rather than coerced. The other three accept TRUE and FALSE.
	TrueLiteral  string
	FalseLiteral string
}

var dialects = map[string]Dialect{
	"postgres": {
		Name:             "postgres",
		IdentOpen:        '"',
		IdentClose:       '"',
		PlaceholderStyle: PlaceholderDollar,
		MaxBoundParams:   65535,
		TrueLiteral:      "TRUE",
		FalseLiteral:     "FALSE",
	},
	"sqlite": {
		Name:             "sqlite",
		IdentOpen:        '"',
		IdentClose:       '"',
		PlaceholderStyle: PlaceholderQuestion,
		MaxBoundParams:   32766,
		TrueLiteral:      "TRUE",
		FalseLiteral:     "FALSE",
	},
	"mysql": {
		Name:             "mysql",
		IdentOpen:        '`',
		IdentClose:       '`',
		EscapeBackslash:  true,
		PlaceholderStyle: PlaceholderQuestion,
		MaxBoundParams:   65535,
		TrueLiteral:      "TRUE",
		FalseLiteral:     "FALSE",
	},
	"mssql": {
		Name:             "mssql",
		IdentOpen:        '[',
		IdentClose:       ']',
		UnicodePrefix:    "N",
		PlaceholderStyle: PlaceholderAtP,
		MaxBoundParams:   2100,
		TrueLiteral:      "1",
		FalseLiteral:     "0",
	},
}

// aliases maps alternate database names onto canonical dialect keys.
var aliases = map[string]string{
	"postgresql": "postgres",
	"pgsql":      "postgres",
	"pg":         "postgres",
	"sqlite3":    "sqlite",
	"mariadb":    "mysql",
	"sqlserver":  "mssql",
	"tsql":       "mssql",
}

// Lookup resolves a dialect name or alias. An unrecognised name produces an
// error rather than a silent fallback to prevent generating invalid SQL that
// only fails downstream when executed against the database.
func Lookup(name string) (Dialect, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = DefaultDialect
	}
	if canonical, ok := aliases[key]; ok {
		key = canonical
	}
	d, ok := dialects[key]
	if !ok {
		valid := slices.Sorted(maps.Keys(dialects))
		return Dialect{}, fmt.Errorf("unsupported sql dialect %q: want one of %s",
			name, strings.Join(valid, ", "))
	}
	return d, nil
}

// AppendIdent appends ident as a quoted identifier to dst.
func (d Dialect) AppendIdent(dst []byte, ident string) []byte {
	dst = append(dst, d.IdentOpen)
	for i := 0; i < len(ident); i++ {
		c := ident[i]
		if c == d.IdentClose {
			dst = append(dst, c)
		}
		dst = append(dst, c)
	}
	return append(dst, d.IdentClose)
}

// AppendQualifiedIdent appends a possibly schema-qualified table name,
// quoting each dotted component separately so that "dbo.users" refers to table
// "users" in schema "dbo" rather than a single table named "dbo.users".
func (d Dialect) AppendQualifiedIdent(dst []byte, name string) []byte {
	for i, part := range strings.Split(name, ".") {
		if i > 0 {
			dst = append(dst, '.')
		}
		dst = d.AppendIdent(dst, part)
	}
	return dst
}

// StringLike constraints types accepted by AppendString to string and []byte.
type StringLike interface {
	~string | ~[]byte
}

func isASCII[T StringLike](s T) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// AppendString appends s as a quoted string literal in dialect d. Doubling
// quotes handles single-quote escaping across all dialects. Backslashes and
// control characters (\r, \n) are escaped only when EscapeBackslash is enabled
// (MySQL), ensuring raw CR characters are not swallowed by line-based ingestion.
// Embedded NUL bytes are dropped to preserve file loadability across engines.
func AppendString[T StringLike](dst []byte, d Dialect, s T) []byte {
	if d.UnicodePrefix != "" && !isASCII(s) {
		dst = append(dst, d.UnicodePrefix...)
	}
	dst = append(dst, '\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			dst = append(dst, '\'', '\'')
		case '\\':
			if d.EscapeBackslash {
				dst = append(dst, '\\')
			}
			dst = append(dst, '\\')
		case '\r', '\n':
			if !d.EscapeBackslash {
				dst = append(dst, c)
				break
			}
			if c == '\r' {
				dst = append(dst, '\\', 'r')
			} else {
				dst = append(dst, '\\', 'n')
			}
		case 0:
			// Dropped by design for the generator to stay loadable across all
			// four engines. Importers that must catch NUL use AppendValue.
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, '\'')
}

// AppendString appends s as a quoted string literal in dialect d.
func (d Dialect) AppendString(dst []byte, s string) []byte {
	return AppendString(dst, d, s)
}

// AppendBytes appends b as a quoted string literal in dialect d.
func (d Dialect) AppendBytes(dst []byte, b []byte) []byte {
	return AppendString(dst, d, b)
}

// Placeholder returns the 1-based n-th parameter placeholder for dialect d.
func (d Dialect) Placeholder(n int) string {
	switch d.PlaceholderStyle {
	case PlaceholderDollar:
		return fmt.Sprintf("$%d", n)
	case PlaceholderAtP:
		return fmt.Sprintf("@p%d", n)
	default:
		return "?"
	}
}

// AppendPlaceholder appends the 1-based n-th parameter placeholder to dst.
func (d Dialect) AppendPlaceholder(dst []byte, n int) []byte {
	switch d.PlaceholderStyle {
	case PlaceholderDollar:
		dst = append(dst, '$')
		return strconv.AppendInt(dst, int64(n), 10)
	case PlaceholderAtP:
		dst = append(dst, "@p"...)
		return strconv.AppendInt(dst, int64(n), 10)
	default:
		return append(dst, '?')
	}
}

// AppendValue formats val as a SQL literal in dialect d. Bare NULL is returned
// for nil, numbers are formatted without quotes, Raw expressions are emitted
// verbatim, and strings are escaped. Unlike AppendString, an embedded NUL byte
// returns ErrEmbeddedNUL to prevent silent data corruption.
// On error dst is returned unchanged rather than as a nil slice, so that a
// caller writing `dst, err = d.AppendValue(dst, v)` still holds the statement
// it had built - otherwise the error destroys the context needed to report it.
func (d Dialect) AppendValue(dst []byte, val any) ([]byte, error) {
	if val == nil {
		return append(dst, "NULL"...), nil
	}

	switch v := val.(type) {
	case Raw:
		return append(dst, string(v)...), nil
	case *Raw:
		if v == nil {
			return append(dst, "NULL"...), nil
		}
		return append(dst, string(*v)...), nil
	case int:
		return strconv.AppendInt(dst, int64(v), 10), nil
	case int8:
		return strconv.AppendInt(dst, int64(v), 10), nil
	case int16:
		return strconv.AppendInt(dst, int64(v), 10), nil
	case int32:
		return strconv.AppendInt(dst, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(dst, v, 10), nil
	case uint:
		return strconv.AppendUint(dst, uint64(v), 10), nil
	case uint8:
		return strconv.AppendUint(dst, uint64(v), 10), nil
	case uint16:
		return strconv.AppendUint(dst, uint64(v), 10), nil
	case uint32:
		return strconv.AppendUint(dst, uint64(v), 10), nil
	case uint64:
		return strconv.AppendUint(dst, v, 10), nil
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return dst, fmt.Errorf("sqlgen: unsupported float value: %v", v)
		}
		return strconv.AppendFloat(dst, float64(v), 'g', -1, 32), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return dst, fmt.Errorf("sqlgen: unsupported float value: %v", v)
		}
		return strconv.AppendFloat(dst, v, 'g', -1, 64), nil
	case bool:
		if v {
			return append(dst, d.TrueLiteral...), nil
		}
		return append(dst, d.FalseLiteral...), nil
	case string:
		if strings.IndexByte(v, 0) >= 0 {
			return dst, ErrEmbeddedNUL
		}
		return AppendString(dst, d, v), nil
	case []byte:
		if bytes.IndexByte(v, 0) >= 0 {
			return dst, ErrEmbeddedNUL
		}
		return AppendString(dst, d, v), nil
	default:
		return dst, fmt.Errorf("sqlgen: unsupported value type %T", val)
	}
}

// IdentList renders cols as a quoted, comma-separated column list.
func (d Dialect) IdentList(cols []string) string {
	var b []byte
	for i, col := range cols {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = d.AppendIdent(b, col)
	}
	return string(b)
}

// Table picks the target SQL table name: table when set, otherwise derived
// from name with words joined by underscores and lowercased. When both are
// blank, it falls back to "mockagen".
func Table(name, table string) string {
	if t := strings.TrimSpace(table); t != "" {
		return t
	}
	n := strings.ToLower(strings.Join(strings.Fields(name), "_"))
	if n == "" {
		return "mockagen"
	}
	return n
}

// bulkScriptPreamble opens every dialect's script: what it loads, and notes
// that the script and CSV are a movable pair intended to be executed on the
// target host.
func bulkScriptPreamble(b *strings.Builder, quotedTable, csvName string) {
	fmt.Fprintf(b, "-- Bulk load for %s, generated by mockagen.\n", quotedTable)
	fmt.Fprintf(b, "-- This script and %s are a pair: keep them in the same directory - zip\n", csvName)
	b.WriteString("-- them together to move them to the database host - and run this from there.\n--\n")
}

// BulkLoadScript renders the dialect's bulk-load command for csvName, which
// is a bare file name resolved relative to wherever the script is run.
func BulkLoadScript(d Dialect, table string, cols []string, csvName string, includeHeader bool) []byte {
	var b strings.Builder
	quotedTable := string(d.AppendQualifiedIdent(nil, table))
	colList := d.IdentList(cols)

	switch d.Name {
	case "postgres":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- Run with:\n--   psql -d <database> -f <this file>\n--\n")
		b.WriteString("-- \\copy is a psql client-side meta-command: it reads the file on the\n")
		b.WriteString("-- machine running psql, relative to that process's working directory, and\n")
		b.WriteString("-- has to stay on a single line. The server-side COPY is faster but needs an\n")
		b.WriteString("-- absolute path the database server itself can see, plus pg_read_server_files.\n--\n")
		b.WriteString("-- To load empty fields as NULL rather than empty strings, add NULL '' to the\n")
		b.WriteString("-- WITH list below. It applies to every column, so it is left off here.\n")
		header := ""
		if includeHeader {
			header = ", HEADER"
		}
		fmt.Fprintf(&b, "\\copy %s (%s) FROM '%s' WITH (FORMAT csv%s)\n", quotedTable, colList, csvName, header)

	case "mysql":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- Run with:\n--   mysql --local-infile=1 -D <database> < <this file>\n--\n")
		b.WriteString("-- LOCAL reads the file on the client, relative to its working directory, and\n")
		b.WriteString("-- needs local_infile enabled on both client and server. Dropping LOCAL makes\n")
		b.WriteString("-- the server read the path instead, which secure_file_priv usually restricts.\n--\n")
		b.WriteString("-- To load empty fields as NULL, read the columns into variables and SET each\n")
		b.WriteString("-- one to NULLIF(@col, '') in place of the plain column list below.\n")
		fmt.Fprintf(&b, "LOAD DATA LOCAL INFILE '%s' INTO TABLE %s\n", csvName, quotedTable)
		b.WriteString("\tFIELDS TERMINATED BY ',' ENCLOSED BY '\"' ESCAPED BY ''\n")
		b.WriteString("\tLINES TERMINATED BY '\\n'\n")
		if includeHeader {
			b.WriteString("\tIGNORE 1 LINES\n")
		}
		fmt.Fprintf(&b, "\t(%s);\n", colList)

	case "mssql":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- Run with:\n--   sqlcmd -d <database> -i <this file>\n--\n")
		fmt.Fprintf(&b, "-- WARNING: BULK INSERT is executed by the SQL Server process, so '%s' is\n", csvName)
		b.WriteString("-- resolved on the SERVER - and relative to that process's working directory,\n")
		b.WriteString("-- not the one you run sqlcmd from. Having unpacked this pair on the server,\n")
		b.WriteString("-- substitute the absolute path of the directory holding them. To load from\n")
		b.WriteString("-- the client instead, bcp does resolve a relative name against your own\n")
		b.WriteString("-- working directory:\n")
		fmt.Fprintf(&b, "--   bcp %s in %s -c -t, -S <server> -d <database> -T\n--\n", quotedTable, csvName)
		b.WriteString("-- BULK INSERT takes no column list: it maps CSV fields to table columns by\n")
		b.WriteString("-- POSITION, so the table's column order must match the CSV's, which is:\n")
		fmt.Fprintf(&b, "--   %s\n--\n", strings.Join(cols, ", "))
		b.WriteString("-- FORMAT = 'CSV' needs SQL Server 2017 or later.\n--\n")
		b.WriteString("-- NON-ASCII DATA: the CSV is UTF-8, and BULK INSERT otherwise reads it in\n")
		b.WriteString("-- the server's default codepage, silently storing mojibake. On Windows,\n")
		b.WriteString("-- add CODEPAGE = '65001' to the WITH list to fix that. On SQL Server for\n")
		b.WriteString("-- Linux that option is rejected outright (\"Keyword or statement option\n")
		b.WriteString("-- 'CODEPAGE' is not supported on the 'Linux' platform\"), and neither a\n")
		b.WriteString("-- UTF-8 collation on the target column nor DATAFILETYPE helps - the\n")
		b.WriteString("-- latter cannot be combined with FORMAT = 'CSV' at all. If your data has\n")
		b.WriteString("-- non-ASCII values and your server runs on Linux, use mode: insert\n")
		b.WriteString("-- instead: its N'' literals carry unicode correctly.\n--\n")
		b.WriteString("-- One quirk to know: BULK INSERT loads an empty field as NULL rather than\n")
		b.WriteString("-- as an empty string when the target column is nullable, even though the\n")
		b.WriteString("-- CSV quotes it. Declare the column NOT NULL if you need '' preserved.\n")
		fmt.Fprintf(&b, "BULK INSERT %s\n\tFROM '%s'\n", quotedTable, csvName)
		firstRow := 1
		if includeHeader {
			firstRow = 2
		}
		fmt.Fprintf(&b, "\tWITH (FORMAT = 'CSV', FIRSTROW = %d, FIELDTERMINATOR = ',', ROWTERMINATOR = '0x0a', FIELDQUOTE = '\"');\n", firstRow)

	case "sqlite":
		bulkScriptPreamble(&b, quotedTable, csvName)
		b.WriteString("-- These are sqlite3 CLI dot-commands, not SQL. Run with:\n")
		b.WriteString("--   sqlite3 <database file> < <this file>\n--\n")
		b.WriteString("-- .import appends to the table when it exists, and creates it from the CSV\n")
		b.WriteString("-- otherwise - every column typed TEXT. --skip needs sqlite3 3.32 or later.\n")
		b.WriteString(".mode csv\n")
		skip := ""
		if includeHeader {
			skip = "--skip 1 "
		}
		fmt.Fprintf(&b, ".import %s%s %s\n", skip, csvName, table)
	}
	return []byte(b.String())
}
