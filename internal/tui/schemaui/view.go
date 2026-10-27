package schemaui

import (
	"strconv"
	"strings"

	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/catdevman/mockagen/pkg/schema/introspect"
)

// Help lines are the golden substrings the view tests look for. They
// name the keys and never include a DSN or a password.
const (
	connectHelp = "j/k move   tab field   enter connect   n save connection   q quit"
	tablesHelp  = "j/k move   space select   / filter   enter inspect   esc back   q quit"
	columnsHelp = "j/k move   e type   p preview   s save   [ ] table   esc back   q quit"
	previewHelp = "s save   esc back   q quit"
	saveHelp    = "enter save   tab field   e edit   esc back   q quit"
)

// multiSaveNote is shown when more than one table is selected. The path
// is a directory; each file is named after its table.
const multiSaveNote = "each table is <table>.schema.yaml inside this directory"

// View draws the screen. Width and height come from WindowSizeMsg so a
// test can pin an 80 by 24 frame without a terminal.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render(clip(m.title(), m.width)))
	b.WriteByte('\n')
	switch m.screen {
	case screenTables:
		m.writeTables(&b)
	case screenColumns:
		m.writeColumns(&b)
	case screenPreview:
		m.writePreview(&b)
	case screenSave:
		m.writeSave(&b)
	default:
		m.writeConnect(&b)
	}
	m.writeNote(&b)
	return b.String()
}

