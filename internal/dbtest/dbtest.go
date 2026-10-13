// Package dbtest provides the database container test harness and targets
// used across integration tests in mockagen.
//
// Targets include the three containerized databases (PostgreSQL, MySQL,
// SQL Server) and a fourth local target (SQLite) whose container step is a
// no-op, allowing uniform four-dialect test tables.
//
// Containers are started lazily upon first need and terminated via Teardown,
// typically called from TestMain.
package dbtest

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ContainerBundleDir is where a generated bundle is copied inside the
// container (or the local filesystem for sqlite). The bulk scripts reference
// their CSV by bare name, so the client is run with this as its working
// directory - matching what a user does after unpacking the bundle.
const ContainerBundleDir = "/tmp/mockagen-bundle"

// hostWorkDir is where the sqlite target keeps its database and its bundle.
// Those are real host paths, because sqlite is the one target with no
// container to isolate it, and they carry the process id because `go test
// ./...` runs one process per package concurrently: with fixed names under
// /tmp, two packages' runs removed each other's database mid-test, and a
// directory left behind by another user made MkdirAll fail outright.
//
// Derived rather than created here, so that importing this package has no
// filesystem side effect. Setup creates it; Teardown removes it.
var hostWorkDir = filepath.Join(os.TempDir(),
	fmt.Sprintf("mockagen-dbtest-%d", os.Getpid()))

// HostBundleDir is the sqlite target's bundle directory, the host-side
// counterpart of ContainerBundleDir.
var HostBundleDir = filepath.Join(hostWorkDir, "bundle")

// SQLiteDBPath is the sqlite target's database file. It sits beside
// HostBundleDir rather than inside it, so that wiping the bundle between
// loads does not drop the active database.
var SQLiteDBPath = filepath.Join(hostWorkDir, "mockagen.db")

// MSSQLPassword satisfies SQL Server's password complexity rules (length,
// uppercase, lowercase, numbers, and symbols), and is used both to start the
// container and to connect to it.
const MSSQLPassword = "Mockagen1!"

// StartupTimeout bounds how long a database server gets to become usable.
// SQL Server needs the most of the three and is comfortably inside this on
// a cold start.
const StartupTimeout = 3 * time.Minute

var (
	startedMu  sync.Mutex
	started    = map[string]testcontainers.Container{}
	startErrs  = map[string]error{}
	noDockerMu sync.Once
	noDocker   bool
	sqliteMu   sync.Once
	noSQLite   bool
)

// ServerImage describes how one database server is started: the image ref,
// environment variables, command overrides, and a readiness probe strategy.
// The readiness checks run each server's own CLI client inside the container
// rather than probing ports, because listening sockets accept connections
// before the database engine finishes initialization.
type ServerImage struct {
	ref   string
	env   map[string]string
	cmd   []string
	ready wait.Strategy
}

// Target describes one database under test and encapsulates how to start,
// initialize, query, and bulk-load data into it across both containerized
// services and local SQLite.
type Target struct {
	// Dialect is the mockagen dialect identifier (e.g. "postgres", "mysql",
	// "mssql", "sqlite").
	Dialect string

	// Port is the internal TCP port for containerized databases (5432, 3306,
	// 1433). For SQLite, Port is 0.
	Port int

	// Client is the argv prefix that runs the database CLI client against
	// the test database.
	Client []string

	// Env is set for every command execution. MySQL requires MYSQL_PWD here
	// to prevent interactive password warnings on stderr.
	Env []string

	// InitSQL runs before anything else, creating the database where the
	// container image does not create one automatically (e.g. MSSQL).
	InitSQL []string

	// DDL creates the test table the integration tests load into.
	DDL string

	// FileFlag is how the CLI client is instructed to execute a file (empty
	// means pipe file content via stdin).
	FileFlag string

	// QueryFlag is how the CLI client is given a single statement to execute.
	QueryFlag string

	// SelectHex returns "<id>:<hex of value>" per row, ordered by id.
	SelectHex string

	// Encode renders an expected Go string as hex matching the database's
	// internal storage format.
	Encode func(string) string

	// BulkASCIIOnly restricts the bulk round-trip to ASCII values. Only
	// SQL Server for Linux requires this due to CODEPAGE limitations.
	BulkASCIIOnly bool

	// image holds the container launch specification (nil for SQLite).
	image *ServerImage
}

