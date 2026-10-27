package mapui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func exampleModel() Model {
	sch := schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", SQLType: "uuid", Default: "gen_random_uuid()"},
			{Name: "email", SQLType: "varchar(255)", MaxLength: i64(255)},
			{Name: "status", SQLType: "user_status", EnumValues: []string{"active", "suspended", "closed"}},
			{Name: "login_count", SQLType: "integer", Nullable: true},
			// No source matches this name, and it cannot be omitted.
			// The board has to show that rather than hide the column.
			{Name: "nickname", SQLType: "varchar(80)"},
		},
	}
	sample := importer.Sample{
		Format:    "csv",
		Delimiter: ",",
		HasHeader: true,
		Encoding:  "utf-8",
		Fields:    []string{"email", "statuz", "note"},
		Rows:      1,
		Preview:   [][]string{{"ada@example.com", "a", "legacy"}},
	}
	m := New(Session{
		Schema:  sch,
		Sample:  sample,
		Map:     importer.Propose(sch, sample, nil),
		OutPath: "users.map.yaml",
	})
	// Ascii keeps the golden free of color codes. A truecolor
	// terminal would otherwise change the bytes of the same screen.
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	r.SetHasDarkBackground(false)
	m.r = r
	m.styles = newStyles(r)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Model)
}

func i64(n int64) *int64 { return &n }

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = next.(Model)
	}
	return m
}

func TestBoardView(t *testing.T) {
	got := exampleModel().View()
	for _, want := range []string{
		"mockagen map   users-import   → users.map.yaml   2 problems",
		"csv   delim ,   utf-8   1 row   email, statuz, note",
		"sources",
		"targets",
		"> email",
		"status",
		"map!",
		"BLOCKER",
		"omitted",
		`column "status": importer: lookup has no incoming keys`,
		"space link",
		"q quit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q\n%s", want, got)
		}
	}
	for i, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line %d width %d: %s", i, w, line)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("ascii profile still emitted color\n%s", got)
	}
}

func TestOmitRequiredStaysMapped(t *testing.T) {
	// email is NOT NULL with no default. o must not write an omitted
	// entry Compile would reject.
	m := press(exampleModel(), "o")
	if m.omitted("email") {
		t.Fatal("email was omitted")
	}
	if m.field("email") == nil {
		t.Fatal("email lost its field")
	}
	if !strings.Contains(m.note, "cannot be omitted") {
		t.Fatalf("note = %q", m.note)
	}
}

func TestSpaceLinksSource(t *testing.T) {
	// Move onto note, then onto login_count, and link them. The
	// nullable column leaves omitted because it now has a source.
	m := press(exampleModel(), "left", "down", "down", "right", "down", "down", " ")
	f := m.field("login_count")
	if f == nil || f.Source != "note" {
		t.Fatalf("login_count = %+v", f)
	}
	if m.omitted("login_count") {
		t.Fatal("login_count still omitted")
	}
}

func TestLookupEditorSetsIncomingKey(t *testing.T) {
	// The rows are the schema values. "a" is only in the sample, so
	// it waits until space places it. suspended and closed stay
	// warnings because this file never produces them.
	m := press(exampleModel(), "down", "t", "down", "e")
	if m.screen != screenLookup {
		t.Fatalf("screen %d", m.screen)
	}
	if strings.Join(m.lookupChoices, ",") != "active,suspended,closed" {
		t.Fatalf("choices = %q", m.lookupChoices)
	}
	if len(m.lookupPool) != 1 || m.lookupPool[0] != "a" {
		t.Fatalf("pool = %q", m.lookupPool)
	}
	view := m.View()
	if !strings.Contains(view, "suspended") || !strings.Contains(view, "warning") || !strings.Contains(view, "sample not placed  a") {
		t.Fatalf("view =\n%s", view)
	}
	m = press(m, " ", "enter")
	f := m.field("status")
	if f == nil || len(f.Transforms) < 2 {
		t.Fatal(f)
	}
	step := f.Transforms[1]
	if len(step.Accept) != 0 || len(step.Pairs) != 1 || step.Pairs[0].From != "a" || step.Pairs[0].To != "active" {
		t.Fatalf("step = %+v", step)
	}
	if m.diagOn("status") {
		t.Fatalf("diags = %v", m.diags)
	}
	gaps := strings.Join(m.enumGaps(), "\n")
	if !strings.Contains(gaps, "suspended has no incoming key") || !strings.Contains(gaps, "closed has no incoming key") {
		t.Fatalf("gaps = %s", gaps)
	}
	if strings.Contains(gaps, "active has no incoming key") {
		t.Fatalf("active was warned: %s", gaps)
	}
}

