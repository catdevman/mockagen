// Package mapui is the bubbletea screen over a field map. It edits the
// same FieldMap the non-interactive command writes. Update does no I/O.
// s writes from a command and stays on the screen. stdout has no file
// until quit, so that checkpoint is printed on the way out.
package mapui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Session is what the screen starts from. Schema and Sample are set
// when the command already loaded them. Ask is set when a file flag
// was left off. Those reads run as commands, so Update itself does no
// I/O and a test can drive the picker with messages.
type Session struct {
	Schema  schema.Schema
	Sample  importer.Sample
	Map     importer.FieldMap
	OutPath string
	Ask     *Ask
}

// Result is the map after the screen closes. Save means the command
// should write it on the way out, which is the stdout case. Checkpoint
// means s already wrote an -out file. Quit does not write that file
// again. Drift means a -map from the command line does not match the
// sample. The screen did not edit it.
type Result struct {
	Map        importer.FieldMap
	Schema     schema.Schema
	Sample     importer.Sample
	SchemaPath string
	Save       bool
	Checkpoint bool
	Drift      bool
}

// savedMsg is the result of s. The bytes are the YAML snapshot the
// quit check compares against, so a later edit still asks and a save
// on its own does not.
type savedMsg struct {
	raw []byte
	err error
}

type screen int

const (
	screenBoard screen = iota
	screenEdit
	screenCheck
	screenLookup
	screenPick
)

type focus int

const (
	focusSource focus = iota
	focusTarget
)

type promptKind int

const (
	promptNone promptKind = iota
	promptConst
	promptStep
	promptNullIf
	promptFormat
	promptDefault
	promptLookupKey
)

// stepOrder is the closed transform set. The editor cycles this list so
// it cannot offer expr or a name Compile would reject as unknown.
var stepOrder = []string{
	"trim", "case", "replace", "regex_replace", "map", "pad_left", "pad_right",
}

// Model is the mapping screen. The field map is the one Propose built;
// every key edits that value, and Compile is what marks a column bad.
type Model struct {
	sch        schema.Schema
	sample     importer.Sample
	fm         importer.FieldMap
	outPath    string
	schemaPath string
	sampleN    int

	pickSteps []pickStep
	pickIdx   int
	picker    filepicker.Model
	lastPick  string
	// busy ignores picker keys while a load is in flight, so a second
	// enter cannot start a read of a different file.
	busy bool
	// haveBase is a map to extend. mapFromCLI means -map named it, so
	// a header mismatch quits for the command to report instead of
	// asking for a file the user did not come here to pick.
	haveBase   bool
	mapFromCLI bool
	base       importer.FieldMap
	drift      bool

	r      *lipgloss.Renderer
	styles styles
	width  int
	height int

	screen screen
	focus  focus
	src    int
	tgt    int

	editTarget string
	editIdx    int
	prompt     promptKind
	input      textinput.Model

	// lookupChoices is the schema's list of stored values, in schema
	// order. lookupKeys[i] holds the source codes that store
	// lookupChoices[i]. lookupPool is sample values not placed yet.
	// A schema value with no key is a warning, not a compile error:
	// this file can load without ever writing that value, and a later
	// file may still need a code for it.
	lookupChoices []string
	lookupKeys    [][]string
	lookupPool    []string
	lookupIdx     int

	diags []importer.Diagnostic
	note  string
	save  bool
	// baseline is the map as YAML at open, and again after each s. q
	// compares against it, so a quit with no edits does not stop to
	// ask. The compiled regexp cache is not in the YAML, so a refresh
	// alone is not an edit.
	baseline []byte
	// confirming is the "quit without saving" question. y leaves, n
	// stays, and s writes and stays. Anything else is ignored so the
	// question cannot also link a column.
	confirming bool
	// checkpoint is set after s succeeds. q then leaves the file in
	// place instead of writing it a second time.
	checkpoint bool
}

