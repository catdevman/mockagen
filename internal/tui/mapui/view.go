package mapui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/charmbracelet/lipgloss"
)

// View draws the screen. Width and height come from WindowSizeMsg so a
// golden test can pin an 80 by 24 frame without a terminal.
func (m Model) View() string {
	switch m.screen {
	case screenEdit:
		return m.viewEdit()
	case screenCheck:
		return m.viewCheck()
	case screenLookup:
		return m.viewLookup()
	case screenPick:
		return m.viewPick()
	case screenObject:
		return m.viewObject()
	default:
		return m.viewBoard()
	}
}

const pickHelp = "enter selects   h parent directory   q cancels"

const pickHelpMap = "enter selects   h parent directory   s skips   q cancels"

func (m Model) viewPick() string {
	var b strings.Builder
	title := "mockagen map   " + m.pickTitle()
	b.WriteString(m.styles.title.Render(clip(title, m.width)))
	b.WriteByte('\n')
	b.WriteString(m.styles.faint.Render(clip(m.picker.CurrentDirectory, m.width)))
	b.WriteString("\n\n")
	for _, line := range strings.Split(strings.TrimRight(m.picker.View(), "\n"), "\n") {
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	}
	help := pickHelp
	if m.onMapStep() {
		help = pickHelpMap
	}
	b.WriteString(m.styles.faint.Render(clip(help, m.width)))
	b.WriteByte('\n')
	note := clip(m.note, m.width)
	if m.note != "" {
		note = m.styles.bad.Render(note)
	}
	b.WriteString(note)
	return b.String()
}

func (m Model) viewBoard() string {
	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteByte('\n')
	b.WriteString(clip(m.metaLine(), m.width))
	b.WriteString("\n\n")
	b.WriteString(m.columns())
	b.WriteByte('\n')
	if line := m.focusedPreview(); line != "" {
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
	}
	for _, line := range m.diagLines(3) {
		b.WriteByte('\n')
		b.WriteString(m.styles.bad.Render(clip(line, m.width)))
	}
	if m.runErr != nil {
		b.WriteByte('\n')
		b.WriteString(m.styles.bad.Render(clip(m.runErr.Error(), m.width)))
	}
	for i, line := range m.enumGaps() {
		if i == 3 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(m.styles.warn.Render(clip("warning  "+line, m.width)))
	}
	b.WriteString("\n\n")
	b.WriteString(m.styles.faint.Render(clip(boardHelpMove, m.width)))
	b.WriteByte('\n')
	b.WriteString(m.styles.faint.Render(clip(boardHelpKeys, m.width)))
	b.WriteByte('\n')
	note := clip(m.noteLine(), m.width)
	if m.confirming {
		note = m.styles.bad.Render(note)
	}
	b.WriteString(note)
	return b.String()
}

const boardHelpMove = "↑↓←→ move between the columns"
const boardHelpKeys = "space link   x ignore   c const   o omit   t edit   v check   s save   q quit"