// Container starts this target's server if not already running, and returns
// the shared container instance. SQLite returns nil because it runs locally
// without Docker overhead.
func (tg Target) Container(t testing.TB) testcontainers.Container {
	t.Helper()
	if tg.Dialect == "sqlite" {
		sqliteMu.Do(func() {
			_, err := exec.LookPath("sqlite3")
			noSQLite = err != nil
		})
		if noSQLite {
			t.Skip("sqlite3 not installed")
		}
		return nil
	}

	noDockerMu.Do(func() {
		_, err := exec.LookPath("docker")
		noDocker = err != nil
	})
	if noDocker {
		t.Skip("docker not installed")
	}

	startedMu.Lock()
	defer startedMu.Unlock()
	if err, ok := startErrs[tg.Dialect]; ok {
		t.Fatal(err)
	}
	if c, ok := started[tg.Dialect]; ok {
		return c
	}

	if tg.image == nil {
		t.Fatalf("%s target has no container image configured", tg.Dialect)
	}

	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithEnv(tg.image.env),
		testcontainers.WithWaitStrategyAndDeadline(StartupTimeout, tg.image.ready),
	}
	if tg.Port > 0 {
		opts = append(opts, testcontainers.WithExposedPorts(fmt.Sprintf("%d/tcp", tg.Port)))
	}
	if len(tg.image.cmd) > 0 {
		opts = append(opts, testcontainers.WithCmd(tg.image.cmd...))
	}
	c, err := testcontainers.Run(context.Background(), tg.image.ref, opts...)
	if err != nil {
		err = fmt.Errorf("starting %s (%s): %w", tg.Dialect, tg.image.ref, err)
		startErrs[tg.Dialect] = err
		t.Fatal(err)
	}
	started[tg.Dialect] = c
	return c
}

// MappedPort returns the host TCP port mapped to the container's database
// port, allowing host-bound Go SQL drivers to connect. SQLite returns 0
// because it has no network port.
func (tg Target) MappedPort(t testing.TB) int {
	t.Helper()
	if tg.Dialect == "sqlite" {
		return 0
	}
	c := tg.Container(t)
	p, err := c.MappedPort(context.Background(), fmt.Sprintf("%d/tcp", tg.Port))
	if err != nil {
		t.Fatalf("mapped port for %s (%d): %v", tg.Dialect, tg.Port, err)
	}
	return int(p.Num())
}

// Host returns the reachable host address for the container port, resolving
// bridge or port-forwarding addresses. SQLite returns "" since it is local.
func (tg Target) Host(t testing.TB) string {
	t.Helper()
	if tg.Dialect == "sqlite" {
		return ""
	}
	c := tg.Container(t)
	host, err := c.Host(context.Background())
	if err != nil {
		t.Fatalf("host for %s: %v", tg.Dialect, err)
	}
	if host == "" || host == "0.0.0.0" {
		return "127.0.0.1"
	}
	return host
}

// DSN constructs a connection string suitable for database drivers pointing
// at this target. For SQLite, this returns the filesystem path to the file.
func (tg Target) DSN(t testing.TB) string {
	t.Helper()
	switch tg.Dialect {
	case "postgres":
		return fmt.Sprintf("postgres://postgres:mockagen@%s:%d/mockagen?sslmode=disable",
			tg.Host(t), tg.MappedPort(t))
	case "mysql":
		return fmt.Sprintf("root:mockagen@tcp(%s:%d)/mockagen",
			tg.Host(t), tg.MappedPort(t))
	case "mssql":
		return fmt.Sprintf("sqlserver://sa:%s@%s:%d?database=mockagen&trustServerCertificate=true",
			MSSQLPassword, tg.Host(t), tg.MappedPort(t))
	case "sqlite":
		return SQLiteDBPath
	default:
		t.Fatalf("unsupported dialect for DSN: %s", tg.Dialect)
		return ""
	}
}

