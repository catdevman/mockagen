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

func plainModel(sch schema.Schema, sample importer.Sample, fm importer.FieldMap) Model {
	m := New(Session{
		Schema:  sch,
		Sample:  sample,
		Map:     fm,
		OutPath: "out.map.yaml",
	})
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	r.SetHasDarkBackground(false)
	m.r = r
	m.styles = newStyles(r)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Model)
}

func TestCheckRunsACleanSample(t *testing.T) {
	// Every required column is mapped, so Compile is empty and the
	// check screen is a real Run. "not run" would mean the plan was
	// refused.
	m := plainModel(schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "email", SQLType: "varchar"},
			{Name: "login_count", SQLType: "integer", Nullable: true},
		},
	}, importer.Sample{
		Format: "csv", Delimiter: ",", HasHeader: true, Encoding: "utf-8",
		Fields:  []string{"email", "login_count"},
		Rows:    1,
		Preview: [][]string{{"ada@example.com", "4"}},
	}, importer.FieldMap{
		Name: "users-import",
		Source: importer.SourceSpec{
			Format: "csv", HasHeader: true,
			Fields: []string{"email", "login_count"},
		},
		Fields: []importer.Field{
			{Target: "email", Source: "email"},
			{Target: "login_count", Source: "login_count"},
		},
	})
	if len(m.diags) != 0 {
		t.Fatalf("diags %v", m.diags)
	}
	if !strings.Contains(m.View(), "ada@example.com") {
		t.Fatalf("board =\n%s", m.View())
	}
	m = press(m, "v")
	view := m.View()
	if !strings.Contains(view, "read ") || !strings.Contains(view, "wrote ") {
		t.Fatalf("view =\n%s", view)
	}
	if strings.Contains(view, "not run") {
		t.Fatalf("view =\n%s", view)
	}
}

func TestCheckShowsABadInteger(t *testing.T) {
	// Abort still returns the report. The column and the strconv
	// text are how a person finds the cell; the line is the row.
	m := press(plainModel(schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "login_count", SQLType: "integer", Nullable: true},
		},
	}, importer.Sample{
		Format: "csv", Delimiter: ",", HasHeader: true, Encoding: "utf-8",
		Fields:  []string{"login_count"},
		Rows:    1,
		Preview: [][]string{{"nope"}},
	}, importer.FieldMap{
		Name: "users-import",
		Source: importer.SourceSpec{
			Format: "csv", HasHeader: true, Fields: []string{"login_count"},
		},
		Fields: []importer.Field{
			{Target: "login_count", Source: "login_count"},
		},
	}), "v")
	view := m.View()
	if !strings.Contains(view, "login_count") || !strings.Contains(view, "invalid") {
		t.Fatalf("view =\n%s", view)
	}
	if !strings.Contains(view, "line 1") {
		t.Fatalf("view =\n%s", view)
	}
	if strings.Contains(view, "not run") {
		t.Fatalf("view =\n%s", view)
	}
}

func TestEditorShowsTrimStages(t *testing.T) {
	// Stages, not only the final Apply value. chain is the text
	// after trim, so the spaces in the sample are gone.
	m := press(plainModel(schema.Schema{
		Name:    "people",
		Columns: []schema.Column{{Name: "name", SQLType: "varchar"}},
	}, importer.Sample{
		Format: "csv", Delimiter: ",", HasHeader: true, Encoding: "utf-8",
		Fields:  []string{"name"},
		Rows:    1,
		Preview: [][]string{{" ada "}},
	}, importer.FieldMap{
		Name: "people-import",
		Source: importer.SourceSpec{
			Format: "csv", HasHeader: true, Fields: []string{"name"},
		},
		Fields: []importer.Field{{
			Target:     "name",
			Source:     "name",
			Transforms: []importer.Transform{{Name: "trim"}},
		}},
	}), "t")
	if m.screen != screenEdit {
		t.Fatalf("screen %d", m.screen)
	}
	view := m.View()
	if !strings.Contains(view, "chain") || !strings.Contains(view, "ada") {
		t.Fatalf("view =\n%s", view)
	}
	if !strings.Contains(view, "chain  ada") {
		t.Fatalf("view =\n%s", view)
	}
}

func TestCheckStaysDarkWhenCompileFails(t *testing.T) {
	// A blocker is a diagnostic. Run must not paper over it with a
	// sample that happens to look fine.
	m := press(exampleModel(), "v")
	view := m.View()
	if !strings.Contains(view, "not run") {
		t.Fatalf("view =\n%s", view)
	}
	if !strings.Contains(view, `column "nickname"`) {
		t.Fatalf("view =\n%s", view)
	}
}

func TestBoardPreviewShowsNull(t *testing.T) {
	// null_if is a pipeline result. The board has to say NULL, not
	// the sentinel, or it looks like the word was stored.
	m := plainModel(schema.Schema{
		Name: "users",
		Columns: []schema.Column{
			{Name: "note", SQLType: "text", Nullable: true},
		},
	}, importer.Sample{
		Format: "csv", Delimiter: ",", HasHeader: true, Encoding: "utf-8",
		Fields:  []string{"note"},
		Rows:    1,
		Preview: [][]string{{"none"}},
	}, importer.FieldMap{
		Name: "users-import",
		Source: importer.SourceSpec{
			Format: "csv", HasHeader: true, Fields: []string{"note"},
		},
		Fields: []importer.Field{{
			Target: "note",
			Source: "note",
			NullIf: []string{"none"},
		}},
	})
	if len(m.diags) != 0 {
		t.Fatalf("diags %v", m.diags)
	}
	if !strings.Contains(m.View(), "note  NULL") {
		t.Fatalf("view =\n%s", m.View())
	}
}

func TestBoardPreviewEmptyIsNotNull(t *testing.T) {
	// A pointer to "" is an empty string. Showing NULL would hide
	// the difference the sinks are built to keep.
	m := plainModel(schema.Schema{
		Name:    "users",
		Columns: []schema.Column{{Name: "email", SQLType: "varchar"}},
	}, importer.Sample{
		Format: "csv", Delimiter: ",", HasHeader: true, Encoding: "utf-8",
		Fields:  []string{"email"},
		Rows:    1,
		Preview: [][]string{{""}},
	}, importer.FieldMap{
		Name: "users-import",
		Source: importer.SourceSpec{
			Format: "csv", HasHeader: true, Fields: []string{"email"},
		},
		Fields: []importer.Field{{Target: "email", Source: "email"}},
	})
	if strings.Contains(m.View(), "NULL") {
		t.Fatalf("view =\n%s", m.View())
	}
	if m.report == nil || len(m.report.Samples) != 1 {
		t.Fatalf("report %+v", m.report)
	}
	if m.report.Samples[0].Values[0].Kind != importer.KindText || len(m.report.Samples[0].Bytes(0)) != 0 {
		t.Fatalf("cell kind %d bytes %q", m.report.Samples[0].Values[0].Kind, m.report.Samples[0].Bytes(0))
	}
}
