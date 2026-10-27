// Package schemaui is the bubbletea screen for `mockagen schema`. Update
// does no I/O. Every dial, query, and file write is a tea.Cmd so a test
// can feed the message the command would have returned.
package schemaui

import (
	"context"
	"strings"
	"time"

	"github.com/catdevman/mockagen/pkg/mockagen/types"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/catdevman/mockagen/pkg/schema/introspect"
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// dialTimeout is the same bound the command line uses for one dial. A
// black-holed address has to fail the screen's command, not hold it.
const dialTimeout = 30 * time.Second

// previewCap is how many rows the preview command asks for. Five matches
// the live sample and is small enough that generateFakes stays a peek.
const previewCap = 5

// dialects is the order j/k walks. It is the four engines the catalog
// queries know; an alias is canonicalised before it is selected.
var dialects = []string{"postgres", "mysql", "mssql", "sqlite"}

// Services are the dial, query, and write functions the command injects.
// The screen calls them from tea.Cmd callbacks, never from Update, so a
// model test can substitute a fake or feed the message directly.
type Services struct {
	Tables          func(ctx context.Context, dialect, dsn string) ([]introspect.Table, error)
	Inspect         func(ctx context.Context, dialect, dsn, schemaName, table string) (schema.Schema, error)
	ColumnSample    func(ctx context.Context, dialect, dsn, table string, cols []string) ([][]string, error)
	Preview         func(s schema.Schema, n int) ([][]string, error)
	SaveSchema      func(path string, s schema.Schema) error
	SaveSpec        func(path string, s schema.Schema) error
	LoadConnections func(path string) ([]Connection, error)
	SaveConnections func(path string, cs []Connection) error
}

// Session is what the command already knows. Connections were loaded
// before the screen opened, so Update never reads the file. EnvDSN is
// set when MOCKAGEN_DSN or DATABASE_URL is present; EnvName is the
// variable the view prints, because the value is a secret.
type Session struct {
	Services        Services
	Connections     []Connection
	ConnectionsPath string
	EnvDSN          string
	EnvName         string
	Dialect         string
	DSN             string
	OutPath         string
	RenderPath      string
}

// screen is one of the five steps. esc walks them backwards; q leaves
// without writing.
type screen int

const (
	screenConnect screen = iota
	screenTables
	screenColumns
	screenPreview
	screenSave
)

// connectFocus is which control j/k and typing apply to. The DSN field
// has to take every rune, including j and q, so those keys only move
// the list when the field is not focused.
type connectFocus int

const (
	focusDialect connectFocus = iota
	focusDSN
	focusSaved
	focusEnv
)

// promptKind is the one-line question in front of the screen. The type
// prompts are driven by types.Type's Requires fields, not by the type
// name, so a new catalog entry does not need a screen change.
type promptKind int

const (
	promptNone promptKind = iota
	promptConnName
	promptConnPass
	promptValues
	promptMin
	promptMax
	promptFormat
)

// tablesMsg is the table list for the picker. The command test
// type-switches this; the model test feeds it and does not dial.
type tablesMsg struct {
	tables []introspect.Table
	err    error
}

// schemasMsg is one inferred schema per selected table. A failure leaves
// the table list up; the columns screen is only the success path.
type schemasMsg struct {
	schemas []schema.Schema
	err     error
}

// sampleMsg is column name to one short cell. The screen does not infer
// a type from it: inference already ran, and a sample is a hint.
type sampleMsg struct {
	table  string
	values map[string]string
	err    error
}

// previewMsg is the generated grid, expanded to the full column width.
// Unresolved columns are blank here; the view prints UNRESOLVED itself
// so a generator cannot paper over them.
type previewMsg struct {
	rows [][]string
	err  error
}

// savedMsg is the paths the save command wrote. The model test feeds it
// and does not touch the disk.
type savedMsg struct {
	paths []string
	err   error
}

// connSavedMsg replaces the saved-connection list. The password is not
// in it: the command wrote password_env, never the value.
type connSavedMsg struct {
	connections []Connection
	err         error
}

// Model is the schema screen. The password is not a field: a dial
// command reads the environment variable and splices a copy.
type Model struct {
	services Services
	connPath string
	conns    []Connection
	hasEnv   bool
	envName  string
	// envDSN is set only when the session had a value and no variable
	// name to re-read. The view never prints it.
	envDSN string

	r      *lipgloss.Renderer
	styles styles
	width  int
	height int

	screen screen
	note   string
	// saved is the "state says saved" bit the save message sets. q does
	// not set it, and it does not by itself write a file.
	saved bool

	dialects   []string
	dialectIdx int
	focus      connectFocus
	dsnInput   textinput.Model
	savedIdx   int

	dialect     string
	dsn         string
	passwordEnv string
	useEnv      bool

	prompt      promptKind
	input       textinput.Model
	promptQueue []promptKind
	pendingName string

	tables      []introspect.Table
	tableCursor int
	selected    map[string]bool
	filter      textinput.Model
	filtering   bool

	schemas   []schema.Schema
	schemaIdx int
	colIdx    int
	onStrip   bool
	sampleOff bool
	samples   map[string]map[string]string
	sampleErr map[string]error

	picking       bool
	pick          []types.Type
	pickIdx       int
	pendingType   types.Type
	pendingValues string
	pendingMin    string
	pendingMax    string
	pendingFormat string

	preview [][]string

	saveFrom      screen
	pathInput     textinput.Model
	renderInput   textinput.Model
	saveField     int
	saveEditing   bool
	outDefault    string
	renderDefault string
}

type styles struct {
	title    lipgloss.Style
	faint    lipgloss.Style
	selected lipgloss.Style
	bad      lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	return styles{
		title:    r.NewStyle().Bold(true),
		faint:    r.NewStyle().Faint(true),
		selected: r.NewStyle().Bold(true),
		bad:      r.NewStyle().Foreground(lipgloss.Color("1")),
	}
}

// New builds a screen from a session. The renderer is the process
// default so a real terminal keeps its color profile; tests replace it.
func New(sess Session) Model {
	m := Model{
		services:      sess.Services,
		connPath:      sess.ConnectionsPath,
		conns:         append([]Connection(nil), sess.Connections...),
		hasEnv:        sess.EnvDSN != "",
		envName:       sess.EnvName,
		r:             lipgloss.DefaultRenderer(),
		width:         80,
		height:        24,
		dialects:      dialects,
		selected:      map[string]bool{},
		samples:       map[string]map[string]string{},
		sampleErr:     map[string]error{},
		sampleOff:     sess.Services.ColumnSample == nil,
		outDefault:    sess.OutPath,
		renderDefault: sess.RenderPath,
	}
	if sess.EnvName == "" && sess.EnvDSN != "" {
		m.envDSN = sess.EnvDSN
	}
	m.styles = newStyles(m.r)
	m.dsnInput = m.newInput("postgres://user@localhost:5432/db")
	m.dsnInput.SetValue(sess.DSN)
	m.filter = m.newInput("filter")
	m.pathInput = m.newInput("schema path")
	m.renderInput = m.newInput("spec path, blank skips")
	m.input = m.newInput("")
	if sess.Dialect != "" {
		name := strings.ToLower(strings.TrimSpace(sess.Dialect))
		for i, d := range m.dialects {
			if d == name {
				m.dialectIdx = i
				break
			}
		}
	}
	m.layoutInputs()
	return m
}

// Run opens the screen on the alt screen and blocks until quit. Saves
// already happened inside commands; a nil error is a quit with or
// without a write.
func Run(sess Session) error {
	p := tea.NewProgram(New(sess), tea.WithAltScreen())
	_, err := p.Run()
	if err != nil {
		return err
	}
	return nil
}

// Init does not start a blink. Nothing is focused until the user tabs
// onto a field, and a timer the first frame does not read is noise.
func (m Model) Init() tea.Cmd { return nil }

// Update applies one message. Dial, query, and write stay in the
// command that produced the message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.layoutInputs()
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	case tablesMsg:
		if msg.err != nil {
			m.note = msg.err.Error()
			return m, nil
		}
		m.tables = msg.tables
		m.tableCursor = 0
		m.selected = map[string]bool{}
		m.screen = screenTables
		m.filtering = false
		m.note = ""
		return m, nil
	case schemasMsg:
		if msg.err != nil {
			m.note = msg.err.Error()
			return m, nil
		}
		m.schemas = msg.schemas
		m.schemaIdx = 0
		m.colIdx = 0
		m.onStrip = false
		m.clearPick()
		m.screen = screenColumns
		m.note = ""
		return m, m.sampleCmd()
	case sampleMsg:
		// Stored beside the column. The type is left alone on purpose:
		// a sample that looks like an email is not a reason to overwrite
		// inferred_by.
		if msg.err != nil {
			m.sampleErr[msg.table] = msg.err
			m.note = msg.err.Error()
			return m, nil
		}
		delete(m.sampleErr, msg.table)
		m.samples[msg.table] = msg.values
		return m, nil
	case previewMsg:
		if msg.err != nil {
			m.note = msg.err.Error()
			return m, nil
		}
		m.preview = msg.rows
		m.note = ""
		if m.screen == screenColumns || m.screen == screenPreview {
			m.screen = screenPreview
		}
		return m, nil
	case savedMsg:
		if msg.err != nil {
			m.note = msg.err.Error()
			m.saved = false
			return m, nil
		}
		m.saved = true
		m.note = "saved " + strings.Join(msg.paths, " ")
		return m, nil
	case connSavedMsg:
		if msg.err != nil {
			m.note = msg.err.Error()
			return m, nil
		}
		m.conns = msg.connections
		m.note = "saved connection"
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c leaves immediately, including from a field, and writes
	// nothing. A save only happens on enter on the save screen.
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.prompt != promptNone {
		return m.onPrompt(msg)
	}
	if msg.String() == "esc" {
		return m.onEsc()
	}
	// q is a character while a field is focused, because a DSN and a
	// value list both need it. Everywhere else it quits with no write.
	if msg.String() == "q" && !m.typing() {
		return m, tea.Quit
	}
	if m.picking {
		return m.onPick(msg)
	}
	switch m.screen {
	case screenConnect:
		return m.onConnect(msg)
	case screenTables:
		return m.onTables(msg)
	case screenColumns:
		return m.onColumns(msg)
	case screenPreview:
		return m.onPreview(msg)
	case screenSave:
		return m.onSave(msg)
	default:
		return m, nil
	}
}

