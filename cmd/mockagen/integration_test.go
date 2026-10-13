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
// The servers are declared below and started with testcontainers: each one
// comes up the first time a test needs it and is removed when the package's
// tests finish, so `mise run test-integration` is the whole workflow and a
// panic or a Ctrl-C cannot leave containers behind - testcontainers' reaper
// removes them even then.
//
// Starting per server rather than as a stack means running one dialect's
// subtest starts only that dialect's container; SQL Server, the slowest by
// far, stays unstarted unless something asks for it.
//
// Each test talks to its server through that server's own CLI client, not
// through a Go SQL driver, because the bulk scripts are written for those
// clients: psql's \copy and mysql's LOAD DATA LOCAL INFILE are client
// features a driver would never exercise. testcontainers is here for the
// lifecycle and for Exec/CopyFileToContainer, not for a connection string.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// mssqlPassword has to satisfy SQL Server's complexity rules, and is used
// both to start the server and to connect to it.
const mssqlPassword = "Mockagen1!"

// startupTimeout bounds how long a server gets to become usable. SQL Server
// needs the most of the three and is comfortably inside this on a cold start.
const startupTimeout = 3 * time.Minute

// started holds the containers this run has booted, so each is started once
// and TestMain can remove them all afterwards.
var (
	startedMu  sync.Mutex
	started    = map[string]testcontainers.Container{}
	startErrs  = map[string]error{}
	noDockerMu sync.Once
	noDocker   bool
)

// errNoDocker marks the one failure that should skip rather than fail: no
// docker at all. Anything else that goes wrong starting a server is a real
// error, since running with -tags=integration is opting in.
var errNoDocker = errors.New("docker not installed")

// container starts this target's server if it is not already running, and
// returns the same one to every later caller.
func (tg target) container(t *testing.T) testcontainers.Container {
	t.Helper()
	noDockerMu.Do(func() {
		_, err := exec.LookPath("docker")
		noDocker = err != nil
	})
	if noDocker {
		t.Skip("docker not installed")
	}

	startedMu.Lock()
	defer startedMu.Unlock()
	if err, ok := startErrs[tg.dialect]; ok {
		t.Fatal(err)
	}
	if c, ok := started[tg.dialect]; ok {
		return c
	}

	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithEnv(tg.image.env),
		testcontainers.WithWaitStrategyAndDeadline(startupTimeout, tg.image.ready),
	}
	if len(tg.image.cmd) > 0 {
		opts = append(opts, testcontainers.WithCmd(tg.image.cmd...))
	}
	c, err := testcontainers.Run(context.Background(), tg.image.ref, opts...)
	if err != nil {
		err = fmt.Errorf("starting %s (%s): %w", tg.dialect, tg.image.ref, err)
		startErrs[tg.dialect] = err
		t.Fatal(err)
	}
	started[tg.dialect] = c
	return c
}

// TestMain removes the containers after the package's tests rather than after
// each one, so a server is started at most once per run. os.Exit skips
// deferred calls, hence the explicit teardown.
func TestMain(m *testing.M) {
	code := m.Run()
	startedMu.Lock()
	for dialect, c := range started {
		if err := c.Terminate(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "terminating %s: %v\n", dialect, err)
		}
	}
	startedMu.Unlock()
	os.Exit(code)
}

// serverImage is how one database server is started: the image, the
// environment it needs, any command override, and how to tell it is usable.
//
// The readiness checks run the server's own client inside the container
// rather than probing a port, because a listening socket is not the same as
// an initialised database - every one of these images accepts a connection
// before it will answer a query.
type serverImage struct {
	ref   string
	env   map[string]string
	cmd   []string
	ready wait.Strategy
}

// containerBundleDir is where a generated bundle is copied inside the
// container. The bulk scripts reference their CSV by bare name, so the client
// is run with this as its working directory - the same thing a user does
// after unpacking the pair on the database host.
const containerBundleDir = "/tmp/mockagen-bundle"

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
		if r == '�' {
			return false
		}
	}
	return true
}