func (m Model) writeConnect(b *strings.Builder) {
	if m.prompt == promptConnName || m.prompt == promptConnPass {
		b.WriteByte('\n')
		b.WriteString(clip(promptLabel(m.prompt), m.width))
		b.WriteByte('\n')
		b.WriteString(clip(m.input.Prompt+inputText(m.input), m.width))
		b.WriteString("\n\n")
		b.WriteString(m.help("enter confirms   esc cancels"))
		return
	}
	b.WriteByte('\n')
	b.WriteString("dialect")
	b.WriteByte('\n')
	for i, name := range m.dialects {
		mark := "  "
		if m.focus == focusDialect && i == m.dialectIdx {
			mark = "> "
		}
		line := mark + name
		if m.focus == focusDialect && i == m.dialectIdx {
			line = m.styles.selected.Render(line)
		}
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString("dsn")
	b.WriteByte('\n')
	mark := "  "
	if m.focus == focusDSN {
		mark = "> "
	}
	b.WriteString(clip(mark+inputText(m.dsnInput), m.width))
	b.WriteByte('\n')
	if len(m.conns) > 0 {
		b.WriteByte('\n')
		b.WriteString("saved")
		b.WriteByte('\n')
		for i, c := range m.conns {
			mark = "  "
			if m.focus == focusSaved && i == m.savedIdx {
				mark = "> "
			}
			line := mark + c.Name + "  " + c.Dialect
			if m.focus == focusSaved && i == m.savedIdx {
				line = m.styles.selected.Render(line)
			}
			b.WriteString(clip(line, m.width))
			b.WriteByte('\n')
		}
	}
	if m.hasEnv {
		b.WriteByte('\n')
		name := m.envName
		if name == "" {
			name = "env"
		}
		// The variable name, never the value. A DATABASE_URL usually
		// carries a password, and the view must not echo it.
		mark = "  "
		if m.focus == focusEnv {
			mark = "> "
		}
		b.WriteString(clip(mark+"e  "+name, m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.help(connectHelp))
}

func (m Model) writeTables(b *strings.Builder) {
	b.WriteByte('\n')
	if m.dialect != "" {
		b.WriteString(clip(m.dialect, m.width))
		b.WriteByte('\n')
	}
	vis := m.visibleTables()
	if len(vis) == 0 {
		b.WriteString("no tables")
		b.WriteByte('\n')
	}
	for i, idx := range vis {
		t := m.tables[idx]
		mark := "  "
		if i == m.tableCursor {
			mark = "> "
		}
		sel := "  "
		if m.selected[tableKey(t)] {
			sel = "* "
		}
		line := mark + sel + tableLabel(t) + "  " + rowCount(t)
		if i == m.tableCursor {
			line = m.styles.selected.Render(line)
		}
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(clip("filter  "+inputText(m.filter), m.width))
	b.WriteByte('\n')
	b.WriteString(m.help(tablesHelp))
}

// rowCount prints an em dash for a nil estimate. sqlite and a postgres
// table that was never analysed have no count, and 0 would be a lie.
func rowCount(t introspect.Table) string {
	if t.ApproxRows == nil {
		return "—"
	}
	return strconv.FormatInt(*t.ApproxRows, 10)
}

func (m Model) writeColumns(b *strings.Builder) {
	if m.prompt == promptValues || m.prompt == promptMin || m.prompt == promptMax || m.prompt == promptFormat {
		b.WriteByte('\n')
		b.WriteString(clip(m.pendingType.Name, m.width))
		b.WriteByte('\n')
		b.WriteString(clip(promptLabel(m.prompt), m.width))
		b.WriteByte('\n')
		b.WriteString(clip(m.input.Prompt+inputText(m.input), m.width))
		b.WriteString("\n\n")
		b.WriteString(m.help("enter sets the type   esc cancels"))
		return
	}
	if m.picking {
		m.writePicker(b)
		return
	}
	b.WriteByte('\n')
	if len(m.schemas) > 1 {
		var names []string
		for i, s := range m.schemas {
			label := tableBase(s)
			if i == m.schemaIdx {
				label = "[" + label + "]"
			}
			names = append(names, label)
		}
		line := strings.Join(names, "  ")
		if m.onStrip {
			line = m.styles.selected.Render(line)
		}
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	} else if s := m.currentSchema(); tableBase(s) != "" && tableBase(s) != "schema" {
		b.WriteString(clip(tableBase(s), m.width))
		b.WriteByte('\n')
	}
	s := m.currentSchema()
	if len(s.Columns) == 0 {
		b.WriteString("no columns")
		b.WriteByte('\n')
	}
	for i, col := range s.Columns {
		mark := "  "
		if i == m.colIdx && !m.onStrip {
			mark = "> "
		}
		gen := col.Type
		if col.Type == "" {
			gen = m.styles.bad.Render("UNRESOLVED")
		}
		line := mark + col.Name + "  " + col.SQLType + "  " + gen + "  " + col.InferredBy + "  " + m.sampleText(col.Name)
		if i == m.colIdx && !m.onStrip {
			// Re-render the marker only. The type word is already styled
			// when it is unresolved, and wrapping the whole line would
			// nest that style.
			line = mark + col.Name + "  " + col.SQLType + "  " + gen + "  " + col.InferredBy + "  " + m.sampleText(col.Name)
		}
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.help(columnsHelp))
}

func (m Model) writePicker(b *strings.Builder) {
	b.WriteByte('\n')
	b.WriteString("type")
	b.WriteByte('\n')
	start, end := window(len(m.pick), m.pickIdx, 12)
	for i := start; i < end; i++ {
		mark := "  "
		line := mark + m.pick[i].Name
		if i == m.pickIdx {
			line = "> " + m.pick[i].Name
			line = m.styles.selected.Render(line)
		}
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.help("j/k move   enter select   esc cancel"))
}

func window(n, idx, size int) (int, int) {
	if n == 0 {
		return 0, 0
	}
	if size > n {
		size = n
	}
	start := idx - size/2
	if start < 0 {
		start = 0
	}
	end := start + size
	if end > n {
		end = n
		start = end - size
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func (m Model) writePreview(b *strings.Builder) {
	b.WriteByte('\n')
	s := m.currentSchema()
	if name := tableBase(s); name != "" && name != "schema" {
		b.WriteString(clip(name, m.width))
		b.WriteByte('\n')
	}
	if len(m.preview) == 0 {
		b.WriteString("no rows")
		b.WriteByte('\n')
	}
	for _, row := range m.preview {
		var cells []string
		for i, col := range s.Columns {
			cells = append(cells, col.Name+"="+previewCell(col, row, i))
		}
		b.WriteString(clip(strings.Join(cells, "  "), m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.help(previewHelp))
}

// previewCell prints UNRESOLVED for an empty type even when the
// generator returned a cell. A resolved blank stays blank.
func previewCell(col schema.Column, row []string, i int) string {
	if col.Type == "" {
		return "UNRESOLVED"
	}
	if i < len(row) {
		return row[i]
	}
	return ""
}

func (m Model) writeSave(b *strings.Builder) {
	b.WriteByte('\n')
	b.WriteString("schema path")
	b.WriteByte('\n')
	mark := "  "
	if m.saveField == 0 {
		mark = "> "
	}
	b.WriteString(clip(mark+inputText(m.pathInput), m.width))
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString("spec path")
	b.WriteByte('\n')
	mark = "  "
	if m.saveField == 1 {
		mark = "> "
	}
	b.WriteString(clip(mark+inputText(m.renderInput), m.width))
	b.WriteByte('\n')
	if len(m.schemas) > 1 {
		b.WriteByte('\n')
		b.WriteString(clip(multiSaveNote, m.width))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.help(saveHelp))
}
