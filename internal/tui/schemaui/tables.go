package schemaui

import (
	"errors"
	"strings"

	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/catdevman/mockagen/pkg/schema/introspect"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) onTables(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch msg.String() {
		case "enter", "esc":
			m.filtering = false
			m.filter.Blur()
			m.clampTable()
			return m, nil
		default:
			cmd := m.forward(&m.filter, msg)
			m.clampTable()
			return m, cmd
		}
	}
	switch msg.String() {
	case "j", "down":
		m.moveTable(1)
	case "k", "up":
		m.moveTable(-1)
	case " ":
		m.toggleSelect()
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinputBlink()
	case "enter":
		chosen := m.chosenTables()
		if len(chosen) == 0 {
			m.note = "nothing selected"
			return m, nil
		}
		m.note = ""
		return m, m.inspectCmd(chosen)
	}
	return m, nil
}

func (m *Model) moveTable(d int) {
	m.tableCursor += d
	m.clampTable()
}

func (m *Model) clampTable() {
	n := len(m.visibleTables())
	if n == 0 {
		m.tableCursor = 0
		return
	}
	if m.tableCursor < 0 {
		m.tableCursor = 0
	}
	if m.tableCursor >= n {
		m.tableCursor = n - 1
	}
}

func (m *Model) toggleSelect() {
	vis := m.visibleTables()
	if len(vis) == 0 {
		return
	}
	m.clampTable()
	k := tableKey(m.tables[vis[m.tableCursor]])
	if m.selected[k] {
		delete(m.selected, k)
	} else {
		m.selected[k] = true
	}
}

func (m Model) chosenTables() []introspect.Table {
	var out []introspect.Table
	for _, t := range m.tables {
		if m.selected[tableKey(t)] {
			out = append(out, t)
		}
	}
	return out
}

// visibleTables applies the filter locally. The catalog was already
// listed; filtering must not issue another query.
func (m Model) visibleTables() []int {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	var idx []int
	for i, t := range m.tables {
		label := strings.ToLower(tableLabel(t))
		if q == "" || strings.Contains(label, q) {
			idx = append(idx, i)
		}
	}
	return idx
}

func tableKey(t introspect.Table) string {
	return t.Schema + "\x00" + t.Name
}

func tableLabel(t introspect.Table) string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// inspectCmd reads each selected table and returns a schemasMsg. One
// schema file is written per table later, on the save screen.
func (m Model) inspectCmd(chosen []introspect.Table) tea.Cmd {
	d := m.target()
	inspect := m.services.Inspect
	tables := append([]introspect.Table(nil), chosen...)
	return func() tea.Msg {
		dialect, dsn, err := d.resolve()
		if err != nil {
			return schemasMsg{err: err}
		}
		if inspect == nil {
			return schemasMsg{err: errors.New("schemaui: no inspect service")}
		}
		ctx, cancel := m.withTimeout()
		defer cancel()
		out := make([]schema.Schema, 0, len(tables))
		for _, t := range tables {
			s, err := inspect(ctx, dialect, dsn, t.Schema, t.Name)
			if err != nil {
				return schemasMsg{err: err}
			}
			out = append(out, s)
		}
		return schemasMsg{schemas: out}
	}
}
