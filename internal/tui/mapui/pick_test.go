package mapui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func openPick(t *testing.T, sess Session) Model {
	t.Helper()
	m := New(sess)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	r.SetHasDarkBackground(false)
	m.r = r
	m.styles = newStyles(r)
	m.picker.Styles = filepicker.DefaultStylesWithRenderer(r)
	m.picker.Styles.EmptyDirectory = m.styles.faint.SetString("empty directory")
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("picker did not read the directory")
	}
	next, cmd = m.Update(cmd())
	m = next.(Model)
	if cmd != nil {
		t.Fatalf("directory read returned %T", cmd())
	}
	return m
}

func enter(m Model) (Model, tea.Msg) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	got := next.(Model)
	if cmd == nil {
		return got, nil
	}
	return got, cmd()
}

func apply(m Model, msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func TestPickerRejectsANonSchema(t *testing.T) {
	// A bad pick has to stay on the picker. Exiting would throw away
	// the only chance to choose a different file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := openPick(t, Session{Ask: &Ask{Dir: dir, NeedSchema: true, SampleN: 1}})
	m, msg := enter(m)
	got, ok := msg.(pickedSchemaMsg)
	if !ok || got.err == nil {
		t.Fatalf("msg = %#v", msg)
	}
	m, cmd := apply(m, msg)
	if cmd != nil || m.screen != screenPick || m.busy {
		t.Fatalf("screen %d busy %v cmd %v", m.screen, m.busy, cmd != nil)
	}
	if !strings.Contains(m.note, "unsupported file format") {
		t.Fatalf("note = %q", m.note)
	}
}

func TestPickerCancelDoesNotAsk(t *testing.T) {
	// q on the picker is cancel. There is no map yet, so the board's
	// quit question would be asking about nothing.
	dir := t.TempDir()
	m := openPick(t, Session{Ask: &Ask{Dir: dir, NeedSchema: true}})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := next.(Model)
	if !quits(cmd) || got.confirming || got.save {
		t.Fatalf("quit %v confirming %v save %v", quits(cmd), got.confirming, got.save)
	}
}

func TestPickerLoadsFilesAndSkipsMap(t *testing.T) {
	// schema, then data, then a map the user does not have. s is how
	// that last step is declined, and the board is what comes next.
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "users.schema.yaml")
	body := []byte("version: mockagen/v1\nkind: Schema\nname: users\ncolumns:\n  - name: email\n    sql_type: varchar(255)\n  - name: note\n    sql_type: text\n    nullable: true\n")
	if err := os.WriteFile(schemaPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.csv"), []byte("email,note\nada,legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := openPick(t, Session{
		OutPath: filepath.Join(dir, "out", "users.map.yaml"),
		Ask: &Ask{
			Dir:        dir,
			SampleN:    1,
			NeedSchema: true,
			NeedData:   true,
			NeedMap:    true,
		},
	})
	view := m.View()
	if !strings.Contains(view, "schema file") || !strings.Contains(view, "users.schema.yaml") || !strings.Contains(view, "q cancels") {
		t.Fatalf("view =\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line width %d: %s", w, line)
		}
	}
	m = press(m, "down")
	m, msg := enter(m)
	if _, ok := msg.(pickedSchemaMsg); !ok {
		t.Fatalf("msg = %#v", msg)
	}
	m, cmd := apply(m, msg)
	if m.pickTitle() != "data file" {
		t.Fatalf("title %s err note %q", m.pickTitle(), m.note)
	}
	if cmd == nil {
		t.Fatal("data picker did not read the directory")
	}
	m, cmd = apply(m, cmd())
	if cmd != nil {
		t.Fatal("data directory read continued")
	}
	m, msg = enter(m)
	if _, ok := msg.(pickedDataMsg); !ok {
		t.Fatalf("msg = %#v", msg)
	}
	m, cmd = apply(m, msg)
	if m.pickTitle() != "existing field map" || !strings.Contains(m.View(), "s skips") {
		t.Fatalf("title %s view =\n%s", m.pickTitle(), m.View())
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := next.(Model)
	if cmd != nil || got.screen != screenBoard {
		t.Fatalf("screen %d cmd %v note %q", got.screen, cmd != nil, got.note)
	}
	if got.fm.Schema != "../users.schema.yaml" {
		t.Fatalf("schema ref %q", got.fm.Schema)
	}
	if got.field("email") == nil || got.field("email").Source != "email" {
		t.Fatalf("email = %+v", got.field("email"))
	}
}

func TestPickerSkipsMapWhenNothingElseIsMissing(t *testing.T) {
	// -schema and -data were flags. The screen only asks for -map,
	// and skipping it proposes from those two alone.
	dir := t.TempDir()
	sch := schema.Schema{
		Name:    "users",
		Columns: []schema.Column{{Name: "email", SQLType: "varchar(255)"}},
	}
	sample := importer.Sample{
		Format: "csv", HasHeader: true, Encoding: "utf-8",
		Fields: []string{"email"}, Rows: 1,
		Preview: [][]string{{"ada"}},
	}
	m := openPick(t, Session{
		Schema: sch,
		Sample: sample,
		Ask: &Ask{
			Dir:        dir,
			SchemaPath: filepath.Join(dir, "users.schema.yaml"),
			DataPath:   filepath.Join(dir, "legacy.csv"),
			NeedMap:    true,
		},
	})
	if !strings.Contains(m.View(), "s skips") {
		t.Fatalf("view =\n%s", m.View())
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := next.(Model)
	if cmd != nil || got.screen != screenBoard || got.haveBase {
		t.Fatalf("screen %d haveBase %v", got.screen, got.haveBase)
	}
	if got.field("email") == nil {
		t.Fatal("skip did not propose email")
	}
}

func TestPickedMapHeaderMismatchStays(t *testing.T) {
	// A map chosen here can be the wrong file. The screen says so
	// and waits, instead of quitting with that file half applied.
	dir := t.TempDir()
	mapYAML := []byte("version: mockagen/v1\nkind: FieldMap\nname: old\nschema: users.schema.yaml\nsource:\n    format: csv\n    has_header: true\n    delimiter: ','\n    encoding: utf-8\n    fields: [title, email]\nunmapped:\n    source: ignore\non_error: row\n")
	if err := os.WriteFile(filepath.Join(dir, "old.map.yaml"), mapYAML, 0o644); err != nil {
		t.Fatal(err)
	}
	sample := importer.Sample{HasHeader: true, Fields: []string{"email", "note"}}
	m := openPick(t, Session{
		Sample: sample,
		Ask:    &Ask{Dir: dir, NeedMap: true, SchemaPath: "users.schema.yaml"},
	})
	m, msg := enter(m)
	got, ok := msg.(pickedMapMsg)
	if !ok || got.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	m, cmd := apply(m, msg)
	if quits(cmd) || m.screen != screenPick || !strings.Contains(m.note, "header does not match") {
		t.Fatalf("screen %d quit %v note %q", m.screen, quits(cmd), m.note)
	}
}

func TestFlagMapDriftQuitsForTheCommand(t *testing.T) {
	// -map was typed on the command line. A data file that does not
	// match it is the command's existing mismatch path, not a prompt.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "legacy.csv"), []byte("email,note\nada,x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := importer.FieldMap{
		Name: "old",
		Source: importer.SourceSpec{
			HasHeader: true,
			Fields:    []string{"title"},
		},
	}
	m := openPick(t, Session{
		Schema: schema.Schema{Name: "users"},
		Ask: &Ask{
			Dir:        dir,
			SchemaPath: filepath.Join(dir, "users.schema.yaml"),
			NeedData:   true,
			SampleN:    1,
			Base:       &base,
			MapFromCLI: true,
		},
	})
	m, msg := enter(m)
	if _, ok := msg.(pickedDataMsg); !ok {
		t.Fatalf("msg = %#v", msg)
	}
	m, cmd := apply(m, msg)
	if !quits(cmd) || !m.drift || len(m.fm.Source.Fields) != 1 || m.fm.Source.Fields[0] != "title" {
		t.Fatalf("quit %v drift %v fields %v", quits(cmd), m.drift, m.fm.Source.Fields)
	}
}