func (m Model) viewLookup() string {
	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteString("\n\n")
	b.WriteString(clip(m.editTarget+"  schema value ← source", m.width))
	b.WriteString("\n\n")
	for i, out := range m.lookupChoices {
		mark := "  "
		if i == m.lookupIdx {
			mark = "> "
		}
		src := "(none)"
		warn := ""
		if i < len(m.lookupKeys) && len(m.lookupKeys[i]) > 0 {
			src = strings.Join(m.lookupKeys[i], ", ")
		} else {
			warn = "   warning"
		}
		line := clip(fmt.Sprintf("%s%s    ← %s%s", mark, out, src, warn), m.width)
		switch {
		case i == m.lookupIdx:
			line = m.styles.selected.Render(line)
		case warn != "":
			line = m.styles.warn.Render(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	pool := "(none)"
	if len(m.lookupPool) > 0 {
		pool = strings.Join(m.lookupPool, ", ")
	}
	b.WriteString("\nsample not placed  " + pool + "\n\n")
	b.WriteString(m.styles.faint.Render(clip(lookupHelpPlace, m.width)))
	b.WriteByte('\n')
	b.WriteString(m.styles.faint.Render(clip(lookupHelpKeys, m.width)))
	b.WriteByte('\n')
	if m.prompt != promptNone {
		b.WriteString(m.input.View())
		b.WriteByte('\n')
	}
	b.WriteString(clip(m.noteLine(), m.width))
	return b.String()
}

const lookupHelpPlace = "space places the next sample value on this schema row"
const lookupHelpKeys = "x removes it   n types a code this file lacks   enter saves   esc back"

func (m Model) viewEdit() string {
	f := m.field(m.editTarget)
	col := m.columnBy(m.editTarget)
	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteString("\n\n")
	head := m.editTarget
	if f != nil {
		head += "   " + originLabel(*f)
	}
	head += "   " + col.SQLType
	b.WriteString(m.styles.title.Render(clip(head, m.width)))
	b.WriteString("\n\nchain\n")
	if f == nil || len(f.Transforms) == 0 {
		b.WriteString(m.styles.faint.Render("(empty chain)"))
		b.WriteByte('\n')
	} else {
		for i, step := range f.Transforms {
			mark := "  "
			if i == m.editIdx {
				mark = "> "
			}
			line := clip(mark+stepLabel(step), m.width)
			if i == m.editIdx {
				line = m.styles.selected.Render(line)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	b.WriteString(m.afterChain(f, col))
	b.WriteString("\nsample\n")
	if f == nil {
		b.WriteString("(no field)\n")
	} else {
		for _, line := range m.editSample(*f) {
			b.WriteString(clip(line, m.width))
			b.WriteByte('\n')
		}
	}
	if m.prompt != promptNone {
		b.WriteByte('\n')
		b.WriteString(m.input.View())
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.styles.faint.Render(clip(editHelp, m.width)))
	b.WriteByte('\n')
	b.WriteString(clip(m.noteLine(), m.width))
	return b.String()
}

const editHelp = "↑↓ step  ] cycle  a add  d drop  e arg  n null  f format  b default  esc back"

const objectHelp = "j/k key   h/l source   a add   space bind   c const   d drop   s save   esc back"

// viewObject draws the json/jsonb editor. Hints are shown and not
// stored: the screen writes a key only when one is typed.
func (m Model) viewObject() string {
	col := m.columnBy(m.editTarget)
	f := m.field(m.editTarget)
	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteString("\n\n")
	head := m.editTarget + "   " + col.SQLType
	if f != nil {
		head += "   " + originLabel(*f)
	}
	b.WriteString(m.styles.title.Render(clip(head, m.width)))
	b.WriteString("\n\nkeys\n")
	if f == nil || f.Object == nil || len(f.Object.Keys) == 0 {
		b.WriteString(m.styles.faint.Render("(no keys yet)"))
		b.WriteByte('\n')
	} else {
		for i, k := range f.Object.Keys {
			mark := "  "
			if i == m.objIdx {
				mark = "> "
			}
			line := clip(mark+k.Key+"    "+objectKeyLabel(k), m.width)
			if i == m.objIdx {
				line = m.styles.selected.Render(line)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	hints := "(none)"
	if len(m.objHints) > 0 {
		hints = strings.Join(m.objHints, ", ")
	}
	b.WriteString("\nhints  " + hints + "\n")
	src := "(none)"
	names := m.sourceNames()
	if m.src >= 0 && m.src < len(names) {
		src = names[m.src]
	}
	b.WriteString("bind source  " + src + "\n")
	if m.prompt != promptNone {
		b.WriteByte('\n')
		b.WriteString(m.input.View())
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(m.styles.faint.Render(clip(objectHelp, m.width)))
	b.WriteByte('\n')
	b.WriteString(clip(m.noteLine(), m.width))
	return b.String()
}

func objectKeyLabel(k importer.ObjectField) string {
	switch {
	case k.Constant != nil:
		return fmt.Sprintf("constant %q", *k.Constant)
	case k.Source != "":
		return "← " + k.Source
	case k.Object != nil:
		return "object"
	default:
		return "no origin"
	}
}

// unfinishedHint is shown under the check screen's diagnostic. The
// to: list reads like a finished mapping, and editing those names
// does not give the lookup any incoming keys.
const unfinishedHint = "to: lists outputs, not keys. esc, t on the column, e on map, ←→ chooses."

func (m Model) viewCheck() string {
	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteString("\n\ncompile\n")
	if len(m.diags) == 0 {
		b.WriteString(m.styles.faint.Render("clean"))
		b.WriteByte('\n')
	}
	for _, line := range m.diagLines(len(m.diags)) {
		b.WriteString(m.styles.bad.Render(clip(line, m.width)))
		b.WriteByte('\n')
	}
	b.WriteString("\nrows\n")
	if m.report == nil {
		// Compile failed, so Run was not called. Saying the sample
		// had no errors would read as a clean load.
		b.WriteString(m.styles.faint.Render("not run"))
		b.WriteByte('\n')
	} else {
		rep := m.report
		line := fmt.Sprintf("read %d, wrote %d, skipped %d, errors %d",
			rep.Read, rep.Written, rep.Skipped, len(rep.Errors))
		b.WriteString(clip(line, m.width))
		b.WriteByte('\n')
		for _, line := range m.errorGroups() {
			b.WriteString(m.styles.bad.Render(clip(line, m.width)))
			b.WriteByte('\n')
		}
		if m.runErr != nil {
			b.WriteString(m.styles.bad.Render(clip(m.runErr.Error(), m.width)))
			b.WriteByte('\n')
		}
	}
	if m.unfinished() {
		b.WriteByte('\n')
		b.WriteString(clip(unfinishedHint, m.width))
		b.WriteByte('\n')
	}
	if gaps := m.enumGaps(); len(gaps) > 0 {
		b.WriteString("\nwarnings\n")
		for _, line := range gaps {
			b.WriteString(m.styles.warn.Render(clip(line, m.width)))
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n")
	b.WriteString(m.styles.faint.Render("esc back   s saves   q quits from the board"))
	b.WriteByte('\n')
	return b.String()
}

func (m Model) titleLine() string {
	name := m.fm.Name
	if name == "" {
		name = "field map"
	}
	where := "stdout"
	if m.outPath != "" {
		where = m.outPath
	}
	state := "clean"
	switch len(m.diags) {
	case 1:
		state = "1 problem"
	default:
		if len(m.diags) > 1 {
			state = fmt.Sprintf("%d problems", len(m.diags))
		}
	}
	line := fmt.Sprintf("mockagen map   %s   → %s   %s", name, where, state)
	return m.styles.title.Render(clip(line, m.width))
}

func (m Model) metaLine() string {
	format := m.sample.Format
	if format == "" {
		format = m.fm.Source.Format
	}
	if format == "" {
		format = "csv"
	}
	delim := m.sample.Delimiter
	if delim == "" {
		delim = m.fm.Source.Delimiter
	}
	enc := m.sample.Encoding
	if enc == "" {
		enc = m.fm.Source.Encoding
	}
	rows := fmt.Sprintf("%d rows", m.sample.Rows)
	if m.sample.Rows == 1 {
		rows = "1 row"
	}
	return fmt.Sprintf("%s   delim %s   %s   %s   %s",
		format, showDelim(delim), enc, rows, strings.Join(m.sourceNames(), ", "))
}

func (m Model) columns() string {
	leftW, rightW := m.splitWidth()
	src := m.sourceLines(leftW - 2)
	tgt := m.targetLines(rightW - 2)
	// The shorter column is padded so the two borders end on the same
	// row. An unpadded box stops halfway down the other one.
	n := len(src)
	if len(tgt) > n {
		n = len(tgt)
	}
	for len(src) < n {
		src = append(src, "")
	}
	for len(tgt) < n {
		tgt = append(tgt, "")
	}
	left := m.box(m.focus == focusSource, leftW, "sources", src)
	right := m.box(m.focus == focusTarget, rightW, "targets", tgt)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

// splitWidth keeps the two columns inside the window. The source list
// is the narrow one: a header name is short next to a sql type.
func (m Model) splitWidth() (int, int) {
	w := m.width
	if w < 40 {
		w = 40
	}
	left := w / 3
	if left < 18 {
		left = 18
	}
	if left > 28 {
		left = 28
	}
	right := w - left - 1
	if right < 18 {
		right = 18
	}
	return left, right
}

func (m Model) box(on bool, width int, title string, lines []string) string {
	inner := width - 2
	if inner < 4 {
		inner = 4
	}
	var b strings.Builder
	b.WriteString(clip(title, inner))
	for _, line := range lines {
		b.WriteByte('\n')
		// Lines are clipped before a style is applied. Clipping after
		// would cut the reset sequence off a colored row.
		b.WriteString(line)
	}
	st := m.styles.box
	if on {
		st = m.styles.boxOn
	}
	return st.Width(inner).Render(b.String())
}

func (m Model) sourceLines(inner int) []string {
	names := m.sourceNames()
	if len(names) == 0 {
		return []string{m.styles.faint.Render("(no header)")}
	}
	used := map[string]string{}
	for _, f := range m.fm.Fields {
		for _, name := range importer.SourceNames(f) {
			if used[name] == "" {
				used[name] = f.Target
			}
		}
	}
	lines := make([]string, len(names))
	for i, name := range names {
		mark := " "
		if m.focus == focusSource && i == m.src {
			mark = ">"
		}
		line := clip(mark+" "+name, inner)
		if tgt := used[name]; tgt != "" {
			plain := mark + " " + name + "  → " + tgt
			line = clip(plain, inner)
		}
		if m.focus == focusSource && i == m.src {
			line = m.styles.selected.Render(line)
		}
		lines[i] = line
	}
	return lines
}

func (m Model) targetLines(inner int) []string {
	if len(m.sch.Columns) == 0 {
		return []string{m.styles.faint.Render("(no columns)")}
	}
	lines := make([]string, len(m.sch.Columns))
	for i, col := range m.sch.Columns {
		mark := " "
		selected := m.focus == focusTarget && i == m.tgt
		if selected {
			mark = ">"
		}
		line := clip(fmt.Sprintf("%s %-12s %-12s %s", mark, col.Name, col.SQLType, m.targetState(col)), inner)
		switch {
		case selected:
			line = m.styles.selected.Render(line)
		case m.diagOn(col.Name):
			line = m.styles.bad.Render(line)
		case m.omitted(col.Name):
			line = m.styles.faint.Render(line)
		}
		lines[i] = line
	}
	return lines
}

func (m Model) targetState(col schema.Column) string {
	if f := m.field(col.Name); f != nil {
		state := originLabel(*f)
		if brief := chainBrief(f.Transforms); brief != "" {
			state += "  " + brief
		}
		return state
	}
	if m.omitted(col.Name) {
		return "omitted"
	}
	if col.Identity || canOmit(col) {
		return "unseen"
	}
	return "BLOCKER"
}

// focusedPreview is one cell of the first row Run kept. A compile
// failure has no report; the Apply line is only a fallback and does
// not mean the plan was run.
func (m Model) focusedPreview() string {
	if m.report != nil {
		return m.reportPreview()
	}
	f := m.focusedField()
	if f == nil || len(m.sample.Preview) == 0 {
		return ""
	}
	lines := m.sampleLines(*f)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// reportPreview names the cell from sampleCols. Samples are write
// order, and the schema list still includes columns that were omitted.
func (m Model) reportPreview() string {
	if m.report == nil || len(m.report.Samples) == 0 {
		return ""
	}
	name := m.column().Name
	rec := m.report.Samples[0]
	for i, col := range m.sampleCols {
		if col != name {
			continue
		}
		return name + "  " + sampleCell(rec, i)
	}
	return ""
}

// sampleCell spells a stored value. An int lives in the numeric
// slot, so the arena would be empty and look like a blank. NULL
// is the word, because an empty string is a real value.
func sampleCell(rec importer.Record, i int) string {
	if i < 0 || i >= len(rec.Values) {
		return "NULL"
	}
	switch rec.Values[i].Kind {
	case importer.KindNull:
		return "NULL"
	case importer.KindInt:
		return strconv.FormatInt(rec.Int(i), 10)
	case importer.KindFloat:
		return strconv.FormatFloat(rec.Float(i), 'f', -1, 64)
	case importer.KindBool:
		if rec.Bool(i) {
			return "true"
		}
		return "false"
	case importer.KindText, importer.KindRaw:
		return string(rec.Bytes(i))
	default:
		return "NULL"
	}
}

func (m Model) afterChain(f *importer.Field, col schema.Column) string {
	nullIf := "—"
	format := "—"
	def := "—"
	if f != nil && len(f.NullIf) > 0 {
		nullIf = strings.Join(f.NullIf, ", ")
	}
	if f != nil && f.Format != "" {
		format = f.Format
	}
	if f != nil && f.Default != nil {
		def = *f.Default
	}
	var b strings.Builder
	b.WriteString("null_if   " + nullIf + "\n")
	b.WriteString("format    " + format + "\n")
	b.WriteString("default   " + def + "\n")
	b.WriteString("schema    " + factsLine(col) + "\n")
	return b.String()
}

// editSample shows at most three rows. The chain, null_if, and the
// checks are Stages, so this screen does not grow a second Apply.
// A sample with no rows falls back to the one-line outcome.
func (m Model) editSample(f importer.Field) []string {
	if len(m.sample.Preview) == 0 {
		return m.sampleLines(f)
	}
	n := len(m.sample.Preview)
	if n > 3 {
		n = 3
	}
	col := m.columnBy(f.Target)
	lines := make([]string, 0, n*2)
	for i := 0; i < n; i++ {
		in, label := m.rowInputs(f, m.sample.Preview[i])
		lines = append(lines, fmt.Sprintf("%d  %s", i+1, label))
		var stages strings.Builder
		for j, st := range importer.Stages(col, f, in) {
			if j > 0 {
				stages.WriteString("   ")
			}
			stages.WriteString(stageText(st))
		}
		lines = append(lines, stages.String())
	}
	return lines
}

// stageText prints the failure on the step that returned it. The
// value is omitted then, because a carried-over value would look
// like the step had succeeded.
func stageText(st importer.Stage) string {
	if st.Err != nil {
		return st.Name + "  " + st.Err.Error()
	}
	if st.Null {
		return st.Name + "  NULL"
	}
	return st.Name + "  " + st.Value
}

func (m Model) sampleLines(f importer.Field) []string {
	col := m.columnBy(f.Target)
	if len(m.sample.Preview) == 0 {
		if f.Constant != nil {
			got, isNull, err := importer.Apply(col, f, nil)
			return []string{"1  " + outcome(fmt.Sprintf("%q", *f.Constant), got, isNull, err)}
		}
		return []string{"(no sample rows)"}
	}
	n := len(m.sample.Preview)
	if n > 5 {
		n = 5
	}
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		in, label := m.rowInputs(f, m.sample.Preview[i])
		got, isNull, err := importer.Apply(col, f, in)
		lines[i] = fmt.Sprintf("%d  %s", i+1, outcome(label, got, isNull, err))
	}
	return lines
}

func (m Model) rowInputs(f importer.Field, row []string) ([]string, string) {
	if f.Constant != nil {
		return nil, fmt.Sprintf("constant %q", *f.Constant)
	}
	// SourceNames walks object keys in the order Apply reads them.
	// A preview that used only Field.Source would pass the wrong cells
	// once the origin is an object.
	names := importer.SourceNames(f)
	vals := make([]string, len(names))
	for i, name := range names {
		vals[i] = m.cell(row, name)
	}
	if len(vals) == 0 {
		return nil, "(no cells)"
	}
	return vals, strings.Join(vals, " | ")
}

func (m Model) cell(row []string, name string) string {
	names := m.sourceNames()
	for i, n := range names {
		if n == name && i < len(row) {
			return row[i]
		}
	}
	return ""
}

func outcome(in, got string, isNull bool, err error) string {
	if err != nil {
		return in + "  →  " + err.Error()
	}
	if isNull {
		return in + "  →  NULL"
	}
	return in + "  →  " + got
}

// errorGroups buckets Report.Errors by target and reason. The line
// numbers stay under the pair so two failures of one column do not
// become two unrelated rows.
func (m Model) errorGroups() []string {
	if m.report == nil {
		return nil
	}
	type pair struct {
		target string
		reason string
	}
	var order []pair
	lines := map[pair][]string{}
	for _, e := range m.report.Errors {
		reason := ""
		if e.Err != nil {
			reason = e.Err.Error()
		}
		k := pair{e.Target, reason}
		if _, ok := lines[k]; !ok {
			order = append(order, k)
		}
		lines[k] = append(lines[k], strconv.FormatInt(e.Line, 10))
	}
	out := make([]string, 0, len(order)*2)
	for _, k := range order {
		out = append(out, k.target+"  "+k.reason)
		out = append(out, "line "+strings.Join(lines[k], ", "))
	}
	return out
}

func (m Model) diagLines(limit int) []string {
	if limit > len(m.diags) {
		limit = len(m.diags)
	}
	lines := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		lines = append(lines, m.diags[i].Error())
	}
	extra := len(m.diags) - limit
	if extra > 0 {
		lines = append(lines, fmt.Sprintf("and %d more", extra))
	}
	return lines
}

func (m Model) noteLine() string {
	if m.confirming {
		return "quit without saving?  y quits   n stays   s saves"
	}
	if m.note != "" {
		return m.note
	}
	if len(m.diags) == 0 {
		return "clean"
	}
	return ""
}

func originLabel(f importer.Field) string {
	switch {
	case f.Object != nil:
		return "object"
	case f.Constant != nil:
		return fmt.Sprintf("constant %q", *f.Constant)
	case f.Source != "":
		return "← " + f.Source
	case len(f.Sources) > 0:
		return "join " + strings.Join(f.Sources, "+")
	case len(f.Coalesce) > 0:
		return "coalesce " + strings.Join(f.Coalesce, ", ")
	default:
		return "no origin"
	}
}

func chainBrief(steps []importer.Transform) string {
	if len(steps) == 0 {
		return ""
	}
	parts := make([]string, len(steps))
	for i, step := range steps {
		parts[i] = step.Name
		if step.Name == "map" && len(step.Accept) > 0 {
			parts[i] = "map!"
		}
	}
	return strings.Join(parts, ", ")
}

func stepLabel(t importer.Transform) string {
	switch t.Name {
	case "trim":
		if t.HasCutset {
			return "trim " + t.Cutset
		}
		return "trim"
	case "case":
		return "case " + t.Case
	case "replace":
		return fmt.Sprintf("replace %q → %q", t.From, t.To)
	case "regex_replace":
		return fmt.Sprintf("regex %q → %q", t.Pattern, t.To)
	case "map":
		if len(t.Accept) > 0 {
			return "map unfinished"
		}
		return "map " + stepArg(t)
	case "pad_left", "pad_right":
		return fmt.Sprintf("%s %d %q", t.Name, t.Width, t.With)
	default:
		return t.Name
	}
}

func factsLine(col schema.Column) string {
	parts := []string{col.SQLType}
	if col.SQLType == "" {
		parts = nil
	}
	if col.MaxLength != nil {
		parts = append(parts, fmt.Sprintf("max_length %d", *col.MaxLength))
	}
	if col.NumericPrecision != nil {
		parts = append(parts, fmt.Sprintf("precision %d", *col.NumericPrecision))
	}
	if col.NumericScale != nil {
		parts = append(parts, fmt.Sprintf("scale %d", *col.NumericScale))
	}
	if len(col.EnumValues) > 0 {
		parts = append(parts, "enum "+strings.Join(col.EnumValues, "|"))
	}
	if col.Nullable {
		parts = append(parts, "nullable")
	} else if col.Name != "" {
		parts = append(parts, "not null")
	}
	if col.Identity {
		parts = append(parts, "identity")
	}
	if col.Default != "" {
		parts = append(parts, "default "+col.Default)
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, "  ")
}

func showDelim(d string) string {
	switch d {
	case "\t":
		return `\t`
	case "":
		return ","
	default:
		return d
	}
}

// clip stops a row from wrapping inside the border. Once a line wraps,
// the two columns no longer share a row and the cursor is hard to see.
func clip(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 1 && lipgloss.Width(string(r)) > n {
		r = r[:len(r)-1]
	}
	return string(r)
}
