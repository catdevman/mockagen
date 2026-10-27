package mapui

import (
	"path/filepath"
	"strings"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
)

// Ask is the set of file flags the screen still has to collect. The
// command loads any path the user already passed. An empty one is a
// picker step. -out is not here: the picker can only select a file
// that already exists, and the map usually does not.
type Ask struct {
	SchemaPath string
	DataPath   string
	MapPath    string
	SampleN    int
	NeedSchema bool
	NeedData   bool
	NeedMap    bool
	// Dir is the first picker's directory. Empty starts at the process
	// working directory, which is where the user ran the command.
	// Tests set it so the picker does not read the repo.
	Dir string
	// Base is a field map already loaded from -map. MapFromCLI means
	// a header mismatch quits for the command to report, instead of
	// leaving the user on a picker they did not open.
	Base       *importer.FieldMap
	MapFromCLI bool
}

type pickStep int

const (
	pickSchema pickStep = iota
	pickData
	pickMap
)

func (a Ask) steps() []pickStep {
	var steps []pickStep
	if a.NeedSchema {
		steps = append(steps, pickSchema)
	}
	if a.NeedData {
		steps = append(steps, pickData)
	}
	if a.NeedMap {
		steps = append(steps, pickMap)
	}
	return steps
}

// firstDir is where the first picker opens. A path the user already
// passed is a better start than the working directory: the other file
// is usually next to it.
func (a Ask) firstDir() string {
	if a.Dir != "" {
		return a.Dir
	}
	switch {
	case a.NeedSchema && a.DataPath != "":
		return dirOrDot(a.DataPath)
	case a.NeedSchema && a.MapPath != "":
		return dirOrDot(a.MapPath)
	case a.NeedSchema:
		return "."
	case a.NeedData:
		return dirOrDot(a.SchemaPath)
	default:
		if a.DataPath != "" {
			return dirOrDot(a.DataPath)
		}
		return dirOrDot(a.SchemaPath)
	}
}

func dirOrDot(path string) string {
	if path == "" {
		return "."
	}
	d := filepath.Dir(path)
	if d == "" || d == "." {
		return "."
	}
	return d
}

type pickedSchemaMsg struct {
	path string
	sch  schema.Schema
	err  error
}

type pickedDataMsg struct {
	path   string
	sample importer.Sample
	err    error
}

type pickedMapMsg struct {
	path string
	fm   importer.FieldMap
	err  error
}

func (m *Model) installAsk(a Ask, sess Session) {
	m.sch = sess.Schema
	m.sample = sess.Sample
	m.sampleN = a.SampleN
	m.schemaPath = a.SchemaPath
	m.pickSteps = a.steps()
	m.mapFromCLI = a.MapFromCLI
	if a.Base != nil {
		m.base = *a.Base
		m.haveBase = true
	}
}

// openPicker arms the bubbles picker. The directory read is the
// returned command, because Update does not touch the disk. esc stays
// "parent directory" inside the picker, so cancel is q.
func (m Model) openPicker(dir string) (Model, tea.Cmd) {
	fp := filepicker.New()
	fp.CurrentDirectory = dir
	fp.ShowPermissions = false
	fp.ShowSize = false
	fp.ShowHidden = false
	fp.AutoHeight = false
	fp.Styles = filepicker.DefaultStylesWithRenderer(m.r)
	fp.Styles.EmptyDirectory = m.styles.faint.SetString("empty directory")
	fp.SetHeight(m.pickHeight())
	m.picker = fp
	m.screen = screenPick
	return m, fp.Init()
}

func (m Model) pickHeight() int {
	h := m.height - 6
	if h < 5 {
		h = 5
	}
	return h
}