type styles struct {
	title    lipgloss.Style
	faint    lipgloss.Style
	selected lipgloss.Style
	bad      lipgloss.Style
	warn     lipgloss.Style
	box      lipgloss.Style
	boxOn    lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	return styles{
		title:    r.NewStyle().Bold(true),
		faint:    r.NewStyle().Faint(true),
		selected: r.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		bad:      r.NewStyle().Foreground(lipgloss.Color("1")),
		warn:     r.NewStyle().Foreground(lipgloss.Color("3")),
		box:      r.NewStyle().Border(lipgloss.RoundedBorder()),
		boxOn: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("6")),
	}
}

// New builds a screen from a session. The renderer is the process
// default so a real terminal keeps its color profile; tests replace it.
func New(sess Session) Model {
	m := Model{
		sch:     sess.Schema,
		sample:  sess.Sample,
		fm:      sess.Map,
		outPath: sess.OutPath,
		r:       lipgloss.DefaultRenderer(),
		width:   80,
		height:  24,
		focus:   focusTarget,
	}
	m.styles = newStyles(m.r)
	if sess.Ask != nil && len(sess.Ask.steps()) > 0 {
		m.installAsk(*sess.Ask, sess)
		m, _ = m.openPicker(sess.Ask.firstDir())
		return m
	}
	m.placeCursor()
	m.refresh()
	m.capture()
	return m
}

// Run opens the screen on the alt screen and blocks until quit. Save is
// set only when a stdout checkpoint has to be printed on the way out.
// Checkpoint means s already wrote the -out file.
func Run(sess Session) (Result, error) {
	p := tea.NewProgram(New(sess), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	done, ok := final.(Model)
	if !ok {
		return Result{}, fmt.Errorf("mapui: unexpected model %T", final)
	}
	return Result{
		Map:        done.fm,
		Schema:     done.sch,
		Sample:     done.sample,
		SchemaPath: done.schemaPath,
		Save:       done.save,
		Checkpoint: done.checkpoint,
		Drift:      done.drift,
	}, nil
}

// Init starts the picker read when a file flag was left off. The blink
// cursor is started only when a prompt opens, so the board does not run
// a timer it never reads.
func (m Model) Init() tea.Cmd {
	if m.screen == screenPick {
		return m.picker.Init()
	}
	return nil
}

// Update applies one message. File writes stay in the command: this
// method only changes the map the screen is holding.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		if m.screen == screenPick {
			m.picker.SetHeight(m.pickHeight())
		}
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	case pickedSchemaMsg:
		return m.onPickedSchema(msg)
	case pickedDataMsg:
		return m.onPickedData(msg)
	case pickedMapMsg:
		return m.onPickedMap(msg)
	case savedMsg:
		if msg.err != nil {
			m.note = "save failed: " + msg.err.Error()
			return m, nil
		}
		m.baseline = msg.raw
		m.checkpoint = true
		if m.outPath == "" {
			m.note = "saved; q prints the map"
		} else {
			m.note = "saved " + m.outPath
		}
		return m, nil
	default:
		// The picker's directory listing is an unexported message.
		// Forwarding it is how the files arrive without a read in
		// Update.
		if m.screen == screenPick {
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c leaves immediately, without the quit question. A
	// checkpoint still stands: the file is already written, and a
	// stdout checkpoint is printed from that snapshot.
	if msg.String() == "ctrl+c" {
		return m.leave()
	}
	if m.confirming {
		return m.onConfirm(msg)
	}
	if m.prompt != promptNone {
		return m.onPrompt(msg)
	}
	switch m.screen {
	case screenEdit:
		return m.onEdit(msg)
	case screenLookup:
		return m.onLookup(msg)
	case screenCheck:
		switch msg.String() {
		case "s":
			return m, m.saveCmd()
		case "esc", "q":
			m.screen = screenBoard
		}
		return m, nil
	case screenPick:
		return m.onPick(msg)
	default:
		return m.onBoard(msg)
	}
}

func (m Model) onBoard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m.askQuit()
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "left", "h", "shift+tab":
		m.focus = focusSource
	case "right", "l", "tab":
		m.focus = focusTarget
	case " ":
		m.link()
	case "x":
		m.ignoreSource()
	case "c":
		cmd := m.ask(promptConst, currentConst(m.focusedField()), "constant value")
		return m, cmd
	case "o":
		m.toggleOmit()
	case "t", "enter":
		m.openEdit()
	case "v":
		m.screen = screenCheck
	case "s":
		return m, m.saveCmd()
	}
	return m, nil
}

