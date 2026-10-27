package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/catdevman/mockagen/internal/tui/schemaui"
	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/mockagen/sqlgen"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/catdevman/mockagen/pkg/schema/introspect"
)

// sampleN is the live sample the columns screen shows. Five rows are
// enough to see a null beside a real value, and the preview cap matches
// it so the two peeks stay the same size.
const sampleN = 5

// runSchemaScreen is the schema screen. Tests replace it because a
// bubbletea program needs a terminal, and the flag rules are what this
// file is responsible for.
var runSchemaScreen = schemaui.Run

// runSchemaTUI opens the screen. Dialect is checked only when one was
// passed: an empty name is the screen's question, not a usage error.
func runSchemaTUI(dialect, dsn, outPath, renderPath string, stderr io.Writer) int {
	if strings.TrimSpace(dialect) != "" {
		name, err := schemaDialect(dialect)
		if err != nil {
			fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
			return 2
		}
		dialect = name
	}
	envName, envDSN := schemaEnv()
	path, err := defaultConnectionsPath()
	if err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}
	var conns []schemaui.Connection
	loaded, err := schemaui.LoadConnections(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}
	conns = loaded
	sess := schemaui.Session{
		Services:        schemaServices(path),
		Connections:     conns,
		ConnectionsPath: path,
		EnvDSN:          envDSN,
		EnvName:         envName,
		Dialect:         dialect,
		DSN:             dsn,
		OutPath:         outPath,
		RenderPath:      renderPath,
	}
	if err := runSchemaScreen(sess); err != nil {
		fmt.Fprintf(stderr, "mockagen schema: %v\n", err)
		return 1
	}
	return 0
}

// schemaEnv reports which variable holds a DSN, preferring this tool's
// own name over DATABASE_URL. The value is handed to the screen so it
// can offer the variable; the view prints the name, not the value.
func schemaEnv() (name, value string) {
	if v := os.Getenv("MOCKAGEN_DSN"); v != "" {
		return "MOCKAGEN_DSN", v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return "DATABASE_URL", v
	}
	return "", ""
}

// defaultConnectionsPath is the user config file. The screen receives
// the path and does not call UserConfigDir itself, so a test of the
// model can point Session.ConnectionsPath at a temp directory.
func defaultConnectionsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mockagen", "connections.yaml"), nil
}

// schemaServices is the real dial and write set. Each method opens its
// own connection because the screen's commands do not share a handle.
func schemaServices(path string) schemaui.Services {
	return schemaui.Services{
		Tables:          schemaTables,
		Inspect:         schemaInspect,
		ColumnSample:    schemaColumnSample,
		Preview:         schemaPreview,
		SaveSchema:      schemaSaveSchema,
		SaveSpec:        schemaSaveSpec,
		LoadConnections: schemaui.LoadConnections,
		SaveConnections: func(p string, cs []schemaui.Connection) error {
			if p == "" {
				p = path
			}
			return schemaui.SaveConnections(p, cs)
		},
	}
}

// schemaTables is Services.Tables. The timeout is the command line's
// schemaTimeout, so a screen dial fails on the same clock as -table.
func schemaTables(ctx context.Context, dialect, dsn string) ([]introspect.Table, error) {
	ctx, cancel := context.WithTimeout(ctx, schemaTimeout)
	defer cancel()
	name, err := schemaDialect(dialect)
	if err != nil {
		return nil, err
	}
	q, closeQ, err := openQuerier(ctx, name, dsn)
	if err != nil {
		return nil, err
	}
	defer closeQ()
	return introspect.Tables(ctx, q, name)
}

// schemaInspect is Services.Inspect. It reuses splitTable and Infer so
// a screen schema matches one written by the flag path.
func schemaInspect(ctx context.Context, dialect, dsn, schemaName, table string) (schema.Schema, error) {
	ctx, cancel := context.WithTimeout(ctx, schemaTimeout)
	defer cancel()
	name, err := schemaDialect(dialect)
	if err != nil {
		return schema.Schema{}, err
	}
	qualified := table
	if schemaName != "" && name != "sqlite" {
		qualified = schemaName + "." + table
	}
	mysqlSchema := databaseName(name, dsn)
	sch, tbl, err := splitTable(name, qualified, mysqlSchema)
	if err != nil {
		return schema.Schema{}, err
	}
	dbName := databaseName(name, dsn)
	if name == "sqlite" {
		// sqlite has no database name. The path is the provenance, as
		// in the flag path, so a later update can find the same file.
		dbName = dsn
	}
	q, closeQ, err := openQuerier(ctx, name, dsn)
	if err != nil {
		return schema.Schema{}, err
	}
	defer closeQ()
	fresh, err := introspect.Inspect(ctx, q, introspect.Request{
		Dialect:  name,
		Schema:   sch,
		Table:    tbl,
		Database: dbName,
	})
	if err != nil {
		return schema.Schema{}, err
	}
	return schema.Infer(fresh), nil
}

