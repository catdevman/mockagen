//go:build integration

// Integration tests that run generated SQL through real database servers.
//
// The golden-output tests in writer_test.go pin what each dialect's bytes
// look like; these check that the bytes actually load, and that every value
// comes back as exactly what went in. That matters most for the three places
// the dialects genuinely differ - mysql's backslash escaping, mssql's N”
// prefix, and each one's bulk loader - because getting those wrong produces
// corrupted data rather than an error.
//
// Container lifecycle, execution, and dialect targeting are managed by
// internal/dbtest.
package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/internal/dbtest"
	"github.com/catdevman/mockagen/pkg/mockagen"
)

// TestMain removes the containers after the package's tests rather than after
// each one, so a server is started at most once per run. os.Exit skips
// deferred calls, hence the explicit teardown.
func TestMain(m *testing.M) {
	code := m.Run()
	dbtest.Teardown()
	os.Exit(code)
}

// integrationValues is the value set these tests round-trip. It is csvTricky
// minus two cases that belong to the databases rather than to mockagen:
//
//   - values holding a NUL, which the sql writer drops by design (no
//     dialect's literal syntax can carry one) and postgres rejects outright;
//   - invalid UTF-8, which a UTF8 database correctly refuses.
//
// Neither is reachable from a generator type. What is left is every case that
// decides whether escaping is right: backslashes, quotes, embedded newlines,
// a leading space, non-BMP characters, and a value long enough to rule out
// truncation.
func integrationValues() []string {
	var out []string
	for _, v := range csvTricky {
		if strings.ContainsRune(v, 0) || !utf8Valid(v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\ufffd' {
			return false
		}
	}
	return true
}

