package schemaui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/catdevman/mockagen/pkg/schema/introspect"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

func boot(m Model) Model {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	r.SetHasDarkBackground(false)
	m.bindRenderer(r)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Model)
}

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		next, _ := m.Update(key(k))
		m = next.(Model)
	}
	return m
}

func feed(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func typeText(m Model, s string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return next.(Model)
}

func assertNoANSI(t *testing.T, view string) {
	t.Helper()
	if strings.Contains(view, "\x1b") {
		t.Fatalf("view contains an escape\n%s", view)
	}
}

func TestConnectView(t *testing.T) {
	const secret = "postgres://app:supersecret@localhost/hidden"
	m := boot(New(Session{
		EnvDSN:  secret,
		EnvName: "MOCKAGEN_DSN",
		Connections: []Connection{{
			Name:    "local",
			Dialect: "postgres",
			DSN:     "postgres://app@localhost/db",
		}},
	}))
	got := m.View()
	for _, want := range []string{
		"mockagen schema",
		"postgres",
		"mysql",
		"mssql",
		"sqlite",
		connectHelp,
		"MOCKAGEN_DSN",
		"local",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "supersecret") || strings.Contains(got, "hidden") {
		t.Fatalf("view leaked the env dsn\n%s", got)
	}
	assertNoANSI(t, got)
}

func TestTablesMessageShowsCount(t *testing.T) {
	n := int64(42)
	m := boot(New(Session{}))
	m = feed(m, tablesMsg{tables: []introspect.Table{
		{Schema: "public", Name: "users"},
		{Schema: "public", Name: "orders", ApproxRows: &n},
	}})
	if m.screen != screenTables {
		t.Fatalf("screen = %d", m.screen)
	}
	got := m.View()
	for _, want := range []string{"users", "—", "orders", "42", "mockagen schema"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q\n%s", want, got)
		}
	}
	assertNoANSI(t, got)
}

func TestSelectThenInspect(t *testing.T) {
	m := boot(New(Session{}))
	m = feed(m, tablesMsg{tables: []introspect.Table{
		{Schema: "public", Name: "users"},
	}})
	next, cmd := m.Update(key("enter"))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("enter with nothing selected returned a command")
	}
	if m.screen != screenTables {
		t.Fatal("left the tables screen")
	}
	if !strings.Contains(m.note, "nothing selected") {
		t.Fatalf("note = %q", m.note)
	}
	m = press(m, " ")
	_, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter with a selection returned no command")
	}
	m = feed(m, schemasMsg{schemas: []schema.Schema{unresolvedSchema()}})
	if m.screen != screenColumns {
		t.Fatalf("screen = %d", m.screen)
	}
	got := m.View()
	if !strings.Contains(got, "UNRESOLVED") {
		t.Fatalf("view missing UNRESOLVED\n%s", got)
	}
	if !strings.Contains(got, "no sample") {
		t.Fatalf("view missing no sample\n%s", got)
	}
	assertNoANSI(t, got)
}

func unresolvedSchema() schema.Schema {
	return schema.Schema{
		Name: "users",
		Source: schema.Source{
			Dialect: "postgres",
			Table:   "public.users",
		},
		Columns: []schema.Column{
			{Name: "note", SQLType: "text"},
		},
	}
}

func TestPickCustomListThenSentenceClearsValues(t *testing.T) {
	m := boot(New(Session{}))
	m = feed(m, schemasMsg{schemas: []schema.Schema{unresolvedSchema()}})
	m = press(m, "e")
	if !m.picking {
		t.Fatal("picker did not open")
	}
	m = movePick(m, "Custom List")
	if !strings.Contains(m.View(), "Custom List") {
		t.Fatalf("picker view missing the type\n%s", m.View())
	}
	m = press(m, "enter")
	if !strings.Contains(m.View(), "values") {
		t.Fatalf("prompt view = %s", m.View())
	}
	m = typeText(m, "red, blue")
	m = press(m, "enter")
	col := m.schemas[0].Columns[0]
	if col.Type != "Custom List" || col.InferredBy != "user" {
		t.Fatalf("column = %+v", col)
	}
	if len(col.Values) != 2 || col.Values[0] != "red" || col.Values[1] != "blue" {
		t.Fatalf("values = %v", col.Values)
	}
	if !strings.Contains(m.View(), "Custom List") {
		t.Fatalf("view missing type\n%s", m.View())
	}
	m = press(m, "e")
	m = movePick(m, "Sentence")
	m = press(m, "enter")
	col = m.schemas[0].Columns[0]
	if col.Type != "Sentence" || col.InferredBy != "user" {
		t.Fatalf("column = %+v", col)
	}
	if len(col.Values) != 0 || col.Min != "" || col.Max != "" || col.Format != "" {
		t.Fatalf("fields not cleared: %+v", col)
	}
	if !strings.Contains(m.View(), "Sentence") {
		t.Fatalf("view missing Sentence\n%s", m.View())
	}
	assertNoANSI(t, m.View())
}