// target describes one database server and how to drive it.
type target struct {
	dialect string      // mockagen dialect name
	image   serverImage // how to start its server

	// client is the argv prefix that runs the server's CLI client against
	// the mockagen database.
	client []string
	// env is set for every exec. mysql needs it: Exec is read back
	// multiplexed, so stderr lands in the same stream as the query output,
	// and passing the password with -p would mix a warning about doing
	// exactly that into the rows. MYSQL_PWD avoids the warning entirely.
	env []string
	// initSQL runs before anything else, to create the database where the
	// image does not make one.
	initSQL []string
	// ddl creates the two-column table the tests load into. Column order
	// matters: mssql's BULK INSERT maps CSV fields by position. The value
	// column is NOT NULL on purpose - a loader that turns a field into NULL
	// then fails the load loudly instead of quietly storing one.
	ddl string
	// fileFlag is how the client is told to execute a file (nil means feed
	// it on stdin).
	fileFlag string
	// queryFlag is how the client is given one statement to run.
	queryFlag string
	// selectHex returns "<id>:<hex of value>" per row, ordered by id. The id
	// prefix keeps an empty value from reading as a blank line.
	selectHex string
	// encode renders an expected Go string the way this server stores it.
	encode func(string) string
	// bulkASCIIOnly restricts the bulk round-trip to ASCII values. Only
	// mssql sets it, and only because BULK INSERT cannot read a UTF-8 file
	// on SQL Server for Linux: CODEPAGE = '65001' would say so but is
	// rejected on that platform, a UTF-8 collation on the target column does
	// not help (it describes storage, not the file), and DATAFILETYPE cannot
	// be combined with FORMAT = 'CSV'. TestIntegrationMSSQLBulkNonASCII pins
	// the limitation so it is recorded rather than quietly skipped.
	bulkASCIIOnly bool
}

func isASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func utf8Hex(s string) string { return hex.EncodeToString([]byte(s)) }

// utf16leHex is how mssql stores an nvarchar: UTF-16, little-endian, with
// non-BMP characters as surrogate pairs.
func utf16leHex(s string) string {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return hex.EncodeToString(b)
}

func targets() []target {
	return []target{
		{
			dialect: "postgres",
			image: serverImage{
				ref: "postgres:17-alpine",
				env: map[string]string{
					"POSTGRES_PASSWORD": "mockagen",
					"POSTGRES_DB":       "mockagen",
				},
				ready: wait.ForExec([]string{"pg_isready", "-U", "postgres", "-d", "mockagen"}).
					WithPollInterval(time.Second),
			},
			client:    []string{"psql", "-U", "postgres", "-d", "mockagen", "-v", "ON_ERROR_STOP=1", "-q", "-t", "-A"},
			ddl:       "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id text NOT NULL, v text NOT NULL);",
			fileFlag:  "-f",
			queryFlag: "-c",
			selectHex: "SELECT id || ':' || encode(convert_to(v,'UTF8'),'hex') FROM vals ORDER BY id::int",
			encode:    utf8Hex,
		},
		{
			dialect: "mysql",
			image: serverImage{
				ref: "mysql:8.4",
				// local_infile is off by default on the server side, and LOAD
				// DATA LOCAL INFILE needs it on both ends - the same flag the
				// generated bulk script tells the user to pass to the client.
				cmd: []string{"--local-infile=1"},
				env: map[string]string{
					"MYSQL_ROOT_PASSWORD": "mockagen",
					"MYSQL_DATABASE":      "mockagen",
				},
				ready: wait.ForExec([]string{
					"mysqladmin", "ping", "-h", "127.0.0.1", "-uroot", "-pmockagen", "--silent",
				}).WithPollInterval(2 * time.Second),
			},
			// --default-character-set is not optional: mockagen writes UTF-8,
			// and without it the client announces latin1 and the server
			// silently stores the mojibake re-encoding of every non-ASCII
			// value. --local-infile is what the generated bulk script asks
			// for; it is harmless in insert mode.
			client:    []string{"mysql", "--default-character-set=utf8mb4", "--local-infile=1", "-uroot", "-D", "mockagen", "-N", "-B"},
			env:       []string{"MYSQL_PWD=mockagen"},
			ddl:       "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id text NOT NULL, v text NOT NULL);",
			queryFlag: "-e",
			selectHex: "SELECT CONCAT(id, ':', HEX(v)) FROM vals ORDER BY CAST(id AS SIGNED)",
			encode:    utf8Hex,
		},
		{
			dialect: "mssql",
			image: serverImage{
				ref: "mcr.microsoft.com/mssql/server:2022-latest",
				env: map[string]string{
					"ACCEPT_EULA":       "Y",
					"MSSQL_SA_PASSWORD": mssqlPassword,
					"MSSQL_PID":         "Developer",
				},
				ready: wait.ForExec([]string{
					"/opt/mssql-tools18/bin/sqlcmd", "-S", "localhost", "-U", "sa",
					"-P", mssqlPassword, "-C", "-Q", "SELECT 1",
				}).WithPollInterval(3 * time.Second),
			},
			// -C trusts the image's self-signed certificate, -b makes sqlcmd
			// exit non-zero on a SQL error (it does not by default, so a
			// failed load looked like a successful one), and -h -1 drops
			// headers. The two width flags are both needed and fought each
			// other to get right: sqlcmd wraps output at 80 columns (-w), and
			// separately truncates a variable-length column at 256
			// characters (-y), which silently cut the longest hex value in
			// half. -y cannot be combined with -W, and -y 0 cannot be
			// combined with -h, so the padding -W would have trimmed is
			// trimmed in Go instead. 8000 is -y's maximum, which caps a test
			// value at about 4000 characters.
			client:        []string{"/opt/mssql-tools18/bin/sqlcmd", "-S", "localhost", "-U", "sa", "-P", mssqlPassword, "-C", "-b", "-d", "mockagen", "-h", "-1", "-w", "65535", "-y", "8000"},
			initSQL:       []string{"IF DB_ID('mockagen') IS NULL CREATE DATABASE mockagen;"},
			ddl:           "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id nvarchar(50) NOT NULL, v nvarchar(max) NOT NULL);",
			fileFlag:      "-i",
			queryFlag:     "-Q",
			selectHex:     "SET NOCOUNT ON; SELECT CAST(id AS VARCHAR(50)) + ':' + CONVERT(VARCHAR(MAX), CONVERT(VARBINARY(MAX), v), 2) FROM vals ORDER BY CAST(id AS INT)",
			encode:        utf16leHex,
			bulkASCIIOnly: true,
		},
	}
}

