package schemaui

import (
	"errors"

	"github.com/catdevman/mockagen/pkg/mockagen"
	"github.com/catdevman/mockagen/pkg/schema"
	tea "github.com/charmbracelet/bubbletea"
)

// onPreview leaves generation to previewCmd. s opens save from here so
// a peek does not have to go back through the column list.
func (m Model) onPreview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s":
		m.openSave(screenPreview)
	case "p":
		m.preview = nil
		return m, m.previewCmd()
	}
	return m, nil
}

// previewCmd asks the injected service for a few rows. When that service
// is missing, previewRows still runs so an unresolved schema returns
// blanks instead of calling a generator that would invent strings.
func (m Model) previewCmd() tea.Cmd {
	s := cloneSchema(m.currentSchema())
	preview := m.services.Preview
	return func() tea.Msg {
		var (
			rows [][]string
			err  error
		)
		if preview != nil {
			rows, err = preview(s, previewCap)
		} else {
			rows, err = previewRows(s, previewCap, nil)
		}
		return previewMsg{rows: rows, err: err}
	}
}

// previewRows builds n sample rows. Columns with an empty type are
// dropped before Render and come back blank: generateFakes would
// otherwise fill them with a random string and the preview would look
// resolved. The view prints UNRESOLVED for those cells itself. n is
// written onto NumberOfRecords so the generator does not use Render's
// default of 1000.
func previewRows(s schema.Schema, n int, gen func(mockagen.MockagenConfig) ([][]string, error)) ([][]string, error) {
	if n < 0 {
		n = 0
	}
	resolved := s
	resolved.Columns = nil
	for _, c := range s.Columns {
		if c.Type != "" {
			resolved.Columns = append(resolved.Columns, c)
		}
	}
	if len(resolved.Columns) == 0 {
		rows := make([][]string, n)
		for i := range rows {
			rows[i] = make([]string, len(s.Columns))
		}
		return rows, nil
	}
	cfg, err := schema.Render(resolved, schema.RenderOptions{})
	if err != nil {
		return nil, err
	}
	cfg.NumberOfRecords = n
	if gen == nil {
		return nil, errors.New("schemaui: no preview generator")
	}
	cells, err := gen(cfg)
	if err != nil {
		return nil, err
	}
	pos := make(map[string]int, len(s.Columns))
	for i, c := range s.Columns {
		pos[c.Name] = i
	}
	rows := make([][]string, len(cells))
	for r, cell := range cells {
		row := make([]string, len(s.Columns))
		for i, col := range cfg.Columns {
			if i >= len(cell) {
				break
			}
			if p, ok := pos[col.Name]; ok {
				row[p] = cell[i]
			}
		}
		rows[r] = row
	}
	return rows, nil
}

// PreviewRows is previewRows for the schema command. generateFakes
// stays in package main; the command passes it as gen.
func PreviewRows(s schema.Schema, n int, gen func(mockagen.MockagenConfig) ([][]string, error)) ([][]string, error) {
	return previewRows(s, n, gen)
}

// cloneSchema copies the column slice. The preview command runs later,
// and a type edit must not change the schema that command captured.
func cloneSchema(s schema.Schema) schema.Schema {
	s.Columns = append([]schema.Column(nil), s.Columns...)
	return s
}