func movePick(m Model, name string) Model {
	idx := -1
	for i, typ := range m.pick {
		if typ.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		panic("missing type " + name)
	}
	for i := 0; i < idx; i++ {
		m = press(m, "j")
	}
	return m
}

func TestPreviewMessage(t *testing.T) {
	m := boot(New(Session{}))
	m = feed(m, schemasMsg{schemas: []schema.Schema{{
		Name: "users",
		Source: schema.Source{
			Table: "public.users",
		},
		Columns: []schema.Column{
			{Name: "email", SQLType: "text", Type: "Sentence", InferredBy: "name:email"},
			{Name: "note", SQLType: "text"},
		},
	}}})
	m = feed(m, sampleMsg{
		table:  "public.users",
		values: map[string]string{"email": "ada@example.com"},
	})
	if m.schemas[0].Columns[0].Type != "Sentence" {
		t.Fatal("sample changed the type")
	}
	if !strings.Contains(m.View(), "ada@example.com") {
		t.Fatalf("sample missing\n%s", m.View())
	}
	next, cmd := m.Update(key("p"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("preview returned no command")
	}
	if m.screen != screenPreview {
		t.Fatalf("screen = %d", m.screen)
	}
	m = feed(m, previewMsg{rows: [][]string{{"ada@example.com", ""}}})
	got := m.View()
	for _, want := range []string{"preview", "ada@example.com", "UNRESOLVED"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q\n%s", want, got)
		}
	}
	assertNoANSI(t, got)
}

func TestSavedMessage(t *testing.T) {
	m := boot(New(Session{}))
	m = feed(m, schemasMsg{schemas: []schema.Schema{unresolvedSchema()}})
	m = press(m, "s")
	if m.screen != screenSave {
		t.Fatalf("screen = %d", m.screen)
	}
	got := m.View()
	if !strings.Contains(got, "users.schema.yaml") {
		t.Fatalf("view missing path\n%s", got)
	}
	if !strings.Contains(got, "save") {
		t.Fatalf("view missing save\n%s", got)
	}
	assertNoANSI(t, got)
	m = feed(m, savedMsg{paths: []string{"users.schema.yaml"}})
	if !m.saved {
		t.Fatal("state was not saved")
	}
	if !strings.Contains(m.View(), "users.schema.yaml") && !strings.Contains(m.note, "users.schema.yaml") {
		t.Fatalf("path missing from view %q note %q", m.View(), m.note)
	}
}

func TestQuit(t *testing.T) {
	m := boot(New(Session{}))
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("q produced %T", msg)
	}
	_, cmd = m.Update(key("ctrl+c"))
	msg = cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c produced %T", msg)
	}
}

func TestEnvDialDoesNotStoreTheValue(t *testing.T) {
	const secret = "postgres://app:secret@localhost/hidden"
	t.Setenv("MOCKAGEN_DSN", secret)
	var gotDSN string
	m := boot(New(Session{
		EnvName: "MOCKAGEN_DSN",
		EnvDSN:  secret,
		Services: Services{
			Tables: func(_ context.Context, dialect, dsn string) ([]introspect.Table, error) {
				gotDSN = dsn
				if dialect != "postgres" {
					t.Errorf("dialect = %q", dialect)
				}
				return []introspect.Table{{Name: "users"}}, nil
			},
		},
	}))
	if strings.Contains(m.View(), "secret") || strings.Contains(m.View(), "hidden") {
		t.Fatalf("view leaked the env dsn\n%s", m.View())
	}
	next, cmd := m.Update(key("e"))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("e returned no command")
	}
	msg := cmd()
	got, ok := msg.(tablesMsg)
	if !ok {
		t.Fatalf("message %T", msg)
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if gotDSN != secret {
		t.Fatalf("dialled %q", gotDSN)
	}
	if strings.Contains(m.dsn, "secret") || strings.Contains(m.View(), "secret") {
		t.Fatal("model stored or printed the env dsn")
	}
}

