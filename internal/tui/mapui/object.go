package mapui

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/catdevman/mockagen/pkg/importer"
	"github.com/catdevman/mockagen/pkg/schema"
	tea "github.com/charmbracelet/bubbletea"
)

// isJSONColumn is the gate for the object editor. jsonb[] shares a
// prefix with jsonb and is an array, so the bracket is left in place
// and that column keeps the ordinary chain editor.
func isJSONColumn(col schema.Column) bool {
	t := strings.ToLower(strings.TrimSpace(col.SQLType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.Join(strings.Fields(t), " ")
	return t == "json" || t == "jsonb"
}

// jsonHints collects object keys from the sample. A non-object cell is
// ignored. The keys are hints: writing them into the map would turn one
// row's shape into a fact the schema does not have.
func jsonHints(rows [][]string) []string {
	seen := map[string]struct{}{}
	var keys []string
	for _, row := range rows {
		for _, cell := range row {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(cell), &obj); err != nil || obj == nil {
				continue
			}
			for k := range obj {
				if _, ok := seen[k]; ok {
					continue
				}
				seen[k] = struct{}{}
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// openObject shows the editor and writes nothing. A json column Propose
// seeded as text keeps that link until a key is typed.
func (m *Model) openObject() {
	col := m.column()
	if col.Name == "" {
		return
	}
	m.editTarget = col.Name
	m.objIdx = 0
	m.objHints = jsonHints(m.sample.Preview)
	m.screen = screenObject
	m.note = ""
}

func (m Model) onObject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.screen = screenBoard
		return m, nil
	case "up", "k":
		m.objIdx--
		m.clampObject()
	case "down", "j":
		m.objIdx++
		m.clampObject()
	case "left", "h":
		m.src--
		m.clampSrc()
	case "right", "l":
		m.src++
		m.clampSrc()
	case "a":
		return m, m.ask(promptObjectKey, "", "key name")
	case " ":
		m.bindObjectSource()
	case "c":
		return m, m.ask(promptObjectConst, currentObjectConst(m), "stored as a JSON string")
	case "d":
		m.dropObjectKey()
	case "s":
		return m, m.saveCmd()
	}
	return m, nil
}

func (m *Model) clampObject() {
	f := m.field(m.editTarget)
	n := 0
	if f != nil && f.Object != nil {
		n = len(f.Object.Keys)
	}
	if n == 0 {
		m.objIdx = 0
		return
	}
	if m.objIdx < 0 {
		m.objIdx = 0
	}
	if m.objIdx >= n {
		m.objIdx = n - 1
	}
}

// ensureObject is the moment the origin becomes object. The seeded
// source link has to go at the same time: Compile rejects two origins,
// and leaving the link would keep writing the old cell.
func (m *Model) ensureObject() *importer.Field {
	col := m.columnBy(m.editTarget)
	f := m.field(m.editTarget)
	if f == nil {
		m.fm.Fields = append(m.fm.Fields, importer.Field{Target: col.Name})
		f = &m.fm.Fields[len(m.fm.Fields)-1]
		m.unomit(col.Name)
	}
	if f.Object == nil {
		f.Source = ""
		f.Sources = nil
		f.Join = nil
		f.Coalesce = nil
		f.Constant = nil
		f.Object = &importer.Object{}
	}
	return f
}

func (m *Model) addObjectKey(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		m.note = "key needs a name"
		return
	}
	f := m.ensureObject()
	for _, k := range f.Object.Keys {
		if k.Key == name {
			m.note = name + " is already a key"
			return
		}
	}
	f.Object.Keys = append(f.Object.Keys, importer.ObjectField{Key: name})
	m.objIdx = len(f.Object.Keys) - 1
	m.note = "added " + name
	m.refresh()
}

func (m *Model) bindObjectSource() {
	f := m.field(m.editTarget)
	if f == nil || f.Object == nil || len(f.Object.Keys) == 0 {
		m.note = "no key to bind"
		return
	}
	names := m.sourceNames()
	if m.src < 0 || m.src >= len(names) {
		m.note = "no source selected"
		return
	}
	m.clampObject()
	k := &f.Object.Keys[m.objIdx]
	k.Source = names[m.src]
	k.Sources = nil
	k.Join = nil
	k.Coalesce = nil
	k.Constant = nil
	k.Object = nil
	m.note = k.Key + " ← " + k.Source
	m.refresh()
}

func (m *Model) setObjectConstant(text string) {
	f := m.field(m.editTarget)
	if f == nil || f.Object == nil || len(f.Object.Keys) == 0 {
		m.note = "no key for a constant"
		return
	}
	m.clampObject()
	k := &f.Object.Keys[m.objIdx]
	v := text
	k.Constant = &v
	k.Source = ""
	k.Sources = nil
	k.Join = nil
	k.Coalesce = nil
	k.Object = nil
	m.note = k.Key + " is a constant"
	m.refresh()
}

func (m *Model) dropObjectKey() {
	f := m.field(m.editTarget)
	if f == nil || f.Object == nil || len(f.Object.Keys) == 0 {
		m.note = "no key to drop"
		return
	}
	m.clampObject()
	i := m.objIdx
	f.Object.Keys = append(f.Object.Keys[:i], f.Object.Keys[i+1:]...)
	if len(f.Object.Keys) == 0 {
		f.Object = nil
		m.objIdx = 0
		m.note = "dropped the last key"
	} else {
		m.clampObject()
		m.note = "dropped key"
	}
	m.refresh()
}

func currentObjectConst(m Model) string {
	f := m.field(m.editTarget)
	if f == nil || f.Object == nil || m.objIdx < 0 || m.objIdx >= len(f.Object.Keys) {
		return ""
	}
	if f.Object.Keys[m.objIdx].Constant == nil {
		return ""
	}
	return *f.Object.Keys[m.objIdx].Constant
}