// execIn runs argv inside the service, optionally with a working directory,
// and returns its combined output.
func (tg target) execIn(t *testing.T, workdir string, argv ...string) string {
	t.Helper()
	// Multiplexed de-frames docker's stream protocol into one reader; without
	// it the output carries 8-byte headers between chunks.
	opts := []tcexec.ProcessOption{tcexec.Multiplexed()}
	if workdir != "" {
		opts = append(opts, tcexec.WithWorkingDir(workdir))
	}
	if len(tg.env) > 0 {
		opts = append(opts, tcexec.WithEnv(tg.env))
	}

	code, reader, err := tg.container(t).Exec(context.Background(), argv, opts...)
	out, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("%s: reading output: %v", strings.Join(argv, " "), readErr)
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	if code != 0 {
		t.Fatalf("%s: exit %d\n%s", strings.Join(argv, " "), code, out)
	}
	return string(out)
}

// query runs one statement and returns stdout.
func (tg target) query(t *testing.T, sql string) string {
	t.Helper()
	return tg.execIn(t, "", append(tg.client, tg.queryFlag, sql)...)
}

// runFile executes a file already copied into the container at
// containerBundleDir, with that directory as the working directory.
func (tg target) runFile(t *testing.T, name string) {
	t.Helper()
	if tg.fileFlag == "" {
		// No file flag: feed it on stdin, which is what the generated mysql
		// script's own instructions do.
		tg.execIn(t, containerBundleDir, "sh", "-c",
			strings.Join(tg.client, " ")+" < "+name)
		return
	}
	tg.execIn(t, containerBundleDir, append(tg.client, tg.fileFlag, name)...)
}

// setup creates the database and table, and an empty bundle directory.
func (tg target) setup(t *testing.T) {
	t.Helper()
	for _, sql := range tg.initSQL {
		// The init statement runs against the server's default database, so
		// it cannot carry -d mockagen before that database exists.
		client := make([]string, 0, len(tg.client))
		for i := 0; i < len(tg.client); i++ {
			if tg.client[i] == "-d" {
				i++
				continue
			}
			client = append(client, tg.client[i])
		}
		tg.execIn(t, "", append(client, tg.queryFlag, sql)...)
	}
	tg.query(t, tg.ddl)
	tg.execIn(t, "", "rm", "-rf", containerBundleDir)
	tg.execIn(t, "", "mkdir", "-p", containerBundleDir)
}

// copyIn copies a local file into the container's bundle directory.
func (tg target) copyIn(t *testing.T, localPath string) {
	t.Helper()
	dest := containerBundleDir + "/" + filepath.Base(localPath)
	// 0o644 so the mssql server process, which reads the CSV itself rather
	// than through the client, can open it.
	if err := tg.container(t).CopyFileToContainer(
		context.Background(), localPath, dest, 0o644); err != nil {
		t.Fatalf("copying %s to %s: %v", localPath, dest, err)
	}
}

