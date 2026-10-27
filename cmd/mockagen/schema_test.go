package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSchemaUsage(t *testing.T) {
	t.Setenv("MOCKAGEN_DSN", "")
	t.Setenv("DATABASE_URL", "")
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{name: "no flags", args: nil, wantCode: 2, wantErr: "dialect is required"},
		{
			name:     "missing dsn",
			args:     []string{"-dialect", "postgres", "-table", "public.users"},
			wantCode: 2,
			wantErr:  "-dsn is required",
		},
		{
			name:     "missing table",
			args:     []string{"-dialect", "postgres", "-dsn", "postgres://localhost/db"},
			wantCode: 2,
			wantErr:  "-table is required",
		},
		{
			name:     "unknown dialect",
			args:     []string{"-dialect", "oracle", "-table", "t", "-dsn", "x"},
			wantCode: 2,
			wantErr:  "unsupported sql dialect",
		},
		{
			name:     "help",
			args:     []string{"-help"},
			wantCode: 0,
		},
		{
			name:     "unexpected argument",
			args:     []string{"-dialect", "postgres", "nope"},
			wantCode: 2,
			wantErr:  "unexpected argument",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runSchema(tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d\n%s", code, tc.wantCode, stderr.String())
			}
			if tc.wantErr != "" && !strings.Contains(stderr.String(), tc.wantErr) {
				t.Fatalf("stderr %q does not contain %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

func TestSchemaDialectAlias(t *testing.T) {
	got, err := schemaDialect("postgresql")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres" {
		t.Fatalf("dialect = %q, want postgres", got)
	}
	if _, err := schemaDialect(""); err == nil {
		t.Fatal("empty dialect succeeded")
	}
}

func TestSplitTable(t *testing.T) {
	cases := []struct {
		dialect string
		table   string
		mysql   string
		schema  string
		name    string
		wantErr string
	}{
		{dialect: "postgres", table: "public.users", schema: "public", name: "users"},
		{dialect: "postgres", table: "users", schema: "public", name: "users"},
		{dialect: "mssql", table: "widgets", schema: "dbo", name: "widgets"},
		{dialect: "mysql", table: "widgets", mysql: "mockagen", schema: "mockagen", name: "widgets"},
		{dialect: "mysql", table: "other.widgets", mysql: "mockagen", schema: "other", name: "widgets"},
		{dialect: "mysql", table: "widgets", wantErr: "database-qualified"},
		{dialect: "sqlite", table: "widgets", name: "widgets"},
		{dialect: "sqlite", table: "main.widgets", name: "main.widgets"},
		{dialect: "postgres", table: "a.b.c", wantErr: "schema.table"},
	}
	for _, tc := range cases {
		schemaName, name, err := splitTable(tc.dialect, tc.table, tc.mysql)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("split %s %q err = %v, want %q", tc.dialect, tc.table, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("split %s %q: %v", tc.dialect, tc.table, err)
			continue
		}
		if schemaName != tc.schema || name != tc.name {
			t.Errorf("split %s %q = %q.%q, want %q.%q", tc.dialect, tc.table, schemaName, name, tc.schema, tc.name)
		}
	}
}

func TestDatabaseName(t *testing.T) {
	cases := []struct {
		dialect string
		dsn     string
		want    string
	}{
		{dialect: "postgres", dsn: "postgres://postgres:mockagen@127.0.0.1:5432/mockagen?sslmode=disable", want: "mockagen"},
		{dialect: "postgres", dsn: "host=localhost user=postgres dbname=mockagen", want: "mockagen"},
		{dialect: "mysql", dsn: "root:mockagen@tcp(127.0.0.1:3306)/mockagen", want: "mockagen"},
		{dialect: "mysql", dsn: "root:mockagen@tcp(127.0.0.1:3306)/mockagen?parseTime=true", want: "mockagen"},
		{dialect: "mssql", dsn: "sqlserver://sa:Mockagen1!@127.0.0.1:1433?database=mockagen&trustServerCertificate=true", want: "mockagen"},
		{dialect: "sqlite", dsn: "/tmp/mockagen.db", want: ""},
	}
	for _, tc := range cases {
		got := databaseName(tc.dialect, tc.dsn)
		if got != tc.want {
			t.Errorf("databaseName(%s, %q) = %q, want %q", tc.dialect, tc.dsn, got, tc.want)
		}
	}
}