// ExecIn runs argv inside the service (or locally for SQLite), optionally
// with a working directory, returning its combined output. Container runs
// use Multiplexed de-framing so Docker stream headers do not corrupt output.
func (tg Target) ExecIn(t testing.TB, workdir string, argv ...string) string {
	t.Helper()
	if tg.Dialect == "sqlite" {
		sqliteMu.Do(func() {
			_, err := exec.LookPath("sqlite3")
			noSQLite = err != nil
		})
		if noSQLite {
			t.Skip("sqlite3 not installed")
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		if workdir != "" {
			cmd.Dir = workdir
		}
		if len(tg.Env) > 0 {
			cmd.Env = append(os.Environ(), tg.Env...)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
		}
		return string(out)
	}

	opts := []tcexec.ProcessOption{tcexec.Multiplexed()}
	if workdir != "" {
		opts = append(opts, tcexec.WithWorkingDir(workdir))
	}
	if len(tg.Env) > 0 {
		opts = append(opts, tcexec.WithEnv(tg.Env))
	}

	code, reader, err := tg.Container(t).Exec(context.Background(), argv, opts...)
	// err first: Exec returns a nil reader on every failure path - a
	// terminated container, a cancelled context, a Docker API error - so
	// reading before checking panics on the nil reader and loses the only
	// message that says what actually went wrong.
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(argv, " "), err)
	}
	out, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("%s: reading output: %v", strings.Join(argv, " "), readErr)
	}
	if code != 0 {
		t.Fatalf("%s: exit %d\n%s", strings.Join(argv, " "), code, out)
	}
	return string(out)
}

// Query executes a single SQL statement using the database's CLI client and
// returns stdout, ensuring statements execute exactly as a user client would.
func (tg Target) Query(t testing.TB, sql string) string {
	t.Helper()
	if tg.QueryFlag == "" {
		return tg.ExecIn(t, "", append(tg.Client, sql)...)
	}
	return tg.ExecIn(t, "", append(tg.Client, tg.QueryFlag, sql)...)
}

// BundleDir is where this target's bundle lives: a path inside the container
// for a containerized target, and the per-process host directory for sqlite.
// One constant could not serve both - it was being MkdirAll'd on the host and
// used as a container working directory at the same time.
func (tg Target) BundleDir() string {
	if tg.Dialect == "sqlite" {
		return HostBundleDir
	}
	return ContainerBundleDir
}

// RunFile executes a script file already placed in the target's bundle dir.
// Clients without file flags (like MySQL and SQLite) feed the file via
// stdin redirection, matching generated bulk script instructions.
func (tg Target) RunFile(t testing.TB, name string) {
	t.Helper()
	if tg.FileFlag == "" {
		tg.ExecIn(t, tg.BundleDir(), "sh", "-c",
			strings.Join(tg.Client, " ")+" < "+name)
		return
	}
	tg.ExecIn(t, tg.BundleDir(), append(tg.Client, tg.FileFlag, name)...)
}