// askQuit leaves at once when nothing has changed since the screen
// opened or the last s. Edits since that point still ask, because q
// is leave and s is not.
func (m Model) askQuit() (tea.Model, tea.Cmd) {
	if !m.dirty() {
		return m.leave()
	}
	m.confirming = true
	return m, nil
}

// leave ends the screen. A checkpoint to a file stays where s put it.
// Stdout has nowhere to write until the screen is gone, so that
// checkpoint is printed on the way out. Discarding edits after a
// checkpoint restores the saved map, so the print matches the file.
func (m Model) leave() (tea.Model, tea.Cmd) {
	if m.dirty() && m.checkpoint {
		if loaded, err := importer.Unmarshal(m.baseline, ".yaml"); err == nil {
			m.fm = loaded
		}
	}
	if m.outPath == "" && m.checkpoint {
		m.save = true
	}
	return m, tea.Quit
}

// saveCmd writes the map and stays on the screen. The snapshot is
// taken here, before the write, so a key pressed while the write
// runs is still an edit on top of this save.
func (m Model) saveCmd() tea.Cmd {
	raw, err := importer.Marshal(m.fm, ".yaml")
	if err != nil {
		return func() tea.Msg { return savedMsg{err: err} }
	}
	path := m.outPath
	if path == "" {
		return func() tea.Msg { return savedMsg{raw: raw} }
	}
	fm := m.fm
	return func() tea.Msg {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return savedMsg{err: err}
			}
		}
		if err := importer.Save(path, fm); err != nil {
			return savedMsg{err: err}
		}
		return savedMsg{raw: raw}
	}
}

// onConfirm answers the quit question. y and a second q leave
// without writing. s saves and stays, because saving is not how
// the screen closes.
func (m Model) onConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "q":
		return m.leave()
	case "s":
		m.confirming = false
		return m, m.saveCmd()
	case "n", "N", "esc":
		m.confirming = false
		return m, nil
	default:
		return m, nil
	}
}

// capture stores the YAML of the map. Compile's regexp cache is
// not part of that YAML, so a later refresh is not itself an edit.
func (m *Model) capture() {
	raw, err := importer.Marshal(m.fm, ".yaml")
	if err != nil {
		m.baseline = nil
		return
	}
	m.baseline = raw
}

// dirty reports a map that would write differently from the one the
// screen opened with. A marshal failure asks rather than dropping
// a map that could not be compared.
func (m Model) dirty() bool {
	raw, err := importer.Marshal(m.fm, ".yaml")
	if err != nil || m.baseline == nil {
		return true
	}
	return !bytes.Equal(raw, m.baseline)
}

func (m Model) onEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenBoard
		return m, nil
	case "up", "k":
		m.editIdx--
		m.clampEdit()
	case "down", "j":
		m.editIdx++
		m.clampEdit()
	case "]", "tab":
		m.cycleStep()
	case "a":
		m.addStep()
	case "d":
		m.dropStep()
	case "e":
		f := m.field(m.editTarget)
		if f == nil || len(f.Transforms) == 0 {
			m.note = "no step to edit"
			return m, nil
		}
		step := f.Transforms[m.editIdx]
		if step.Name == "map" && m.openLookup(step) {
			return m, nil
		}
		cmd := m.ask(promptStep, stepArg(step), stepHint(step.Name))
		return m, cmd
	case "n":
		f := m.field(m.editTarget)
		cur := ""
		if f != nil {
			cur = strings.Join(f.NullIf, ", ")
		}
		cmd := m.ask(promptNullIf, cur, "N/A, NULL")
		return m, cmd
	case "f":
		f := m.field(m.editTarget)
		cur := ""
		if f != nil {
			cur = f.Format
		}
		cmd := m.ask(promptFormat, cur, "2006-01-02")
		return m, cmd
	case "b":
		f := m.field(m.editTarget)
		cur := ""
		if f != nil && f.Default != nil {
			cur = *f.Default
		}
		cmd := m.ask(promptDefault, cur, "used only when the value is null")
		return m, cmd
	case "v":
		m.screen = screenCheck
	case "s":
		return m, m.saveCmd()
	}
	return m, nil
}

