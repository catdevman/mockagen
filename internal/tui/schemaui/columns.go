package schemaui

import (
	"strings"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/mockagen/types"
	"github.com/catdevman/mockagen/pkg/schema"
	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) onColumns(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		if len(m.schemas) > 1 {
			m.onStrip = !m.onStrip
		}
		return m, nil
	case "[":
		return m.shiftTable(-1)
	case "]":
		return m.shiftTable(1)
	case "j", "down":
		if m.onStrip {
			return m.shiftTable(1)
		}
		m.colIdx++
		m.clampCol()
	case "k", "up":
		if m.onStrip {
			return m.shiftTable(-1)
		}
		m.colIdx--
		m.clampCol()
	case "e":
		m.openPicker()
	case "p":
		m.screen = screenPreview
		m.preview = nil
		m.note = ""
		return m, m.previewCmd()
	case "s":
		m.openSave(screenColumns)
	}
	return m, nil
}

func (m Model) shiftTable(d int) (tea.Model, tea.Cmd) {
	n := len(m.schemas)
	if n == 0 {
		return m, nil
	}
	m.schemaIdx = (m.schemaIdx + d + n) % n
	m.clampCol()
	m.clearPick()
	key := sampleKey(m.currentSchema())
	if !m.sampleOff && m.samples[key] == nil && m.sampleErr[key] == nil {
		return m, m.sampleCmd()
	}
	return m, nil
}

func (m *Model) clampCol() {
	s := m.currentSchema()
	n := len(s.Columns)
	if n == 0 {
		m.colIdx = 0
		return
	}
	if m.colIdx < 0 {
		m.colIdx = 0
	}
	if m.colIdx >= n {
		m.colIdx = n - 1
	}
}

func (m Model) currentSchema() schema.Schema {
	if m.schemaIdx < 0 || m.schemaIdx >= len(m.schemas) {
		return schema.Schema{}
	}
	return m.schemas[m.schemaIdx]
}

func (m *Model) openPicker() {
	s := m.currentSchema()
	if m.colIdx < 0 || m.colIdx >= len(s.Columns) {
		m.note = "no column"
		return
	}
	c := s.Columns[m.colIdx]
	// Affinity comes from the importer so this list cannot grow a
	// second spelling of "integer". The rest of the catalog follows,
	// because a misclassified column would otherwise be unfixable.
	m.pick = candidates(importer.Affinity(c.SQLType, c.NumericScale))
	m.pickIdx = 0
	m.picking = true
	m.note = ""
}

func candidates(aff types.SQLAffinity) []types.Type {
	first := types.Candidates(aff)
	seen := make(map[string]bool, len(first))
	out := make([]types.Type, 0, len(first)+8)
	for _, t := range first {
		seen[t.Name] = true
		out = append(out, t)
	}
	for _, t := range types.All() {
		if !seen[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

func (m Model) onPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.pickIdx < len(m.pick)-1 {
			m.pickIdx++
		}
	case "k", "up":
		if m.pickIdx > 0 {
			m.pickIdx--
		}
	case "enter":
		if len(m.pick) == 0 || m.pickIdx < 0 || m.pickIdx >= len(m.pick) {
			return m, nil
		}
		return m.startTypePrompts(m.pick[m.pickIdx])
	case "esc":
		m.clearPick()
	}
	return m, nil
}

// startTypePrompts asks only for the fields the catalog says this type
// needs, in the order values, min, max, format. There is no switch on
// the type name: Custom List is RequiresValues, nothing else.
func (m Model) startTypePrompts(t types.Type) (tea.Model, tea.Cmd) {
	m.pendingType = t
	m.pendingValues = ""
	m.pendingMin = ""
	m.pendingMax = ""
	m.pendingFormat = ""
	q := typePrompts(t)
	if len(q) == 0 {
		m.applyPicked()
		return m, nil
	}
	m.promptQueue = q[1:]
	return m, m.ask(q[0], "", promptLabel(q[0]))
}

func typePrompts(t types.Type) []promptKind {
	var q []promptKind
	if t.RequiresValues {
		q = append(q, promptValues)
	}
	if t.RequiresMin {
		q = append(q, promptMin)
	}
	if t.RequiresMax {
		q = append(q, promptMax)
	}
	if t.RequiresFormat {
		q = append(q, promptFormat)
	}
	return q
}

// applyPicked writes the choice onto the column. Fields the new type
// does not need are cleared so a Custom List's values do not ride along
// onto Sentence and get rendered as if they still applied.
func (m *Model) applyPicked() {
	if m.schemaIdx < 0 || m.schemaIdx >= len(m.schemas) {
		m.clearPick()
		return
	}
	cols := m.schemas[m.schemaIdx].Columns
	if m.colIdx < 0 || m.colIdx >= len(cols) {
		m.clearPick()
		return
	}
	t := m.pendingType
	col := &m.schemas[m.schemaIdx].Columns[m.colIdx]
	col.Type = t.Name
	col.InferredBy = "user"
	if t.RequiresValues {
		col.Values = splitCSV(m.pendingValues)
	} else {
		col.Values = nil
	}
	if t.RequiresMin {
		col.Min = m.pendingMin
	} else {
		col.Min = ""
	}
	if t.RequiresMax {
		col.Max = m.pendingMax
	} else {
		col.Max = ""
	}
	if t.RequiresFormat {
		col.Format = m.pendingFormat
	} else {
		col.Format = ""
	}
	m.note = col.Name + " is " + t.Name
	m.clearPick()
}

func (m *Model) clearPick() {
	m.picking = false
	m.pick = nil
	m.pickIdx = 0
	m.promptQueue = nil
	m.pendingType = types.Type{}
	m.pendingValues = ""
	m.pendingMin = ""
	m.pendingMax = ""
	m.pendingFormat = ""
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sampleCmd loads one short cell per column. A nil service is not
// called: the view says "no sample" instead of dialing.
func (m Model) sampleCmd() tea.Cmd {
	if m.services.ColumnSample == nil {
		return nil
	}
	s := m.currentSchema()
	cols := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = c.Name
	}
	table := s.Source.Table
	if table == "" {
		table = s.Name
	}
	key := sampleKey(s)
	d := m.target()
	sample := m.services.ColumnSample
	return func() tea.Msg {
		dialect, dsn, err := d.resolve()
		if err != nil {
			return sampleMsg{table: key, err: err}
		}
		ctx, cancel := m.withTimeout()
		defer cancel()
		rows, err := sample(ctx, dialect, dsn, table, cols)
		if err != nil {
			return sampleMsg{table: key, err: err}
		}
		values := make(map[string]string, len(cols))
		if len(rows) > 0 {
			for i, name := range cols {
				if i < len(rows[0]) {
					values[name] = shortCell(rows[0][i])
				}
			}
		}
		return sampleMsg{table: key, values: values}
	}
}

func sampleKey(s schema.Schema) string {
	if s.Source.Table != "" {
		return s.Source.Table
	}
	return s.Name
}

// shortCell keeps a sample on one row of an 80-column frame. The full
// value is not useful past the point where the type name falls off.
func shortCell(s string) string {
	r := []rune(s)
	const max = 24
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func (m Model) sampleText(col string) string {
	key := sampleKey(m.currentSchema())
	// A fed sample wins over the "no sample" placeholder. The service
	// being nil only means the screen did not dial; it does not hide a
	// message a test already delivered.
	if vals, ok := m.samples[key]; ok {
		return vals[col]
	}
	if m.sampleOff || m.sampleErr[key] != nil {
		return "no sample"
	}
	return ""
}