// typing reports a focused field that must receive runes. q and j are
// letters there, not commands.
func (m Model) typing() bool {
	if m.prompt != promptNone {
		return true
	}
	if m.screen == screenConnect && m.focus == focusDSN {
		return true
	}
	if m.screen == screenTables && m.filtering {
		return true
	}
	if m.screen == screenSave && m.saveEditing {
		return true
	}
	return false
}

func (m Model) onEsc() (tea.Model, tea.Cmd) {
	if m.picking {
		m.clearPick()
		return m, nil
	}
	if m.filtering {
		m.filtering = false
		m.filter.Blur()
		return m, nil
	}
	if m.screen == screenConnect && m.focus == focusDSN {
		m.focus = focusDialect
		m.dsnInput.Blur()
		return m, nil
	}
	if m.screen == screenSave && m.saveEditing {
		m.saveEditing = false
		m.pathInput.Blur()
		m.renderInput.Blur()
		return m, nil
	}
	if m.screen == screenConnect {
		return m, nil
	}
	m.back()
	return m, nil
}

func (m *Model) back() {
	switch m.screen {
	case screenSave:
		m.screen = m.saveFrom
	case screenPreview:
		m.screen = screenColumns
	case screenColumns:
		m.screen = screenTables
	case screenTables:
		m.screen = screenConnect
	}
	m.note = ""
}