func (m Model) onPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.commitPrompt()
		return m, nil
	case "esc":
		m.prompt = promptNone
		m.note = ""
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

// ask opens the one-line editor. The blink command is what shows the
// cursor; without it the prompt looks like a label.
func (m *Model) ask(kind promptKind, value, placeholder string) tea.Cmd {
	m.prompt = kind
	m.input = textinput.New()
	m.input.Prompt = "> "
	m.input.Placeholder = placeholder
	m.input.SetValue(value)
	w := m.width - 8
	if w < 20 {
		w = 20
	}
	if w > 60 {
		w = 60
	}
	m.input.Width = w
	m.input.Focus()
	m.note = ""
	return textinput.Blink
}

func (m *Model) commitPrompt() {
	text := m.input.Value()
	kind := m.prompt
	m.prompt = promptNone
	switch kind {
	case promptConst:
		m.setConstant(text)
	case promptStep:
		m.applyStepArg(text)
	case promptNullIf:
		m.setNullIf(text)
	case promptFormat:
		m.setFormat(text)
	case promptDefault:
		m.setDefault(text)
	case promptLookupKey:
		m.addLookupKey(text)
	}
}

func (m *Model) refresh() {
	_, m.diags = importer.Compile(m.sch, m.fm)
}

func (m *Model) placeCursor() {
	m.tgt = 0
	for i, col := range m.sch.Columns {
		if m.omitted(col.Name) {
			continue
		}
		m.tgt = i
		break
	}
	if f := m.field(m.column().Name); f != nil && f.Source != "" {
		for i, name := range m.sourceNames() {
			if name == f.Source {
				m.src = i
				break
			}
		}
	}
}

func (m *Model) move(d int) {
	if m.focus == focusSource {
		m.src += d
		m.clampSrc()
		return
	}
	m.tgt += d
	m.clampTgt()
}

func (m *Model) clampSrc() {
	n := len(m.sourceNames())
	if n == 0 {
		m.src = 0
		return
	}
	if m.src < 0 {
		m.src = 0
	}
	if m.src >= n {
		m.src = n - 1
	}
}

func (m *Model) clampTgt() {
	n := len(m.sch.Columns)
	if n == 0 {
		m.tgt = 0
		return
	}
	if m.tgt < 0 {
		m.tgt = 0
	}
	if m.tgt >= n {
		m.tgt = n - 1
	}
}

func (m *Model) clampEdit() {
	f := m.field(m.editTarget)
	n := 0
	if f != nil {
		n = len(f.Transforms)
	}
	if n == 0 {
		m.editIdx = 0
		return
	}
	if m.editIdx < 0 {
		m.editIdx = 0
	}
	if m.editIdx >= n {
		m.editIdx = n - 1
	}
}

func (m Model) sourceNames() []string {
	if len(m.sample.Fields) > 0 {
		return m.sample.Fields
	}
	names := make([]string, len(m.sample.Indexes))
	for i, n := range m.sample.Indexes {
		names[i] = strconv.Itoa(n)
	}
	return names
}

func (m Model) column() schema.Column {
	if m.tgt < 0 || m.tgt >= len(m.sch.Columns) {
		return schema.Column{}
	}
	return m.sch.Columns[m.tgt]
}

func (m Model) columnBy(name string) schema.Column {
	for _, col := range m.sch.Columns {
		if col.Name == name {
			return col
		}
	}
	return schema.Column{Name: name}
}

func (m Model) field(name string) *importer.Field {
	for i := range m.fm.Fields {
		if m.fm.Fields[i].Target == name {
			return &m.fm.Fields[i]
		}
	}
	return nil
}

func (m Model) focusedField() *importer.Field {
	return m.field(m.column().Name)
}

func (m Model) omitted(name string) bool {
	for _, n := range m.fm.Omitted {
		if n == name {
			return true
		}
	}
	return false
}

// unfinished is the seeded map step: outputs were proposed, and no
// sample value has been pointed at one of them yet.
func (m Model) unfinished() bool {
	for _, d := range m.diags {
		if errors.Is(d.Err, importer.ErrUnfinishedLookup) {
			return true
		}
	}
	return false
}

