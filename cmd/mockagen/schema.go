package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/sqlgen"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/catdevman/mockagen/pkg/schema/introspect"
	yaml "gopkg.in/yaml.v3"
)

// schemaTimeout bounds the dial and the catalog queries. A refused port
// fails at once; the bound is what stops a black-holed address from
// holding the command open.
const schemaTimeout = 30 * time.Second

// runSchema reads one table and writes a schema file. Flags are parsed
// here, as in runMap, so the help text and the exit codes can be tested
// without the other subcommands.
func runSchema(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dialect, dsn, table, outPath, renderPath, updatePath string
	var tui bool
	fs.StringVar(&dialect, "dialect", "", "postgres, mysql, mssql, or sqlite")
	fs.StringVar(&dsn, "dsn", "", "connection string, or a sqlite database path")
	fs.StringVar(&table, "table", "", "table to read (schema-qualified as public.users)")
	fs.StringVar(&outPath, "o", "", "schema file to write (default stdout)")
	fs.StringVar(&renderPath, "render", "", "also write a generation spec to this path")
	fs.StringVar(&updatePath, "update", "", "refresh facts in this schema file")
	// -tui is opt-in. A script keeps the flagless path, which still
	// exits 2 when dialect, dsn, or table was left off.
	fs.BoolVar(&tui, "tui", false, "open the schema screen; ask for anything not passed as a flag")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "mockagen schema: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	// Missing flags are questions on the screen, not usage errors, so
	// this returns before the checks below. A script without -tui still
	// hits those checks.
	if tui {
		return runSchemaTUI(dialect, dsn, outPath, renderPath, stderr)
	}

	// The flag wins, then this tool's own variable, then the generic
	// one. DATABASE_URL is shared with everything else on a machine, and
	// a mockagen-specific value should not lose to it.
	if dsn == "" {
		dsn = os.Getenv("MOCKAGEN_DSN")
	}
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}

	var prev schema.Schema
	havePrev := false
	if updatePath != "" {
		loaded, err := schema.Load(updatePath)
		if err != nil {
			fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
			return 1
		}
		prev = loaded
		havePrev = true
		if dialect == "" {
			dialect = prev.Source.Dialect
		}
		if table == "" {
			table = prev.Source.Table
		}
	}

	name, err := schemaDialect(dialect)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 2
	}
	if strings.TrimSpace(table) == "" {
		fmt.Fprintln(stderr, "mockagen schema: -table is required")
		return 2
	}
	if strings.TrimSpace(dsn) == "" {
		fmt.Fprintln(stderr, "mockagen schema: -dsn is required (or set MOCKAGEN_DSN or DATABASE_URL)")
		return 2
	}

	mysqlSchema := databaseName(name, dsn)
	if mysqlSchema == "" && havePrev {
		mysqlSchema = prev.Source.Database
	}
	schemaName, tableName, err := splitTable(name, table, mysqlSchema)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 2
	}
	dbName := databaseName(name, dsn)
	if name == "sqlite" {
		// sqlite has no database name. The path is the provenance, and
		// it is the path the user passed, so it stays stable.
		dbName = dsn
	} else if dbName == "" && havePrev {
		dbName = prev.Source.Database
	}

	ctx, cancel := context.WithTimeout(context.Background(), schemaTimeout)
	defer cancel()
	q, closeQ, err := openQuerier(ctx, name, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}
	defer closeQ()

	fresh, err := introspect.Inspect(ctx, q, introspect.Request{
		Dialect:  name,
		Schema:   schemaName,
		Table:    tableName,
		Database: dbName,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}

	var result schema.Schema
	var drift []string
	if havePrev {
		result, drift = schema.Update(prev, fresh)
	} else {
		result = schema.Infer(fresh)
	}
	for _, line := range drift {
		fmt.Fprintf(stderr, "mockagen schema: %s\n", line)
	}
	for _, w := range result.Warnings() {
		fmt.Fprintf(stderr, "mockagen schema: %s\n", w)
	}

	// -update with no -o rewrites the file it read. A separate -o leaves
	// that file as the input and writes the refresh somewhere else.
	dest := outPath
	if dest == "" && havePrev {
		dest = updatePath
	}
	if dest == "" {
		data, err := schema.Marshal(result, ".yaml")
		if err != nil {
			fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
			return 1
		}
		if _, err := stdout.Write(data); err != nil {
			fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
			return 1
		}
	} else if err := schema.Save(dest, result); err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}

	if renderPath == "" {
		return 0
	}
	// The schema file is already written. Render refuses an unresolved
	// column, and that refusal should not throw away the file the next
	// edit fixes.
	cfg, err := result.Render(schema.RenderOptions{})
	if err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}
	if err := writeSpec(renderPath, cfg); err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}
	return 0
}