// CopyIn places a file into the target's bundle dir. Files are copied with 0o644
// permissions so server processes running under non-root container users
// (such as mssql) can open them directly for bulk ingestion.
func (tg Target) CopyIn(t testing.TB, localPath string) {
	t.Helper()
	dest := filepath.Join(tg.BundleDir(), filepath.Base(localPath))
	if tg.Dialect == "sqlite" {
		data, err := os.ReadFile(localPath)
		if err != nil {
			t.Fatalf("reading %s: %v", localPath, err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			t.Fatalf("writing %s: %v", dest, err)
		}
		return
	}
	if err := tg.Container(t).CopyFileToContainer(
		context.Background(), localPath, dest, 0o644); err != nil {
		t.Fatalf("copying %s to %s: %v", localPath, dest, err)
	}
}

// Setup initializes the database, creates the target table, and prepares an
// empty bundle directory so subsequent test loads start from a known state.
func (tg Target) Setup(t testing.TB) {
	t.Helper()
	if tg.Dialect == "sqlite" {
		_ = os.Remove(SQLiteDBPath)
		_ = os.RemoveAll(HostBundleDir)
		if err := os.MkdirAll(HostBundleDir, 0o755); err != nil {
			t.Fatalf("creating bundle dir: %v", err)
		}
		tg.Query(t, tg.DDL)
		return
	}

	for _, sql := range tg.InitSQL {
		client := make([]string, 0, len(tg.Client))
		for i := 0; i < len(tg.Client); i++ {
			if tg.Client[i] == "-d" {
				i++
				continue
			}
			client = append(client, tg.Client[i])
		}
		tg.ExecIn(t, "", append(client, tg.QueryFlag, sql)...)
	}
	tg.Query(t, tg.DDL)
	tg.ExecIn(t, "", "rm", "-rf", ContainerBundleDir)
	tg.ExecIn(t, "", "mkdir", "-p", ContainerBundleDir)
}

// Loaded retrieves stored rows as hex strings keyed by row ID, allowing
// exact byte-level verification without client string escaping interference.
func (tg Target) Loaded(t testing.TB) map[int]string {
	t.Helper()
	out := tg.Query(t, tg.SelectHex)
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

// Compare checks that every loaded row value matches the expected hex byte-for-byte.
func (tg Target) Compare(t testing.TB, want []string, got map[int]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("loaded %d rows, want %d", len(got), len(want))
	}
	for i, v := range want {
		wantHex := strings.ToLower(tg.Encode(v))
		if got[i] != wantHex {
			t.Errorf("row %d (%q):\n got %s\nwant %s", i, v, got[i], wantHex)
		}
	}
}

// Teardown stops and removes every container started during the test run.
// It is designed for TestMain invocation after m.Run(), avoiding orphan
// containers across test failures.
func Teardown() {
	startedMu.Lock()
	defer startedMu.Unlock()
	for dialect, c := range started {
		if err := c.Terminate(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "terminating %s: %v\n", dialect, err)
		}
	}
	started = map[string]testcontainers.Container{}
	// The sqlite target's database and bundle live on the host, so the
	// container reaper does not cover them.
	_ = os.RemoveAll(hostWorkDir)
}

// Targets returns all four supported database targets, including SQLite,
// so multi-dialect test matrices stay uniform at four rows.
func Targets() []Target {
	return []Target{
		postgresTarget(),
		mysqlTarget(),
		mssqlTarget(),
		sqliteTarget(),
	}
}

// ServerTargets returns the three container-backed database targets, for
// tests that specifically exercise remote database server processes.
func ServerTargets() []Target {
	return []Target{
		postgresTarget(),
		mysqlTarget(),
		mssqlTarget(),
	}
}

// Lookup finds a target by its mockagen dialect name (accepting both sqlite
// and sqlite3), returning false if the dialect is not supported.
func Lookup(dialect string) (Target, bool) {
	for _, tg := range Targets() {
		if tg.Dialect == dialect || (tg.Dialect == "sqlite" && dialect == "sqlite3") {
			return tg, true
		}
	}
	return Target{}, false
}

// IsASCIIOnly detects whether a string contains non-ASCII runes, used to
// filter test suites for targets with known codepage restrictions.
func IsASCIIOnly(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// UTF8Hex encodes a string into lowercase hexadecimal UTF-8 bytes to verify
// against hex queries on UTF-8 database engines.
func UTF8Hex(s string) string { return hex.EncodeToString([]byte(s)) }

// UTF16LEHex encodes a string as lowercase hexadecimal UTF-16LE, matching
// SQL Server's nvarchar storage format including surrogate pairs.
func UTF16LEHex(s string) string {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(units)*2)
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return hex.EncodeToString(b)
}