// loaded reads the table back and returns value hex keyed by row id.
func (tg target) loaded(t *testing.T) map[int]string {
	t.Helper()
	out := tg.query(t, tg.selectHex)
	got := make(map[int]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, hexVal, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("unparsable row %q in:\n%s", line, out)
		}
		n, err := strconv.Atoi(strings.TrimSpace(id))
		if err != nil {
			t.Fatalf("unparsable id in row %q", line)
		}
		got[n] = strings.ToLower(strings.TrimSpace(hexVal))
	}
	return got
}

// compare checks every value came back byte-for-byte.
func (tg target) compare(t *testing.T, want []string, got map[int]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("loaded %d rows, want %d", len(got), len(want))
	}
	for i, v := range want {
		wantHex := strings.ToLower(tg.encode(v))
		if got[i] != wantHex {
			t.Errorf("row %d (%q):\n got %s\nwant %s", i, v, got[i], wantHex)
		}
	}
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
	for _, tg := range targets() {
		t.Run(tg.dialect, func(t *testing.T) {
			tg.setup(t)

			dir := writeRecords(t, mockagen.MockagenConfig{
				FileFormat: "sql",
				Dialect:    tg.dialect,
				Table:      "vals",
			}, values, "out.sql")

			tg.copyIn(t, filepath.Join(dir, "out.sql"))
			tg.runFile(t, "out.sql")
			tg.compare(t, values, tg.loaded(t))
		})
	}
}

// TestIntegrationBulk loads the bundle each dialect generates, run the way
// the generated script's own comments say to: from the directory holding both
// files, with the server's own client.
func TestIntegrationBulk(t *testing.T) {
	for _, tg := range targets() {
		t.Run(tg.dialect, func(t *testing.T) {
			tg.setup(t)

			values := integrationValues()
			if tg.bulkASCIIOnly {
				var ascii []string
				for _, v := range values {
					if isASCIIOnly(v) {
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
				Dialect:       tg.dialect,
				Table:         "vals",
				IncludeHeader: true,
			}, values, "out.sql")

			script := filepath.Join(dir, "out.load.sql")
			if tg.dialect == "mssql" {
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
					"FROM '"+containerBundleDir+"/out.csv'", 1)
				if fixed == string(b) {
					t.Fatal("expected the mssql script to reference 'out.csv' by bare name")
				}
				if err := os.WriteFile(script, []byte(fixed), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			tg.copyIn(t, filepath.Join(dir, "out.csv"))
			tg.copyIn(t, script)
			tg.runFile(t, "out.load.sql")
			tg.compare(t, values, tg.loaded(t))
		})
	}
}

// TestIntegrationMSSQLUnicodePrefix is the targeted check for the one piece of
// dialect handling whose absence corrupts data silently rather than failing:
// without the N” prefix an nvarchar literal is parsed as varchar first, and
// anything outside the server's code page becomes a question mark.
func TestIntegrationMSSQLUnicodePrefix(t *testing.T) {
	var tg target
	for _, candidate := range targets() {
		if candidate.dialect == "mssql" {
			tg = candidate
		}
	}
	tg.setup(t)

	const value = "日本🎉"
	tg.query(t, fmt.Sprintf("INSERT INTO vals (id, v) VALUES ('1', N'%s'), ('2', '%s');", value, value))
	got := tg.loaded(t)

	if want := strings.ToLower(utf16leHex(value)); got[1] != want {
		t.Errorf("N'' literal: got %s, want %s", got[1], want)
	}
	if got[2] == strings.ToLower(utf16leHex(value)) {
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
	var tg target
	for _, candidate := range targets() {
		if candidate.dialect == "mssql" {
			tg = candidate
		}
	}
	tg.setup(t)

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
		"FROM 'out.csv'", "FROM '"+containerBundleDir+"/out.csv'", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tg.copyIn(t, filepath.Join(dir, "out.csv"))
	tg.copyIn(t, script)
	tg.runFile(t, "out.load.sql")

	got := tg.loaded(t)[0]
	if got == strings.ToLower(utf16leHex(value)) {
		t.Error("non-ASCII survived mssql BULK INSERT: the Linux CODEPAGE limitation " +
			"appears fixed - re-add CODEPAGE = '65001' to the script and update the README")
	}
	t.Logf("confirmed limitation: %q loaded as %s, not %s",
		value, got, strings.ToLower(utf16leHex(value)))

	// The script must warn about exactly this, so a user does not discover it
	// the way this test did.
	for _, want := range []string{"NON-ASCII", "CODEPAGE", "Linux", "mode: insert"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("generated mssql script should mention %q", want)
		}
	}
}