func (m Model) onPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.commitPrompt()
	case "esc":
		m.cancelPrompt()
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

func (m *Model) cancelPrompt() {
	kind := m.prompt
	m.prompt = promptNone
	m.input.Blur()
	switch kind {
	case promptValues, promptMin, promptMax, promptFormat:
		m.clearPick()
	default:
		m.pendingName = ""
	}
}

func (m Model) commitPrompt() (tea.Model, tea.Cmd) {
	text := m.input.Value()
	kind := m.prompt
	m.prompt = promptNone
	m.input.Blur()
	switch kind {
	case promptConnName:
		name := strings.TrimSpace(text)
		if name == "" {
			m.note = "not saved: name is required"
			return m, nil
		}
		m.pendingName = name
		return m, m.ask(promptConnPass, "", "password env, empty for sqlite")
	case promptConnPass:
		return m, m.saveConnectionCmd(m.pendingName, strings.TrimSpace(text))
	case promptValues:
		m.pendingValues = text
		return m.advancePrompt()
	case promptMin:
		m.pendingMin = text
		return m.advancePrompt()
	case promptMax:
		m.pendingMax = text
		return m.advancePrompt()
	case promptFormat:
		m.pendingFormat = text
		return m.advancePrompt()
	default:
		return m, nil
	}
}