func TestLookupTypesACodeTheSampleLacks(t *testing.T) {
	// n attaches a code this CSV does not contain, on the schema row
	// that should store it. The next file can then use s.
	m := press(exampleModel(), "down", "t", "down", "e", " ", "down", "n")
	if m.prompt != promptLookupKey {
		t.Fatalf("prompt %d", m.prompt)
	}
	for _, r := range "s" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	m = press(m, "enter", "enter")
	f := m.field("status")
	if f == nil || len(f.Transforms) < 2 {
		t.Fatal(f)
	}
	var got []string
	for _, p := range f.Transforms[1].Pairs {
		got = append(got, p.From+"→"+p.To)
	}
	if strings.Join(got, ",") != "a→active,s→suspended" {
		t.Fatalf("pairs = %q", got)
	}
}

func TestLookupPlacesExactSchemaValue(t *testing.T) {
	// A sample cell that is already a schema value needs no guess.
	// It lands on that row, and the other schema values stay open.
	m := exampleModel()
	m.sample.Preview = [][]string{{"ada@example.com", "active", "legacy"}}
	m = press(m, "down", "t", "down", "e")
	if len(m.lookupPool) != 0 || len(m.lookupKeys) < 1 || len(m.lookupKeys[0]) != 1 || m.lookupKeys[0][0] != "active" {
		t.Fatalf("pool %q keys %#v", m.lookupPool, m.lookupKeys)
	}
}

func TestEditorCyclesStep(t *testing.T) {
	m := press(exampleModel(), "t")
	if m.screen != screenEdit || m.editTarget != "email" {
		t.Fatalf("screen %d target %s", m.screen, m.editTarget)
	}
	m = press(m, "]")
	f := m.field("email")
	if f == nil || len(f.Transforms) == 0 || f.Transforms[0].Name != "case" {
		t.Fatalf("transforms = %+v", f)
	}
	m = press(m, "esc")
	if m.screen != screenBoard {
		t.Fatalf("screen = %d", m.screen)
	}
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestQuitUnchangedDoesNotAsk(t *testing.T) {
	// Opening the screen and moving the cursor writes nothing. q is
	// the way out, and a question there would fire on every visit.
	m := press(exampleModel(), "down")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := next.(Model)
	if !quits(cmd) || got.confirming {
		t.Fatalf("confirming %v quit %v", got.confirming, quits(cmd))
	}
}

func TestQuitAfterEditAsks(t *testing.T) {
	// Un-omitting login_count is a real edit. q must not throw it
	// away on the first press. y does, n keeps the edit on screen.
	m := press(exampleModel(), "down", "down", "o")
	if m.omitted("login_count") {
		t.Fatal("login_count was not un-omitted")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := next.(Model)
	if quits(cmd) || !got.confirming {
		t.Fatalf("confirming %v quit %v", got.confirming, quits(cmd))
	}
	if !strings.Contains(got.View(), "quit without saving?") {
		t.Fatalf("view =\n%s", got.View())
	}
	// A key that edits the map is ignored while the question is up.
	got = press(got, " ")
	if !got.confirming {
		t.Fatal("space left the question")
	}
	stayed, cmd := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	kept := stayed.(Model)
	if quits(cmd) || kept.confirming || kept.omitted("login_count") {
		t.Fatalf("after n: confirming %v omitted %v", kept.confirming, kept.omitted("login_count"))
	}
	again, cmd := kept.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	asked := again.(Model)
	if !asked.confirming {
		t.Fatal("second q did not ask")
	}
	done, cmd := asked.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	left := done.(Model)
	if !quits(cmd) || left.save {
		t.Fatalf("y quit %v save %v", quits(cmd), left.save)
	}
}

func TestEditorQReturnsWithoutAsking(t *testing.T) {
	// q inside the chain is back, not quit. The unsaved edit is still
	// there, and the question belongs to the quit on the board.
	m := press(exampleModel(), "t", "]")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got := next.(Model)
	if quits(cmd) || got.confirming || got.screen != screenBoard {
		t.Fatalf("screen %d confirming %v quit %v", got.screen, got.confirming, quits(cmd))
	}
}

func TestSaveStaysInTheScreen(t *testing.T) {
	// s writes the file and leaves the screen up. q is the way out,
	// and it does not ask again about the edit that was just saved.
	m := exampleModel()
	m.outPath = filepath.Join(t.TempDir(), "out", "users.map.yaml")
	m = press(m, "down", "down", "o")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := next.(Model)
	if quits(cmd) || cmd == nil {
		t.Fatalf("quit %v cmd %v", quits(cmd), cmd != nil)
	}
	msg := cmd()
	next, cmd = got.Update(msg)
	got = next.(Model)
	if cmd != nil || quits(cmd) {
		t.Fatal("save completion tried to quit")
	}
	if got.omitted("login_count") {
		t.Fatal("save restored the omitted column")
	}
	if _, err := os.Stat(m.outPath); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.note, "saved ") {
		t.Fatalf("note = %q", got.note)
	}
	next, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got = next.(Model)
	if !quits(cmd) || got.confirming {
		t.Fatalf("confirming %v quit %v", got.confirming, quits(cmd))
	}
}

func TestStdoutSavePrintsOnQuit(t *testing.T) {
	// stdout has no file while the screen is up. s only marks the
	// snapshot. q is what tells the command to print it.
	m := exampleModel()
	m.outPath = ""
	m = press(m, "down", "down", "o")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("s did nothing")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		t.Fatal("s quit")
	}
	next, cmd = got.Update(msg)
	got = next.(Model)
	if cmd != nil || got.save || !got.checkpoint {
		t.Fatalf("cmd %v save %v checkpoint %v", cmd != nil, got.save, got.checkpoint)
	}
	if !strings.Contains(got.note, "q prints the map") {
		t.Fatalf("note = %q", got.note)
	}
	next, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	got = next.(Model)
	if !quits(cmd) || !got.save || got.confirming {
		t.Fatalf("quit %v save %v confirming %v", quits(cmd), got.save, got.confirming)
	}
}