// schemaDialect rejects an empty name before sqlgen.Lookup. Lookup's
// default is postgres, and an omitted -dialect must not connect to it.
func schemaDialect(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("dialect is required")
	}
	d, err := sqlgen.Lookup(name)
	if err != nil {
		return "", err
	}
	switch d.Name {
	case "postgres", "mysql", "mssql", "sqlite":
		return d.Name, nil
	default:
		return "", fmt.Errorf("unknown dialect %q", name)
	}
}

// splitTable accepts schema.table. An unqualified postgres table is
// public and an unqualified mssql table is dbo, which is where an
// omitted schema actually lands. mysql's namespace is the database, so
// it has to come from the DSN or from a qualifier. sqlite keeps the
// whole string: it has no schema, and a dot can belong to the name.
func splitTable(dialect, table, mysqlSchema string) (string, string, error) {
	table = strings.TrimSpace(table)
	if table == "" {
		return "", "", errors.New("table is required")
	}
	if dialect == "sqlite" {
		return "", table, nil
	}
	if schemaName, name, ok := strings.Cut(table, "."); ok {
		if schemaName == "" || name == "" || strings.Contains(name, ".") {
			return "", "", fmt.Errorf("table %q must be schema.table", table)
		}
		return schemaName, name, nil
	}
	switch dialect {
	case "postgres":
		return "public", table, nil
	case "mssql":
		return "dbo", table, nil
	case "mysql":
		if mysqlSchema == "" {
			return "", "", errors.New("mysql table must be database-qualified, or the DSN must name a database")
		}
		return mysqlSchema, table, nil
	default:
		return "", "", fmt.Errorf("unknown dialect %q", dialect)
	}
}

// databaseName pulls the database out of a DSN for provenance and, on
// mysql, for the catalog filter. A keyword libpq DSN is not a URL.
// Failure leaves the name empty; introspection still runs, and mysql
// then requires a qualified table.
func databaseName(dialect, dsn string) string {
	switch dialect {
	case "postgres":
		if u, err := url.Parse(dsn); err == nil {
			if db := strings.Trim(strings.TrimPrefix(u.Path, "/"), "/"); db != "" && u.Host != "" {
				return db
			}
		}
		return keywordDSN(dsn, "dbname")
	case "mysql":
		i := strings.LastIndex(dsn, "/")
		if i < 0 {
			return ""
		}
		rest := dsn[i+1:]
		if j := strings.IndexByte(rest, '?'); j >= 0 {
			rest = rest[:j]
		}
		return strings.Trim(rest, "/")
	case "mssql":
		u, err := url.Parse(dsn)
		if err != nil {
			return ""
		}
		if db := u.Query().Get("database"); db != "" {
			return db
		}
		return strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
	default:
		return ""
	}
}

// keywordDSN reads one libpq-style field. That connection string is
// not a URL, so the database is a key rather than a path.
func keywordDSN(dsn, key string) string {
	for _, field := range strings.Fields(dsn) {
		k, v, ok := strings.Cut(field, "=")
		if ok && strings.EqualFold(k, key) && v != "" {
			return v
		}
	}
	return ""
}

// openQuerier picks the driver by the name it registered, which is not
// the dialect name: pgx's stdlib registers "pgx" and go-mssqldb
// registers "sqlserver". sqlite has no driver.
func openQuerier(ctx context.Context, dialect, dsn string) (introspect.Querier, func(), error) {
	if dialect == "sqlite" {
		return introspect.SQLiteCLI{DB: dsn}, func() {}, nil
	}
	driver := map[string]string{
		"postgres": "pgx",
		"mysql":    "mysql",
		"mssql":    "sqlserver",
	}[dialect]
	if driver == "" {
		return nil, nil, fmt.Errorf("unknown dialect %q", dialect)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		dbClose(db)
		return nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, nil, err
	}
	return introspect.SQLQuerier{DB: db}, func() { db.Close() }, nil
}

// dbClose releases a handle sql.Open returned together with an error.
// A nil handle means the open did not produce one, and Close on nil
// panics.
func dbClose(db *sql.DB) {
	if db != nil {
		db.Close()
	}
}

// writeSpec writes a generation spec. The extension picks the
// encoding, and an unknown one is an error rather than a YAML file
// under a surprising name. JSON has a trailing newline because the
// YAML encoder already ends the file that way.
func writeSpec(path string, cfg mockagen.MockagenConfig) error {
	var data []byte
	var err error
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		data, err = json.MarshalIndent(&cfg, "", "  ")
		if err == nil {
			data = append(data, '\n')
		}
	case "", ".yaml", ".yml":
		data, err = yaml.Marshal(&cfg)
	default:
		return fmt.Errorf("unsupported spec format %q", filepath.Ext(path))
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