func (m Model) diagOn(name string) bool {
	for _, d := range m.diags {
		if d.Column == name {
			return true
		}
	}
	return false
}

// link pairs the highlighted source with the highlighted target and
// starts that target on the same chain Propose would have written.
func (m *Model) link() {
	names := m.sourceNames()
	if m.src < 0 || m.src >= len(names) {
		m.note = "no source selected"
		return
	}
	col := m.column()
	if col.Name == "" {
		return
	}
	if col.Identity {
		m.note = col.Name + " is identity and cannot be targeted"
		return
	}
	src := names[m.src]
	m.unomit(col.Name)
	f := m.ensure(col)
	f.Source = src
	f.Sources = nil
	f.Join = nil
	f.Coalesce = nil
	f.Constant = nil
	m.note = col.Name + " ← " + src
	m.refresh()
}

// ignoreSource drops a single-source link. The source stays in the
// header; unmapped.source is what says an unused column is not an error.
func (m *Model) ignoreSource() {
	names := m.sourceNames()
	if m.src < 0 || m.src >= len(names) {
		return
	}
	src := names[m.src]
	var kept []importer.Field
	n := 0
	for _, f := range m.fm.Fields {
		if f.Source == src {
			n++
			continue
		}
		kept = append(kept, f)
	}
	m.fm.Fields = kept
	if n == 0 {
		m.note = src + " is already unmapped"
	} else {
		m.note = "ignored " + src
	}
	m.refresh()
}

func (m *Model) setConstant(text string) {
	col := m.column()
	if col.Name == "" {
		return
	}
	if col.Identity {
		m.note = col.Name + " is identity and cannot be targeted"
		return
	}
	m.unomit(col.Name)
	f := m.ensure(col)
	v := text
	f.Constant = &v
	f.Source = ""
	f.Sources = nil
	f.Join = nil
	f.Coalesce = nil
	m.note = col.Name + " is a constant"
	m.refresh()
}

// toggleOmit moves a legal target onto omitted, or back off it. A
// NOT NULL column with no default stays mapped: omitting it would
// make every row fail, which is what ErrBadOmitted is for.
func (m *Model) toggleOmit() {
	col := m.column()
	if col.Name == "" {
		return
	}
	if col.Identity {
		m.dropTarget(col.Name)
		m.setOmitted(col.Name, true)
		m.note = col.Name + " stays off the insert"
		m.refresh()
		return
	}
	if m.omitted(col.Name) {
		m.setOmitted(col.Name, false)
		m.note = "un-omitted " + col.Name
		m.refresh()
		return
	}
	if !canOmit(col) {
		m.note = col.Name + " cannot be omitted"
		return
	}
	m.dropTarget(col.Name)
	m.setOmitted(col.Name, true)
	m.note = "omitted " + col.Name
	m.refresh()
}

func canOmit(col schema.Column) bool {
	return col.Identity || col.Nullable || col.Default != ""
}

func (m *Model) openEdit() {
	col := m.column()
	f := m.field(col.Name)
	if f == nil {
		m.note = col.Name + " has no chain"
		return
	}
	m.editTarget = col.Name
	m.editIdx = 0
	m.screen = screenEdit
	m.note = ""
}

func (m *Model) ensure(col schema.Column) *importer.Field {
	if f := m.field(col.Name); f != nil {
		return f
	}
	m.fm.Fields = append(m.fm.Fields, importer.Field{
		Target:     col.Name,
		Transforms: importer.SeedTransforms(col),
	})
	return &m.fm.Fields[len(m.fm.Fields)-1]
}

func (m *Model) dropTarget(name string) {
	kept := m.fm.Fields[:0]
	for _, f := range m.fm.Fields {
		if f.Target != name {
			kept = append(kept, f)
		}
	}
	m.fm.Fields = kept
}

func (m *Model) setOmitted(name string, on bool) {
	var next []string
	for _, n := range m.fm.Omitted {
		if n != name {
			next = append(next, n)
		}
	}
	if on {
		next = append(next, name)
	}
	m.fm.Omitted = next
}

func (m *Model) unomit(name string) {
	if m.omitted(name) {
		m.setOmitted(name, false)
	}
}