func TestTablesCommandMessage(t *testing.T) {
	var gotDialect, gotDSN string
	m := New(Session{
		Services: Services{
			Tables: func(ctx context.Context, dialect, dsn string) ([]introspect.Table, error) {
				if ctx == nil {
					t.Fatal("nil context")
				}
				gotDialect = dialect
				gotDSN = dsn
				return []introspect.Table{{Schema: "public", Name: "users"}}, nil
			},
		},
	})
	m.dialect = "postgres"
	m.dsn = "postgres://app@localhost/db"
	cmd := m.tablesCmd()
	if cmd == nil {
		t.Fatal("no command")
	}
	msg := cmd()
	got, ok := msg.(tablesMsg)
	if !ok {
		t.Fatalf("message %T", msg)
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.tables) != 1 || got.tables[0].Name != "users" {
		t.Fatalf("tables = %+v", got.tables)
	}
	if gotDialect != "postgres" || gotDSN != "postgres://app@localhost/db" {
		t.Fatalf("dialled %s %s", gotDialect, gotDSN)
	}
}

func TestSaveConnectionsOmitsPassword(t *testing.T) {
	const secret = "s3cr3t-only-env"
	t.Setenv("MOCKAGEN_DB_PASSWORD", secret)
	path := filepath.Join(t.TempDir(), "mockagen", "connections.yaml")
	cs := []Connection{{
		Name:        "local",
		Dialect:     "postgres",
		DSN:         "postgres://app@localhost:5432/app",
		PasswordEnv: "MOCKAGEN_DB_PASSWORD",
	}}
	if err := SaveConnections(path, cs); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "password_env") || !strings.Contains(text, "MOCKAGEN_DB_PASSWORD") {
		t.Fatalf("file = %s", text)
	}
	if strings.Contains(text, secret) {
		t.Fatalf("password leaked into %s", text)
	}
	loaded, err := LoadConnections(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].PasswordEnv != "MOCKAGEN_DB_PASSWORD" {
		t.Fatalf("loaded = %+v", loaded)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	body := "connections:\n  - name: local\n    dialect: postgres\n    dsn: postgres://app@localhost/db\n    password: " + secret + "\n"
	if err := os.WriteFile(bad, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConnections(bad); err == nil {
		t.Fatal("password key loaded")
	}
}

func TestHasPassword(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{dsn: "postgres://app:secret@localhost/db", want: true},
		{dsn: "postgres://app@localhost/db", want: false},
		{dsn: "host=localhost password=secret", want: true},
		{dsn: "host=localhost Password=secret user=app", want: true},
		{dsn: "host=localhost user=app", want: false},
		{dsn: "app:secret@tcp(localhost:3306)/db", want: true},
		{dsn: "app@tcp(localhost:3306)/db", want: false},
	}
	for _, tc := range cases {
		if got := hasPassword(tc.dsn); got != tc.want {
			t.Errorf("hasPassword(%q) = %v, want %v", tc.dsn, got, tc.want)
		}
	}
}

func TestSaveRefusesPasswordDSN(t *testing.T) {
	called := false
	m := boot(New(Session{
		Services: Services{
			SaveConnections: func(string, []Connection) error {
				called = true
				return nil
			},
		},
	}))
	m.dsnInput.SetValue("postgres://app:secret@localhost/db")
	m = press(m, "n")
	if called {
		t.Fatal("save wrote a dsn that already has a password")
	}
	if !strings.Contains(m.note, "not saved") {
		t.Fatalf("note = %q", m.note)
	}
}