func (m Model) advancePrompt() (tea.Model, tea.Cmd) {
	if len(m.promptQueue) == 0 {
		m.applyPicked()
		return m, nil
	}
	next := m.promptQueue[0]
	m.promptQueue = m.promptQueue[1:]
	return m, m.ask(next, "", promptLabel(next))
}

// ask opens the one-line editor. The blink command is what shows a
// cursor in a real terminal; tests never run it.
func (m *Model) ask(kind promptKind, value, placeholder string) tea.Cmd {
	m.prompt = kind
	m.input = m.newInput(placeholder)
	m.input.Prompt = "> "
	m.input.SetValue(value)
	m.input.Width = m.fieldWidth()
	m.input.Focus()
	m.note = ""
	return textinput.Blink
}

func promptLabel(k promptKind) string {
	switch k {
	case promptValues:
		return "values"
	case promptMin:
		return "min"
	case promptMax:
		return "max"
	case promptFormat:
		return "format"
	case promptConnName:
		return "name"
	case promptConnPass:
		return "password_env"
	default:
		return ""
	}
}

func (m *Model) newInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.CharLimit = 0
	// Hide the cursor block. Its reverse style is an ANSI sequence on
	// a color profile, and the golden views are plain text.
	ti.Cursor.SetMode(cursor.CursorHide)
	m.restyle(&ti)
	return ti
}

func (m *Model) restyle(ti *textinput.Model) {
	if m.r == nil {
		return
	}
	ti.PromptStyle = m.r.NewStyle()
	ti.TextStyle = m.r.NewStyle()
	ti.PlaceholderStyle = m.r.NewStyle()
	ti.Cursor.Style = m.r.NewStyle()
	ti.Cursor.TextStyle = m.r.NewStyle()
}

// bindRenderer points every style at r. Tests pass an Ascii renderer
// because the process default follows the terminal and would put color
// codes in the golden view.
func (m *Model) bindRenderer(r *lipgloss.Renderer) {
	m.r = r
	m.styles = newStyles(r)
	m.restyle(&m.dsnInput)
	m.restyle(&m.filter)
	m.restyle(&m.input)
	m.restyle(&m.pathInput)
	m.restyle(&m.renderInput)
}

func (m *Model) layoutInputs() {
	w := m.fieldWidth()
	m.dsnInput.Width = w
	m.filter.Width = w
	m.pathInput.Width = w
	m.renderInput.Width = w
	m.input.Width = w
}

func (m Model) fieldWidth() int {
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	if w > 72 {
		w = 72
	}
	return w
}

func (m Model) forward(ti *textinput.Model, msg tea.KeyMsg) tea.Cmd {
	next, cmd := ti.Update(msg)
	*ti = next
	return cmd
}

func inputText(ti textinput.Model) string {
	if ti.Value() != "" {
		return ti.Value()
	}
	return ti.Placeholder
}

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

func (m Model) title() string {
	switch m.screen {
	case screenTables:
		return "mockagen schema   tables"
	case screenColumns:
		return "mockagen schema   columns"
	case screenPreview:
		return "mockagen schema   preview"
	case screenSave:
		return "mockagen schema   save"
	default:
		return "mockagen schema"
	}
}

func (m Model) writeNote(b *strings.Builder) {
	if m.note == "" {
		return
	}
	b.WriteByte('\n')
	b.WriteString(clip(m.note, m.width))
}

func (m Model) help(line string) string {
	return m.styles.faint.Render(clip(line, m.width))
}

// dialTarget is the unspliced dial the next command will open. The
// password stays in the environment until resolve runs inside that
// command.
type dialTarget struct {
	dialect     string
	dsn         string
	passwordEnv string
	useEnv      bool
	envName     string
	envDSN      string
}

func (m Model) target() dialTarget {
	return dialTarget{
		dialect:     m.dialect,
		dsn:         m.dsn,
		passwordEnv: m.passwordEnv,
		useEnv:      m.useEnv,
		envName:     m.envName,
		envDSN:      m.envDSN,
	}
}

func (m Model) withTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dialTimeout)
}