// schemaColumnSample is Services.ColumnSample. A query error is
// returned as the message's error; the columns screen stays up.
func schemaColumnSample(ctx context.Context, dialect, dsn, table string, cols []string) ([][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, schemaTimeout)
	defer cancel()
	if len(cols) == 0 {
		return nil, nil
	}
	query, err := sampleQuery(dialect, table, cols)
	if err != nil {
		return nil, err
	}
	name, err := schemaDialect(dialect)
	if err != nil {
		return nil, err
	}
	q, closeQ, err := openQuerier(ctx, name, dsn)
	if err != nil {
		return nil, err
	}
	defer closeQ()
	rows, err := q.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(rows))
	for r, row := range rows {
		rec := make([]string, len(cols))
		for i, col := range cols {
			if v, ok := lookupCell(row, fmt.Sprintf("c%d", i), col); ok {
				rec[i] = v
			} else {
				// Querier drops NULL keys. The word is what the screen
				// shows, so a null is not mistaken for an empty string.
				rec[i] = "NULL"
			}
		}
		out[r] = rec
	}
	return out, nil
}

// sampleQuery selects the named columns only. SELECT * would pull
// columns the screen did not ask for, and mssql has no LIMIT.
func sampleQuery(dialect, table string, cols []string) (string, error) {
	name, err := schemaDialect(dialect)
	if err != nil {
		return "", err
	}
	d, err := sqlgen.Lookup(name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if name == "mssql" {
		fmt.Fprintf(&b, "SELECT TOP (%d) ", sampleN)
	} else {
		b.WriteString("SELECT ")
	}
	for i, col := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.Write(d.AppendIdent(nil, col))
		b.WriteString(" AS ")
		b.Write(d.AppendIdent(nil, fmt.Sprintf("c%d", i)))
	}
	b.WriteString(" FROM ")
	if name == "sqlite" {
		// A sqlite table name can contain a dot. Quoting it as one
		// identifier keeps that dot in the name instead of a schema.
		b.Write(d.AppendIdent(nil, table))
	} else {
		b.Write(d.AppendQualifiedIdent(nil, table))
	}
	if name != "mssql" {
		fmt.Fprintf(&b, " LIMIT %d", sampleN)
	}
	return b.String(), nil
}

// lookupCell finds an alias or the column name. Drivers fold case, so
// the match is case-insensitive; a missing key is NULL, not "".
func lookupCell(row introspect.Row, keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := row[key]; ok {
			return v, true
		}
		for k, v := range row {
			if strings.EqualFold(k, key) {
				return v, true
			}
		}
	}
	return "", false
}

// schemaPreview caps the peek at five rows and drops unresolved columns
// inside PreviewRows before generateFakes can fill them with random text.
func schemaPreview(s schema.Schema, n int) ([][]string, error) {
	if n > sampleN {
		n = sampleN
	}
	if n < 0 {
		n = 0
	}
	return schemaui.PreviewRows(s, n, schemaPreviewGen)
}

// schemaPreviewGen reads string fields off each generateFakes record.
// The generator stays in this package; moving it would put faker on
// every caller of the schema types.
func schemaPreviewGen(cfg mockagen.MockagenConfig) ([][]string, error) {
	ch, _ := generateFakes(cfg)
	var rows [][]string
	for rec := range ch {
		v := reflect.ValueOf(rec)
		if !v.IsValid() {
			continue
		}
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				continue
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return nil, fmt.Errorf("preview: record is %s", v.Kind())
		}
		row := make([]string, v.NumField())
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			switch f.Kind() {
			case reflect.String:
				row[i] = f.String()
			default:
				if f.CanInterface() {
					row[i] = fmt.Sprint(f.Interface())
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// schemaSaveSchema is Services.SaveSchema. ensureOutputDir creates the
// parent, which schema.Save itself does not.
func schemaSaveSchema(path string, s schema.Schema) error {
	if err := ensureOutputDir(path); err != nil {
		return err
	}
	return schema.Save(path, s)
}

// schemaSaveSpec renders without Force. An unresolved column is an
// error on the screen rather than a spec full of random strings.
func schemaSaveSpec(path string, s schema.Schema) error {
	cfg, err := s.Render(schema.RenderOptions{})
	if err != nil {
		return err
	}
	if err := ensureOutputDir(path); err != nil {
		return err
	}
	return writeSpec(path, cfg)
}