func postgresTarget() Target {
	return Target{
		Dialect: "postgres",
		Port:    5432,
		image: &ServerImage{
			ref: "postgres:17-alpine",
			env: map[string]string{
				"POSTGRES_PASSWORD": "mockagen",
				"POSTGRES_DB":       "mockagen",
			},
			ready: wait.ForExec([]string{"psql", "-U", "postgres", "-d", "mockagen", "-c", "SELECT 1"}).
				WithPollInterval(time.Second),
		},
		Client:    []string{"psql", "-U", "postgres", "-d", "mockagen", "-v", "ON_ERROR_STOP=1", "-q", "-t", "-A"},
		DDL:       "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id text NOT NULL, v text NOT NULL);",
		FileFlag:  "-f",
		QueryFlag: "-c",
		SelectHex: "SELECT id || ':' || encode(convert_to(v,'UTF8'),'hex') FROM vals ORDER BY id::int",
		Encode:    UTF8Hex,
	}
}

func mysqlTarget() Target {
	return Target{
		Dialect: "mysql",
		Port:    3306,
		image: &ServerImage{
			ref: "mysql:8.4",
			cmd: []string{"--local-infile=1"},
			env: map[string]string{
				"MYSQL_ROOT_PASSWORD": "mockagen",
				"MYSQL_DATABASE":      "mockagen",
			},
			ready: wait.ForExec([]string{
				"mysqladmin", "ping", "-h", "127.0.0.1", "-uroot", "-pmockagen", "--silent",
			}).WithPollInterval(2 * time.Second),
		},
		Client:    []string{"mysql", "--default-character-set=utf8mb4", "--local-infile=1", "-uroot", "-D", "mockagen", "-N", "-B"},
		Env:       []string{"MYSQL_PWD=mockagen"},
		DDL:       "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id text NOT NULL, v text NOT NULL);",
		QueryFlag: "-e",
		SelectHex: "SELECT CONCAT(id, ':', HEX(v)) FROM vals ORDER BY CAST(id AS SIGNED)",
		Encode:    UTF8Hex,
	}
}

func mssqlTarget() Target {
	return Target{
		Dialect: "mssql",
		Port:    1433,
		image: &ServerImage{
			ref: "mcr.microsoft.com/mssql/server:2022-latest",
			env: map[string]string{
				"ACCEPT_EULA":       "Y",
				"MSSQL_SA_PASSWORD": MSSQLPassword,
				"MSSQL_PID":         "Developer",
			},
			ready: wait.ForExec([]string{
				"/opt/mssql-tools18/bin/sqlcmd", "-S", "localhost", "-U", "sa",
				"-P", MSSQLPassword, "-C", "-Q", "SELECT 1",
			}).WithPollInterval(3 * time.Second),
		},
		Client:        []string{"/opt/mssql-tools18/bin/sqlcmd", "-S", "localhost", "-U", "sa", "-P", MSSQLPassword, "-C", "-b", "-d", "mockagen", "-h", "-1", "-w", "65535", "-y", "8000"},
		InitSQL:       []string{"IF DB_ID('mockagen') IS NULL CREATE DATABASE mockagen;"},
		DDL:           "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id nvarchar(50) NOT NULL, v nvarchar(max) NOT NULL);",
		FileFlag:      "-i",
		QueryFlag:     "-Q",
		SelectHex:     "SET NOCOUNT ON; SELECT CAST(id AS VARCHAR(50)) + ':' + CONVERT(VARCHAR(MAX), CONVERT(VARBINARY(MAX), v), 2) FROM vals ORDER BY CAST(id AS INT)",
		Encode:        UTF16LEHex,
		BulkASCIIOnly: true,
	}
}

func sqliteTarget() Target {
	return Target{
		Dialect:   "sqlite",
		Port:      0,
		image:     nil,
		Client:    []string{"sqlite3", "-batch", "-noheader", SQLiteDBPath},
		DDL:       "DROP TABLE IF EXISTS vals; CREATE TABLE vals (id text NOT NULL, v text NOT NULL);",
		FileFlag:  "",
		QueryFlag: "",
		SelectHex: "SELECT id || ':' || lower(hex(v)) FROM vals ORDER BY CAST(id AS INTEGER)",
		Encode:    UTF8Hex,
	}
}
