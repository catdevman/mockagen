package schemaui

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) onSave(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.saveEditing {
		return m.onSaveField(msg)
	}
	switch msg.String() {
	case "tab":
		m.saveField = 1 - m.saveField
		return m, nil
	case "enter":
		return m, m.saveCmd()
	case "e":
		m.saveEditing = true
		m.activeSave().Focus()
		return m, textinputBlink()
	}
	return m, nil
}

func (m Model) onSaveField(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.saveEditing = false
		m.pathInput.Blur()
		m.renderInput.Blur()
		return m, m.saveCmd()
	case "tab":
		m.activeSave().Blur()
		m.saveField = 1 - m.saveField
		m.activeSave().Focus()
		return m, textinputBlink()
	case "esc":
		m.saveEditing = false
		m.pathInput.Blur()
		m.renderInput.Blur()
		return m, nil
	default:
		return m, m.forward(m.activeSave(), msg)
	}
}

func (m *Model) activeSave() *textinput.Model {
	if m.saveField == 1 {
		return &m.renderInput
	}
	return &m.pathInput
}

// openSave moves to the path screen. The default file is the table
// name; with several tables the path is a directory and each file is
// named inside it, which the view says explicitly.
func (m *Model) openSave(from screen) {
	m.saveFrom = from
	m.screen = screenSave
	m.saveEditing = false
	m.saveField = 0
	m.note = ""
	if strings.TrimSpace(m.pathInput.Value()) == "" {
		if m.outDefault != "" {
			m.pathInput.SetValue(m.outDefault)
		} else if len(m.schemas) <= 1 {
			m.pathInput.SetValue(tableFile(m.currentSchema()))
		} else {
			m.pathInput.SetValue(".")
		}
	}
	if strings.TrimSpace(m.renderInput.Value()) == "" && m.renderDefault != "" {
		m.renderInput.SetValue(m.renderDefault)
	}
}

func tableBase(s schema.Schema) string {
	name := s.Name
	if name == "" {
		name = s.Source.Table
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "schema"
	}
	// A slash would write outside the directory the screen promised.
	name = strings.NewReplacer("/", "_", "\\", "_").Replace(name)
	return name
}

func tableFile(s schema.Schema) string {
	return tableBase(s) + ".schema.yaml"
}

// saveCmd writes one schema file per table, and a spec only when a
// render path was set. The model test feeds savedMsg instead of
// running this.
func (m Model) saveCmd() tea.Cmd {
	path := strings.TrimSpace(m.pathInput.Value())
	render := strings.TrimSpace(m.renderInput.Value())
	schemas := make([]schema.Schema, len(m.schemas))
	for i := range m.schemas {
		schemas[i] = cloneSchema(m.schemas[i])
	}
	saveSchema := m.services.SaveSchema
	saveSpec := m.services.SaveSpec
	return func() tea.Msg {
		if saveSchema == nil {
			return savedMsg{err: errors.New("schemaui: no save service")}
		}
		if len(schemas) <= 1 {
			s := schema.Schema{}
			if len(schemas) == 1 {
				s = schemas[0]
			}
			if path == "" {
				path = tableFile(s)
			}
			if err := saveSchema(path, s); err != nil {
				return savedMsg{err: err}
			}
			paths := []string{path}
			if render == "" {
				return savedMsg{paths: paths}
			}
			if saveSpec == nil {
				return savedMsg{paths: paths, err: errors.New("schemaui: no spec service")}
			}
			if err := saveSpec(render, s); err != nil {
				return savedMsg{paths: paths, err: err}
			}
			return savedMsg{paths: append(paths, render)}
		}
		if path == "" {
			path = "."
		}
		var paths []string
		for _, s := range schemas {
			p := filepath.Join(path, tableFile(s))
			if err := saveSchema(p, s); err != nil {
				return savedMsg{paths: paths, err: err}
			}
			paths = append(paths, p)
			if render == "" {
				continue
			}
			if saveSpec == nil {
				return savedMsg{paths: paths, err: errors.New("schemaui: no spec service")}
			}
			spec := filepath.Join(render, tableBase(s)+".spec.yaml")
			if err := saveSpec(spec, s); err != nil {
				return savedMsg{paths: paths, err: err}
			}
			paths = append(paths, spec)
		}
		return savedMsg{paths: paths}
	}
}