func TestSplicePassword(t *testing.T) {
	cases := []struct {
		name    string
		dsn     string
		pw      string
		want    string
		wantErr string
	}{
		{
			name: "url",
			dsn:  "postgres://app@localhost:5432/app",
			pw:   "secret",
			want: "postgres://app:secret@localhost:5432/app",
		},
		{
			name: "url query",
			dsn:  "postgres://app@localhost/db?sslmode=disable",
			pw:   "secret",
			want: "postgres://app:secret@localhost/db?sslmode=disable",
		},
		{
			name:    "url no user",
			dsn:     "postgres://localhost/db",
			pw:      "secret",
			wantErr: "no user",
		},
		{
			name: "keyword",
			dsn:  "host=localhost user=postgres dbname=app",
			pw:   "secret",
			want: "host=localhost user=postgres dbname=app password=secret",
		},
		{
			name: "keyword already",
			dsn:  "host=localhost password=keep user=app",
			pw:   "secret",
			want: "host=localhost password=keep user=app",
		},
		{
			name: "mysql",
			dsn:  "app@tcp(localhost:3306)/db",
			pw:   "secret",
			want: "app:secret@tcp(localhost:3306)/db",
		},
		{
			name: "mysql has colon",
			dsn:  "app:old@tcp(localhost:3306)/db",
			pw:   "secret",
			want: "app:old@tcp(localhost:3306)/db",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splicePassword(tc.dsn, tc.pw)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), tc.pw) {
					t.Fatalf("error contains the password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewRows(t *testing.T) {
	t.Run("empty types do not call gen", func(t *testing.T) {
		called := false
		s := schema.Schema{Columns: []schema.Column{
			{Name: "note", SQLType: "text"},
			{Name: "bio", SQLType: "text"},
		}}
		rows, err := previewRows(s, 1, func(mockagen.MockagenConfig) ([][]string, error) {
			called = true
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if called {
			t.Fatal("gen was called")
		}
		if len(rows) != 1 || len(rows[0]) != 2 || rows[0][0] != "" || rows[0][1] != "" {
			t.Fatalf("rows = %#v", rows)
		}
	})
	t.Run("mixed schema passes only the resolved column", func(t *testing.T) {
		var got mockagen.MockagenConfig
		s := schema.Schema{
			Name: "users",
			Columns: []schema.Column{
				{Name: "email", SQLType: "text", Type: "Sentence"},
				{Name: "note", SQLType: "text"},
			},
		}
		rows, err := previewRows(s, 1, func(cfg mockagen.MockagenConfig) ([][]string, error) {
			got = cfg
			return [][]string{{"hello"}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Columns) != 1 || got.Columns[0].Name != "email" {
			t.Fatalf("gen columns = %+v", got.Columns)
		}
		if got.NumberOfRecords != 1 {
			t.Fatalf("NumberOfRecords = %d", got.NumberOfRecords)
		}
		if len(rows) != 1 || len(rows[0]) != 2 || rows[0][0] != "hello" || rows[0][1] != "" {
			t.Fatalf("rows = %#v", rows)
		}
	})
}

func TestMultiTableSaveNote(t *testing.T) {
	m := boot(New(Session{}))
	m = feed(m, schemasMsg{schemas: []schema.Schema{
		{Name: "users", Columns: []schema.Column{{Name: "id", SQLType: "integer"}}},
		{Name: "orders", Columns: []schema.Column{{Name: "id", SQLType: "integer"}}},
	}})
	m = press(m, "s")
	got := m.View()
	if !strings.Contains(got, multiSaveNote) {
		t.Fatalf("view missing multi-table note\n%s", got)
	}
	assertNoANSI(t, got)
}

func TestEscCancelsPicker(t *testing.T) {
	m := boot(New(Session{}))
	m = feed(m, schemasMsg{schemas: []schema.Schema{unresolvedSchema()}})
	m = press(m, "e", "esc")
	if m.picking {
		t.Fatal("picker still open")
	}
	if m.schemas[0].Columns[0].Type != "" || m.schemas[0].Columns[0].InferredBy != "" {
		t.Fatalf("esc wrote %+v", m.schemas[0].Columns[0])
	}
}
