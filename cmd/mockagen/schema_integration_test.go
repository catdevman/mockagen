//go:build integration

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/internal/dbtest"
	"github.com/catdevman/mockagen/pkg/schema"
)

// widgetCreate is one small table per dialect: a text primary key, a
// unique text column and a text column whose type the alter step changes.
// The schema command's -update path is what this exercises, so the fixture
// stays smaller than the catalog golden in pkg/schema/introspect.
var widgetCreate = map[string][]string{
	"postgres": {
		"DROP TABLE IF EXISTS widgets",
		`CREATE TABLE widgets (
		    id uuid PRIMARY KEY,
		    email varchar(50) NOT NULL UNIQUE,
		    label varchar(20) NOT NULL
		)`,
	},
	"mysql": {
		"DROP TABLE IF EXISTS widgets",
		`CREATE TABLE widgets (
		    id char(36) NOT NULL PRIMARY KEY,
		    email varchar(50) NOT NULL UNIQUE,
		    label varchar(20) NOT NULL
		)`,
	},
	"mssql": {
		"DROP TABLE IF EXISTS dbo.widgets",
		`CREATE TABLE dbo.widgets (
		    id uniqueidentifier NOT NULL PRIMARY KEY,
		    email nvarchar(50) NOT NULL UNIQUE,
		    label nvarchar(20) NOT NULL
		)`,
	},
	"sqlite": {
		"DROP TABLE IF EXISTS widgets",
		`CREATE TABLE widgets (
		    id TEXT NOT NULL PRIMARY KEY,
		    email TEXT NOT NULL UNIQUE,
		    label TEXT NOT NULL
		)`,
	},
}

var widgetAlter = map[string][]string{
	"postgres": {
		"ALTER TABLE widgets ALTER COLUMN label TYPE varchar(100)",
	},
	"mysql": {
		"ALTER TABLE widgets MODIFY label varchar(100) NOT NULL",
	},
	"mssql": {
		"ALTER TABLE dbo.widgets ALTER COLUMN label nvarchar(100) NOT NULL",
	},
	"sqlite": {
		"DROP TABLE widgets",
		`CREATE TABLE widgets (
		    id TEXT NOT NULL PRIMARY KEY,
		    email TEXT NOT NULL UNIQUE,
		    label VARCHAR(100) NOT NULL
		)`,
	},
}

var widgetTable = map[string]string{
	"postgres": "public.widgets",
	"mysql":    "widgets",
	"mssql":    "dbo.widgets",
	"sqlite":   "widgets",
}

// TestIntegrationSchemaUpdate introspects a live table, keeps a hand
// edit across -update, and reports a sql_type that changed underneath
// the file.
func TestIntegrationSchemaUpdate(t *testing.T) {
	for _, tg := range dbtest.Targets() {
		t.Run(tg.Dialect, func(t *testing.T) {
			tg.Setup(t)
			for _, stmt := range widgetCreate[tg.Dialect] {
				tg.Query(t, stmt)
			}
			dir := t.TempDir()
			out := filepath.Join(dir, "widgets.schema.yaml")
			var stdout, stderr bytes.Buffer
			code := runCLI([]string{
				"schema",
				"-dialect", tg.Dialect,
				"-dsn", tg.DSN(t),
				"-table", widgetTable[tg.Dialect],
				"-o", out,
			}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("schema exit %d\n%s", code, stderr.String())
			}
			loaded, err := schema.Load(out)
			if err != nil {
				t.Fatal(err)
			}
			id := widgetColumn(t, loaded, "id")
			if id.Type != "GUID" || id.InferredBy != "key:text-pk" {
				t.Fatalf("id inferred as %q by %q", id.Type, id.InferredBy)
			}
			oldLabel := widgetColumn(t, loaded, "label").SQLType
			for i := range loaded.Columns {
				if loaded.Columns[i].Name != "email" {
					continue
				}
				loaded.Columns[i].Type = "Word"
				loaded.Columns[i].InferredBy = "user"
				loaded.Columns[i].Values = nil
			}
			if err := schema.Save(out, loaded); err != nil {
				t.Fatal(err)
			}
			for _, stmt := range widgetAlter[tg.Dialect] {
				tg.Query(t, stmt)
			}
			stderr.Reset()
			code = runCLI([]string{"schema", "-update", out, "-dsn", tg.DSN(t)}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("update exit %d\n%s", code, stderr.String())
			}
			updated, err := schema.Load(out)
			if err != nil {
				t.Fatal(err)
			}
			email := widgetColumn(t, updated, "email")
			if email.Type != "Word" || email.InferredBy != "user" {
				t.Fatalf("email = type %q by %q, want Word by user", email.Type, email.InferredBy)
			}
			label := widgetColumn(t, updated, "label")
			if label.SQLType == oldLabel {
				t.Fatalf("label sql_type stayed %q", oldLabel)
			}
			if label.Type != "Sentence" || label.InferredBy != "type:text" {
				t.Fatalf("label inferred as %q by %q", label.Type, label.InferredBy)
			}
			msg := stderr.String()
			if !strings.Contains(msg, oldLabel) || !strings.Contains(msg, label.SQLType) {
				t.Fatalf("stderr did not report %q -> %q:\n%s", oldLabel, label.SQLType, msg)
			}
		})
	}
}

func widgetColumn(t *testing.T, s schema.Schema, name string) schema.Column {
	t.Helper()
	for _, c := range s.Columns {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("column %q not in schema", name)
	return schema.Column{}
}