func TestCtrlCKeepsStdoutCheckpoint(t *testing.T) {
	// ctrl+c does not ask. An edit after s is dropped, and the
	// snapshot is still what gets printed. There is no other copy.
	m := exampleModel()
	m.outPath = ""
	m = press(m, "down", "down", "o")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("s did nothing")
	}
	next, _ = got.Update(cmd())
	got = next.(Model)
	got = press(got, "o")
	if !got.omitted("login_count") {
		t.Fatal("edit after save did not omit")
	}
	next, cmd = got.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	left := next.(Model)
	if !quits(cmd) || !left.save || left.confirming || left.omitted("login_count") {
		t.Fatalf("quit %v save %v confirming %v omitted %v",
			quits(cmd), left.save, left.confirming, left.omitted("login_count"))
	}
}

func TestQuitAfterCheckpointDropsLaterEdits(t *testing.T) {
	// y after a later edit must not replace the file s already wrote.
	// The screen returns the snapshot, which is what the file holds.
	m := exampleModel()
	m.outPath = filepath.Join(t.TempDir(), "users.map.yaml")
	m = press(m, "down", "down", "o")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("s did nothing")
	}
	next, _ = got.Update(cmd())
	got = next.(Model)
	saved, err := os.ReadFile(m.outPath)
	if err != nil {
		t.Fatal(err)
	}
	got = press(got, "o")
	if !got.omitted("login_count") {
		t.Fatal("second o did not omit")
	}
	next, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	asked := next.(Model)
	if quits(cmd) || !asked.confirming {
		t.Fatal("q did not ask about the edit after the save")
	}
	if !strings.Contains(asked.View(), "s saves") {
		t.Fatalf("view =\n%s", asked.View())
	}
	next, cmd = asked.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	left := next.(Model)
	if !quits(cmd) || left.save || left.omitted("login_count") {
		t.Fatalf("quit %v save %v omitted %v", quits(cmd), left.save, left.omitted("login_count"))
	}
	after, err := os.ReadFile(m.outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(saved) {
		t.Fatal("quit rewrote the checkpoint")
	}
}