func (m *Model) addStep() {
	f := m.field(m.editTarget)
	if f == nil {
		return
	}
	f.Transforms = append(f.Transforms, freshStep("trim", m.columnBy(m.editTarget)))
	m.editIdx = len(f.Transforms) - 1
	m.note = "added trim"
	m.refresh()
}

func (m *Model) dropStep() {
	f := m.field(m.editTarget)
	if f == nil || len(f.Transforms) == 0 {
		m.note = "no step to drop"
		return
	}
	i := m.editIdx
	f.Transforms = append(f.Transforms[:i], f.Transforms[i+1:]...)
	m.clampEdit()
	m.note = "dropped step"
	m.refresh()
}

func (m *Model) cycleStep() {
	f := m.field(m.editTarget)
	if f == nil || len(f.Transforms) == 0 {
		m.addStep()
		return
	}
	cur := f.Transforms[m.editIdx].Name
	next := stepOrder[0]
	for i, name := range stepOrder {
		if name == cur {
			next = stepOrder[(i+1)%len(stepOrder)]
			break
		}
	}
	f.Transforms[m.editIdx] = freshStep(next, m.columnBy(m.editTarget))
	m.note = "step is " + next
	m.refresh()
}

func (m *Model) applyStepArg(text string) {
	f := m.field(m.editTarget)
	if f == nil || m.editIdx < 0 || m.editIdx >= len(f.Transforms) {
		return
	}
	t := f.Transforms[m.editIdx]
	if err := parseStepArg(&t, text); err != nil {
		m.note = err.Error()
		return
	}
	f.Transforms[m.editIdx] = t
	m.note = "updated " + t.Name
	m.refresh()
}

func (m *Model) setNullIf(text string) {
	f := m.field(m.editTarget)
	if f == nil {
		return
	}
	f.NullIf = splitList(text)
	m.note = "updated null_if"
	m.refresh()
}

func (m *Model) setFormat(text string) {
	f := m.field(m.editTarget)
	if f == nil {
		return
	}
	f.Format = strings.TrimSpace(text)
	m.note = "updated format"
	m.refresh()
}

func (m *Model) setDefault(text string) {
	f := m.field(m.editTarget)
	if f == nil {
		return
	}
	if strings.TrimSpace(text) == "" {
		f.Default = nil
		m.note = "cleared default"
	} else {
		v := text
		f.Default = &v
		m.note = "updated default"
	}
	m.refresh()
}

// openLookup lists every value the schema allows for this column.
// Sample cells that already equal one of those values are placed on
// that row. Anything else waits in the pool, because guessing that
// "a" means "active" would hide a real decision.
func (m *Model) openLookup(t importer.Transform) bool {
	choices := append([]string(nil), t.Accept...)
	if len(choices) == 0 {
		choices = append(choices, m.columnBy(m.editTarget).EnumValues...)
	}
	seen := map[string]bool{}
	for _, c := range choices {
		seen[c] = true
	}
	for _, p := range t.Pairs {
		if p.To != "" && !seen[p.To] {
			seen[p.To] = true
			choices = append(choices, p.To)
		}
	}
	if len(choices) == 0 {
		return false
	}
	keys := make([][]string, len(choices))
	index := map[string]int{}
	for i, c := range choices {
		index[c] = i
	}
	used := map[string]bool{}
	for _, p := range t.Pairs {
		i, ok := index[p.To]
		if !ok || p.From == "" || used[p.From] {
			continue
		}
		used[p.From] = true
		keys[i] = append(keys[i], p.From)
	}
	var pool []string
	if f := m.field(m.editTarget); f != nil && f.Source != "" {
		for _, row := range m.sample.Preview {
			cell := m.cell(row, f.Source)
			if cell == "" || used[cell] {
				continue
			}
			used[cell] = true
			if i, ok := index[cell]; ok {
				keys[i] = append(keys[i], cell)
				continue
			}
			pool = append(pool, cell)
		}
	}
	m.lookupChoices = choices
	m.lookupKeys = keys
	m.lookupPool = pool
	m.lookupIdx = 0
	m.screen = screenLookup
	m.note = ""
	return true
}

