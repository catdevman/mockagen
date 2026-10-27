package mapui

import (
	"io"
	"strings"
	"testing"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func objectModel() Model {
	sch := schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "email", SQLType: "varchar(255)"},
			{Name: "profile", SQLType: "jsonb", Nullable: true},
			{Name: "tags", SQLType: "jsonb[]", Nullable: true},
		},
	}
	sample := importer.Sample{
		Format:    "csv",
		Delimiter: ",",
		HasHeader: true,
		Encoding:  "utf-8",
		Fields:    []string{"city", "email", "tags", "blob"},
		Rows:      1,
		Preview: [][]string{{
			"Austin",
			"ada@example.com",
			"x",
			`{"city":"Austin","email":"a@b.c"}`,
		}},
	}
	m := New(Session{
		Schema:  sch,
		Sample:  sample,
		Map:     importer.Propose(sch, sample, nil),
		OutPath: "users.map.yaml",
	})
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	r.SetHasDarkBackground(false)
	m.r = r
	m.styles = newStyles(r)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Model)
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	return m
}

func TestObjectEditorHintsAreNotSaved(t *testing.T) {
	m := objectModel()
	if m.screen != screenBoard {
		t.Fatalf("screen %d", m.screen)
	}
	// email is not a document. t still opens the chain editor.
	m = press(m, "t")
	if m.screen != screenEdit || m.editTarget != "email" {
		t.Fatalf("screen %d target %s", m.screen, m.editTarget)
	}
	m = press(m, "esc", "down", "t")
	if m.screen != screenObject || m.editTarget != "profile" {
		t.Fatalf("screen %d target %s", m.screen, m.editTarget)
	}
	view := m.View()
	if !strings.Contains(view, "hints  city, email") {
		t.Fatalf("view =\n%s", view)
	}
	if f := m.field("profile"); f != nil && f.Object != nil {
		t.Fatalf("opening wrote %#v", f.Object)
	}
	// jsonb[] is an array. It does not get this editor.
	m = press(m, "esc", "down", "t")
	if m.screen != screenEdit || m.editTarget != "tags" {
		t.Fatalf("array screen %d target %s", m.screen, m.editTarget)
	}
}

func TestObjectEditorBindsAndConstant(t *testing.T) {
	m := press(objectModel(), "down", "t")
	m = press(m, "a")
	m = typeText(m, "city")
	m = press(m, "enter")
	f := m.field("profile")
	if f == nil || f.Object == nil || len(f.Object.Keys) != 1 || f.Object.Keys[0].Key != "city" {
		t.Fatalf("field = %+v", f)
	}
	if f.Source != "" {
		t.Fatalf("source still set: %+v", f)
	}
	// The board cursor sits on email. h moves it to city, which is
	// the source this key should read.
	m = press(m, "h", " ")
	f = m.field("profile")
	if f.Object.Keys[0].Source != "city" {
		t.Fatalf("bound = %+v", f.Object.Keys[0])
	}
	m = press(m, "a")
	m = typeText(m, "status")
	m = press(m, "enter", "c")
	m = typeText(m, "active")
	m = press(m, "enter")
	f = m.field("profile")
	if len(f.Object.Keys) != 2 || f.Object.Keys[1].Constant == nil || *f.Object.Keys[1].Constant != "active" {
		t.Fatalf("keys = %+v", f.Object.Keys)
	}
	if f.Object.Keys[1].Source != "" {
		t.Fatalf("constant kept a source: %+v", f.Object.Keys[1])
	}
	m = press(m, "a")
	m = typeText(m, "city")
	m = press(m, "enter")
	f = m.field("profile")
	if len(f.Object.Keys) != 2 || !strings.Contains(m.note, "already a key") {
		t.Fatalf("dup keys %d note %q", len(f.Object.Keys), m.note)
	}
	m = press(m, "d", "d")
	f = m.field("profile")
	if f == nil || f.Object != nil {
		t.Fatalf("after drop %+v", f)
	}
}