// writeRecords runs the values through newRecordWriter for the given config,
// returning the directory it wrote into.
func writeRecords(t *testing.T, config mockagen.MockagenConfig, values []string, outName string) string {
	t.Helper()
	cols := []string{"id", "v"}
	structArr, recType := csvRecordType(cols)
	for i := range structArr {
		// The sql writer reads the `sql` tag, the csv writer the `csv` one;
		// the generator writes both, so mirror that here.
		structArr[i].Tag = reflect.StructTag(fmt.Sprintf("csv:%q sql:%q", cols[i], cols[i]))
	}
	config.Columns = []mockagen.MockagenColumn{{Name: "id"}, {Name: "v"}}

	dir := t.TempDir()
	w, err := newRecordWriter(config, structArr, filepath.Join(dir, outName))
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range values {
		rec := reflect.New(recType).Elem()
		rec.Field(0).SetString(strconv.Itoa(i))
		rec.Field(1).SetString(v)
		if err := w.WriteRecord(rec.Interface()); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestIntegrationInsert loads the INSERT statements each dialect generates and
// checks every value survives.
func TestIntegrationInsert(t *testing.T) {
	values := integrationValues()
	for _, tg := range dbtest.ServerTargets() {
		t.Run(tg.Dialect, func(t *testing.T) {
			tg.Setup(t)

			dir := writeRecords(t, mockagen.MockagenConfig{
				FileFormat: "sql",
				Dialect:    tg.Dialect,
				Table:      "vals",
			}, values, "out.sql")

			tg.CopyIn(t, filepath.Join(dir, "out.sql"))
			tg.RunFile(t, "out.sql")
			tg.Compare(t, values, tg.Loaded(t))
		})
	}
}

// TestIntegrationBulk loads the bundle each dialect generates, run the way
// the generated script's own comments say to: from the directory holding both
// files, with the server's own client.
func TestIntegrationBulk(t *testing.T) {
	for _, tg := range dbtest.ServerTargets() {
		t.Run(tg.Dialect, func(t *testing.T) {
			tg.Setup(t)

			values := integrationValues()
			if tg.BulkASCIIOnly {
				var ascii []string
				for _, v := range values {
					if dbtest.IsASCIIOnly(v) {
						ascii = append(ascii, v)
					}
				}
				t.Logf("restricted to %d ASCII values of %d; see bulkASCIIOnly",
					len(ascii), len(values))
				values = ascii
			}

			dir := writeRecords(t, mockagen.MockagenConfig{
				FileFormat:    "sql",
				Mode:          "bulk",
				Dialect:       tg.Dialect,
				Table:         "vals",
				IncludeHeader: true,
			}, values, "out.sql")

			script := filepath.Join(dir, "out.load.sql")
			if tg.Dialect == "mssql" {
				// BULK INSERT resolves the path in the server process, not in
				// the directory sqlcmd runs from, so the generated script says
				// to substitute an absolute path once the bundle is unpacked
				// on the server. Do exactly that - it is the documented
				// workaround, and this is what checks it works.
				b, err := os.ReadFile(script)
				if err != nil {
					t.Fatal(err)
				}
				fixed := strings.Replace(string(b), "FROM 'out.csv'",
					"FROM '"+dbtest.ContainerBundleDir+"/out.csv'", 1)
				if fixed == string(b) {
					t.Fatal("expected the mssql script to reference 'out.csv' by bare name")
				}
				if err := os.WriteFile(script, []byte(fixed), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			tg.CopyIn(t, filepath.Join(dir, "out.csv"))
			tg.CopyIn(t, script)
			tg.RunFile(t, "out.load.sql")
			tg.Compare(t, values, tg.Loaded(t))
		})
	}
}

// TestIntegrationMSSQLUnicodePrefix is the targeted check for the one piece of
// dialect handling whose absence corrupts data silently rather than failing:
// without the N” prefix an nvarchar literal is parsed as varchar first, and
// anything outside the server's code page becomes a question mark.
func TestIntegrationMSSQLUnicodePrefix(t *testing.T) {
	tg, ok := dbtest.Lookup("mssql")
	if !ok {
		t.Fatal("mssql target not found")
	}
	tg.Setup(t)

	const value = "日本🎉"
	tg.Query(t, fmt.Sprintf("INSERT INTO vals (id, v) VALUES ('1', N'%s'), ('2', '%s');", value, value))
	got := tg.Loaded(t)

	if want := strings.ToLower(dbtest.UTF16LEHex(value)); got[1] != want {
		t.Errorf("N'' literal: got %s, want %s", got[1], want)
	}
	if got[2] == strings.ToLower(dbtest.UTF16LEHex(value)) {
		t.Error("an unprefixed literal preserved non-ASCII; the N'' prefix may no longer be needed")
	} else {
		t.Logf("confirmed: an unprefixed literal degraded to %s", got[2])
	}
}

// TestIntegrationMSSQLBulkNonASCII records a limitation rather than a bug in
// mockagen: BULK INSERT on SQL Server for Linux reads the UTF-8 CSV in the
// server's default codepage, so non-ASCII values are stored as mojibake.
// CODEPAGE = '65001' is the documented fix and the platform rejects it; the
// generated script says so and points at mode: insert, which carries unicode
// correctly through its N” literals (TestIntegrationInsert/mssql proves it).
//
// If this test ever fails because the value survived, the limitation is gone:
// re-add CODEPAGE to the generated script and update the README.
func TestIntegrationMSSQLBulkNonASCII(t *testing.T) {
	tg, ok := dbtest.Lookup("mssql")
	if !ok {
		t.Fatal("mssql target not found")
	}
	tg.Setup(t)

	const value = "日本語"
	dir := writeRecords(t, mockagen.MockagenConfig{
		FileFormat:    "sql",
		Mode:          "bulk",
		Dialect:       "mssql",
		Table:         "vals",
		IncludeHeader: true,
	}, []string{value}, "out.sql")

	script := filepath.Join(dir, "out.load.sql")
	b, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(strings.Replace(string(b),
		"FROM 'out.csv'", "FROM '"+dbtest.ContainerBundleDir+"/out.csv'", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tg.CopyIn(t, filepath.Join(dir, "out.csv"))
	tg.CopyIn(t, script)
	tg.RunFile(t, "out.load.sql")

	got := tg.Loaded(t)[0]
	if got == strings.ToLower(dbtest.UTF16LEHex(value)) {
		t.Error("non-ASCII survived mssql BULK INSERT: the Linux CODEPAGE limitation " +
			"appears fixed - re-add CODEPAGE = '65001' to the script and update the README")
	}
	t.Logf("confirmed limitation: %q loaded as %s, not %s",
		value, got, strings.ToLower(dbtest.UTF16LEHex(value)))

	// The script must warn about exactly this, so a user does not discover it
	// the way this test did.
	for _, want := range []string{"NON-ASCII", "CODEPAGE", "Linux", "mode: insert"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("generated mssql script should mention %q", want)
		}
	}
}

// TestIntegrationDSN checks that each target's DSN is well formed, not merely
// that it mentions the mapped port: a DSN with the wrong scheme, the wrong
// user or no database name contains the port just as happily, and
// introspection - the first thing here that actually dials one - is where
// that would surface.
func TestIntegrationDSN(t *testing.T) {
	for _, tg := range dbtest.Targets() {
		t.Run(tg.Dialect, func(t *testing.T) {
			dsn := tg.DSN(t)
			if dsn == "" {
				t.Fatalf("%s: empty DSN", tg.Dialect)
			}

			// sqlite's "DSN" is a filesystem path, and the harness keeps it
			// per process so concurrent runs cannot share a database.
			if tg.Dialect == "sqlite" {
				if dsn != dbtest.SQLiteDBPath {
					t.Errorf("sqlite DSN: got %q, want %q", dsn, dbtest.SQLiteDBPath)
				}
				if !filepath.IsAbs(dsn) {
					t.Errorf("sqlite DSN %q is not an absolute path", dsn)
				}
				return
			}

			host, port := tg.Host(t), tg.MappedPort(t)
			if port <= 0 {
				t.Fatalf("%s: invalid mapped port %d", tg.Dialect, port)
			}

			// MySQL's is go-sql-driver's own format rather than a URL, so it
			// is pinned whole.
			if tg.Dialect == "mysql" {
				want := fmt.Sprintf("root:mockagen@tcp(%s:%d)/mockagen", host, port)
				if dsn != want {
					t.Errorf("mysql DSN = %q, want %q", dsn, want)
				}
				return
			}

			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("%s DSN %q does not parse: %v", tg.Dialect, dsn, err)
			}

			var wantScheme, wantUser, wantPassword, gotDB string
			switch tg.Dialect {
			case "postgres":
				wantScheme, wantUser, wantPassword = "postgres", "postgres", "mockagen"
				gotDB = strings.TrimPrefix(u.Path, "/")
			case "mssql":
				wantScheme, wantUser, wantPassword = "sqlserver", "sa", dbtest.MSSQLPassword
				gotDB = u.Query().Get("database")
			default:
				t.Fatalf("no DSN expectations for dialect %q", tg.Dialect)
			}

			if u.Scheme != wantScheme {
				t.Errorf("scheme = %q, want %q", u.Scheme, wantScheme)
			}
			if u.Hostname() != host {
				t.Errorf("host = %q, want %q", u.Hostname(), host)
			}
			if u.Port() != strconv.Itoa(port) {
				t.Errorf("port = %q, want %d", u.Port(), port)
			}
			if u.User.Username() != wantUser {
				t.Errorf("user = %q, want %q", u.User.Username(), wantUser)
			}
			// The password has to survive parsing intact, which is the part a
			// substring check could never show: mssql's contains a "!", and a
			// DSN that escaped or truncated it would still "contain the port".
			if pw, ok := u.User.Password(); !ok || pw != wantPassword {
				t.Errorf("password = %q (set: %t), want %q", pw, ok, wantPassword)
			}
			if gotDB != "mockagen" {
				t.Errorf("database = %q, want %q", gotDB, "mockagen")
			}
		})
	}
}