func (m Model) onLookup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenEdit
		return m, nil
	case "up", "k":
		m.lookupIdx--
		m.clampLookup()
	case "down", "j":
		m.lookupIdx++
		m.clampLookup()
	case " ":
		m.placeSample()
	case "x":
		m.unplaceSample()
	case "n":
		cmd := m.ask(promptLookupKey, "", "code a later file might hold")
		return m, cmd
	case "enter":
		m.commitLookup()
	}
	return m, nil
}

func (m *Model) clampLookup() {
	n := len(m.lookupChoices)
	if n == 0 {
		m.lookupIdx = 0
		return
	}
	if m.lookupIdx < 0 {
		m.lookupIdx = 0
	}
	if m.lookupIdx >= n {
		m.lookupIdx = n - 1
	}
}

func (m *Model) placeSample() {
	m.clampLookup()
	if len(m.lookupPool) == 0 {
		m.note = "no sample value left to place"
		return
	}
	code := m.lookupPool[0]
	m.lookupPool = m.lookupPool[1:]
	m.lookupKeys[m.lookupIdx] = append(m.lookupKeys[m.lookupIdx], code)
	m.note = code + " → " + m.lookupChoices[m.lookupIdx]
}

func (m *Model) unplaceSample() {
	m.clampLookup()
	keys := m.lookupKeys[m.lookupIdx]
	if len(keys) == 0 {
		m.note = "nothing to remove"
		return
	}
	last := keys[len(keys)-1]
	m.lookupKeys[m.lookupIdx] = keys[:len(keys)-1]
	m.lookupPool = append(m.lookupPool, last)
	m.note = "removed " + last
}

// addLookupKey attaches a code the sample never contained. The schema
// row is already selected, so a future file can be described before
// any of its rows exist.
func (m *Model) addLookupKey(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		m.note = "empty code"
		return
	}
	m.clampLookup()
	for i, keys := range m.lookupKeys {
		for _, key := range keys {
			if key == text {
				m.note = text + " is already on " + m.lookupChoices[i]
				return
			}
		}
	}
	kept := m.lookupPool[:0]
	for _, code := range m.lookupPool {
		if code != text {
			kept = append(kept, code)
		}
	}
	m.lookupPool = kept
	m.lookupKeys[m.lookupIdx] = append(m.lookupKeys[m.lookupIdx], text)
	m.note = text + " → " + m.lookupChoices[m.lookupIdx]
}

// commitLookup writes one pair per placed code and drops the seeded
// to: list. Sample values left in the pool would miss at import, so
// enter refuses those. A schema value with no code is kept as a
// warning instead, because this file may simply never use it.
func (m *Model) commitLookup() {
	f := m.field(m.editTarget)
	if f == nil || m.editIdx < 0 || m.editIdx >= len(f.Transforms) {
		m.screen = screenEdit
		return
	}
	if len(m.lookupPool) > 0 {
		m.note = m.lookupPool[0] + " is in the sample and has no schema value"
		return
	}
	var pairs []importer.Pair
	for i, out := range m.lookupChoices {
		for _, from := range m.lookupKeys[i] {
			pairs = append(pairs, importer.Pair{From: from, To: out})
		}
	}
	f.Transforms[m.editIdx].Pairs = pairs
	f.Transforms[m.editIdx].Accept = nil
	m.screen = screenEdit
	m.note = "lookup set"
	m.refresh()
}

// enumGaps lists schema values no incoming key produces. An unfinished
// to: list is already a compile error, so it is not warned twice.
func (m Model) enumGaps() []string {
	var out []string
	for _, f := range m.fm.Fields {
		col := m.columnBy(f.Target)
		if len(col.EnumValues) == 0 {
			continue
		}
		for _, step := range f.Transforms {
			if step.Name != "map" || (len(step.Accept) > 0 && len(step.Pairs) == 0) {
				continue
			}
			covered := map[string]bool{}
			for _, p := range step.Pairs {
				covered[p.To] = true
			}
			for _, v := range col.EnumValues {
				if !covered[v] {
					out = append(out, f.Target+"  "+v+" has no incoming key")
				}
			}
		}
	}
	return out
}