func (m Model) onPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// q leaves before any map exists. The board's quit question would
	// be about a file the user has not started.
	if msg.String() == "q" {
		return m, tea.Quit
	}
	if m.busy {
		return m, nil
	}
	if msg.String() == "s" && m.onMapStep() {
		m.haveBase = false
		return m.openBoard()
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	if did, path := m.picker.DidSelectFile(msg); did {
		m.busy = true
		m.note = "reading " + path
		return m, m.loadPicked(path)
	}
	return m, cmd
}

func (m Model) onMapStep() bool {
	return m.pickIdx < len(m.pickSteps) && m.pickSteps[m.pickIdx] == pickMap
}

func (m Model) loadPicked(path string) tea.Cmd {
	step := m.pickSteps[m.pickIdx]
	n := m.sampleN
	return func() tea.Msg {
		switch step {
		case pickSchema:
			sch, err := schema.Load(path)
			return pickedSchemaMsg{path: path, sch: sch, err: err}
		case pickData:
			sample, err := importer.ReadSample(path, n)
			return pickedDataMsg{path: path, sample: sample, err: err}
		default:
			fm, err := importer.Load(path)
			return pickedMapMsg{path: path, fm: fm, err: err}
		}
	}
}

func (m Model) onPickedSchema(msg pickedSchemaMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if msg.err != nil {
		m.note = msg.err.Error()
		return m, nil
	}
	m.sch = msg.sch
	m.schemaPath = msg.path
	m.lastPick = msg.path
	m.note = ""
	return m.advance()
}

func (m Model) onPickedData(msg pickedDataMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if msg.err != nil {
		m.note = msg.err.Error()
		return m, nil
	}
	m.sample = msg.sample
	m.lastPick = msg.path
	if m.haveBase && !importer.HeaderMatches(m.base.Source, m.sample) {
		// -map was a flag, so the mismatch is the command's to
		// report and to write unchanged. A map the user just
		// picked stays on this screen so they can choose another.
		if m.mapFromCLI {
			m.fm = m.base
			m.drift = true
			return m, tea.Quit
		}
		m.note = driftNote(m.base.Source, m.sample)
		return m, nil
	}
	m.note = ""
	return m.advance()
}

func (m Model) onPickedMap(msg pickedMapMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	if msg.err != nil {
		m.note = msg.err.Error()
		return m, nil
	}
	if !importer.HeaderMatches(msg.fm.Source, m.sample) {
		m.note = driftNote(msg.fm.Source, m.sample)
		return m, nil
	}
	m.base = msg.fm
	m.haveBase = true
	m.lastPick = msg.path
	m.note = ""
	return m.advance()
}

func driftNote(spec importer.SourceSpec, sample importer.Sample) string {
	return "header does not match: map " + strings.Join(spec.Fields, ",") +
		", data " + strings.Join(sample.Fields, ",")
}

func (m Model) advance() (tea.Model, tea.Cmd) {
	m.pickIdx++
	if m.pickIdx >= len(m.pickSteps) {
		return m.openBoard()
	}
	dir := filepath.Dir(m.lastPick)
	if dir == "" {
		dir = "."
	}
	return m.openPicker(dir)
}

// openBoard proposes from whatever the pickers loaded. The schema path
// is pinned here so an s on the board writes the same relative path the
// command would have written.
func (m Model) openBoard() (tea.Model, tea.Cmd) {
	var base *importer.FieldMap
	if m.haveBase {
		cp := m.base
		base = &cp
	}
	m.fm = importer.Propose(m.sch, m.sample, base)
	m.fm.Schema = importer.SchemaRef(m.outPath, m.schemaPath)
	m.screen = screenBoard
	m.focus = focusTarget
	m.placeCursor()
	m.refresh()
	m.capture()
	return m, nil
}

func (m Model) pickTitle() string {
	if m.pickIdx >= len(m.pickSteps) {
		return "file"
	}
	switch m.pickSteps[m.pickIdx] {
	case pickSchema:
		return "schema file"
	case pickData:
		return "data file"
	default:
		return "existing field map"
	}
}