func freshStep(name string, col schema.Column) importer.Transform {
	t := importer.Transform{Name: name}
	switch name {
	case "case":
		t.Case = "lower"
	case "replace":
		// An empty from is ErrEmptyReplace. A hyphen is a real
		// argument the person can overwrite.
		t.From = "-"
	case "regex_replace":
		t.Pattern = ".+"
	case "map":
		if len(col.EnumValues) > 0 {
			t.Accept = append([]string(nil), col.EnumValues...)
		}
	case "pad_left", "pad_right":
		t.Left = name == "pad_left"
		t.Width = 1
		t.With = " "
	}
	return t
}

func parseStepArg(t *importer.Transform, text string) error {
	text = strings.TrimSpace(text)
	switch t.Name {
	case "trim":
		t.HasCutset = text != ""
		t.Cutset = text
	case "case":
		switch text {
		case "upper", "lower", "title":
			t.Case = text
		default:
			return fmt.Errorf("case must be upper, lower, or title")
		}
	case "replace", "regex_replace":
		from, to, ok := strings.Cut(text, "->")
		if !ok {
			return fmt.Errorf("use from -> to")
		}
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if t.Name == "replace" {
			if from == "" {
				return fmt.Errorf("replace from is empty")
			}
			t.From = from
			t.To = to
			return nil
		}
		t.Pattern = from
		t.To = to
	case "map":
		return parseLookup(t, text)
	case "pad_left", "pad_right":
		width, with, ok := strings.Cut(text, " ")
		if !ok || strings.TrimSpace(with) == "" {
			return fmt.Errorf("use width fill")
		}
		n, err := strconv.Atoi(strings.TrimSpace(width))
		if err != nil || n < 1 {
			return fmt.Errorf("pad width must be >= 1")
		}
		t.Width = n
		t.With = with
		t.Left = t.Name == "pad_left"
	default:
		return fmt.Errorf("unknown step %s", t.Name)
	}
	return nil
}

func parseLookup(t *importer.Transform, text string) error {
	if strings.HasPrefix(text, "to:") {
		t.Pairs = nil
		t.Accept = splitList(strings.TrimPrefix(text, "to:"))
		t.LookupDefault = nil
		return nil
	}
	var pairs []importer.Pair
	var def *string
	for _, part := range splitList(text) {
		if strings.HasPrefix(part, "default:") {
			v := strings.TrimSpace(strings.TrimPrefix(part, "default:"))
			def = &v
			continue
		}
		from, to, ok := strings.Cut(part, ":")
		if !ok || strings.TrimSpace(from) == "" {
			return fmt.Errorf("use from: to, from: to")
		}
		pairs = append(pairs, importer.Pair{
			From: strings.TrimSpace(from),
			To:   strings.TrimSpace(to),
		})
	}
	t.Pairs = pairs
	t.Accept = nil
	t.LookupDefault = def
	return nil
}

func splitList(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func currentConst(f *importer.Field) string {
	if f == nil || f.Constant == nil {
		return ""
	}
	return *f.Constant
}

func stepArg(t importer.Transform) string {
	switch t.Name {
	case "trim":
		if t.HasCutset {
			return t.Cutset
		}
		return ""
	case "case":
		return t.Case
	case "replace":
		return t.From + " -> " + t.To
	case "regex_replace":
		return t.Pattern + " -> " + t.To
	case "map":
		if len(t.Accept) > 0 {
			return "to: " + strings.Join(t.Accept, ", ")
		}
		parts := make([]string, 0, len(t.Pairs)+1)
		for _, p := range t.Pairs {
			parts = append(parts, p.From+": "+p.To)
		}
		if t.LookupDefault != nil {
			parts = append(parts, "default: "+*t.LookupDefault)
		}
		return strings.Join(parts, ", ")
	case "pad_left", "pad_right":
		return fmt.Sprintf("%d %s", t.Width, t.With)
	default:
		return ""
	}
}

func stepHint(name string) string {
	switch name {
	case "trim":
		return "cutset, empty trims whitespace"
	case "case":
		return "upper, lower, or title"
	case "replace", "regex_replace":
		return "from -> to"
	case "map":
		return "a: active, s: suspended"
	case "pad_left", "pad_right":
		return "width fill"
	default:
		return ""
	}
}
