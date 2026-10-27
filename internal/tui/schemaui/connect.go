package schemaui

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	yaml "gopkg.in/yaml.v3"
)

// Connection is one saved dial target. PasswordEnv names the variable
// that holds the password. The password itself is never a field: a
// password key in the file is a load error, and a DSN that already
// contains one is refused at save time.
type Connection struct {
	Name        string `yaml:"name"`
	Dialect     string `yaml:"dialect"`
	DSN         string `yaml:"dsn"`
	PasswordEnv string `yaml:"password_env,omitempty"`
}

// connectionFile is the on-disk document. A bare list would not match
// the connections: key the command line documents.
type connectionFile struct {
	Connections []Connection `yaml:"connections"`
}

// LoadConnections reads the connections file. KnownFields is on so a
// password key fails to load instead of being dropped and looking like
// it was stored safely.
func LoadConnections(path string) ([]Connection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var file connectionFile
	if err := dec.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	return file.Connections, nil
}

// SaveConnections writes cs. The file is mode 0600 because it names the
// variable next to a host, and the directory is created here so the
// first save does not depend on a prior install step. The password is
// not read: only the struct is encoded.
func SaveConnections(path string, cs []Connection) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(connectionFile{Connections: cs})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// hasPassword reports a DSN that must not be written. A :// URL is
// checked with url.Parse so user:pass@ is caught; any other string is
// checked for a password= parameter. A mysql native user:pw@tcp form
// has neither, and storing it would put the secret in the file.
func hasPassword(dsn string) bool {
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil && u.User != nil {
			if _, set := u.User.Password(); set {
				return true
			}
		}
	}
	if passwordParam(dsn) {
		return true
	}
	if at := strings.IndexByte(dsn, '@'); at > 0 && !strings.Contains(dsn, "://") {
		if strings.Contains(dsn[:at], ":") {
			return true
		}
	}
	return false
}

// passwordParam matches password= in any case. libpq and the SQL Server
// keyword form both accept Password=.
func passwordParam(dsn string) bool {
	return strings.Contains(strings.ToLower(dsn), "password=")
}

// splicePassword copies password into dsn for one dial. The three forms
// are the ones the engines actually accept: a URL, a libpq keyword
// string, and mysql's native user@tcp(host)/db. The password is not
// included in any error.
func splicePassword(dsn, password string) (string, error) {
	if password == "" {
		return dsn, nil
	}
	if strings.Contains(dsn, "://") {
		return spliceURL(dsn, password)
	}
	if spliced, ok, err := spliceMySQL(dsn, password); ok || err != nil {
		return spliced, err
	}
	if keywordDSN(dsn) {
		if passwordParam(dsn) {
			return dsn, nil
		}
		return strings.TrimRight(dsn, " ") + " password=" + password, nil
	}
	return "", errors.New("DSN has no user")
}

// spliceURL sets the user password on a copy. url.Parse's own error
// quotes the DSN, so a failure is replaced with a line that names no
// secret and says the DSN has no user.
func spliceURL(dsn, password string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", errors.New("DSN has no user")
	}
	if u.User == nil || u.User.Username() == "" {
		return "", errors.New("DSN has no user")
	}
	u.User = url.UserPassword(u.User.Username(), password)
	return u.String(), nil
}

// spliceMySQL rewrites user@tcp(host) when the user has no colon. A
// colon already means a password is in the string, and rewriting would
// nest a second one. A slash or space in the user part means this is
// not the native form.
func spliceMySQL(dsn, password string) (string, bool, error) {
	i := strings.Index(dsn, "@tcp(")
	if i <= 0 {
		return "", false, nil
	}
	user := dsn[:i]
	if strings.ContainsAny(user, "/? ") {
		return "", false, nil
	}
	if strings.Contains(user, ":") {
		return dsn, true, nil
	}
	return user + ":" + password + dsn[i:], true, nil
}

// keywordDSN is the libpq and ADO key=value shape. user= or host= is
// enough to tell it from a path, and the caller appends password=.
func keywordDSN(dsn string) bool {
	for _, field := range strings.Fields(dsn) {
		k, _, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "user") || strings.EqualFold(k, "host") {
			return true
		}
	}
	return false
}

// resolve builds the DSN a command will pass to a service. os.Getenv
// runs here, inside the command, and the returned string is not stored
// on the model.
func (d dialTarget) resolve() (dialect, dsn string, err error) {
	dialect = d.dialect
	if d.useEnv {
		dsn = d.envDSN
		if d.envName != "" {
			dsn = os.Getenv(d.envName)
		}
		return dialect, dsn, nil
	}
	dsn = d.dsn
	if d.passwordEnv != "" {
		dsn, err = splicePassword(dsn, os.Getenv(d.passwordEnv))
	}
	return dialect, dsn, err
}

// onConnect handles the list keys. The DSN field is delegated so j, k,
// and q are typed into the field instead of moving the cursor.
func (m Model) onConnect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.focus == focusDSN {
		return m.onDSN(msg)
	}
	switch msg.String() {
	case "j", "down":
		m.moveConnect(1)
	case "k", "up":
		m.moveConnect(-1)
	case "tab":
		m.cycleFocus(1)
	case "shift+tab", "backtab":
		m.cycleFocus(-1)
	case "e":
		return m.connectEnv()
	case "n":
		return m.startSaveConnection()
	case "enter":
		return m.connectEnter()
	}
	return m, nil
}

// onDSN edits the field. enter dials; esc returns to the dialect list
// without clearing what was typed.
func (m Model) onDSN(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m.connectTyped()
	case "tab":
		m.dsnInput.Blur()
		m.cycleFocus(1)
		return m, nil
	case "shift+tab", "backtab":
		m.dsnInput.Blur()
		m.cycleFocus(-1)
		return m, nil
	case "esc":
		m.focus = focusDialect
		m.dsnInput.Blur()
		return m, nil
	default:
		return m, m.forward(&m.dsnInput, msg)
	}
}

// moveConnect walks the dialect list or the saved list, whichever has
// focus. j and k on the env row do nothing, so they cannot land on a
// row that is not there.
func (m *Model) moveConnect(d int) {
	switch m.focus {
	case focusSaved:
		n := len(m.conns)
		if n == 0 {
			return
		}
		m.savedIdx += d
		if m.savedIdx < 0 {
			m.savedIdx = 0
		}
		if m.savedIdx >= n {
			m.savedIdx = n - 1
		}
	case focusEnv:
		return
	default:
		m.focus = focusDialect
		n := len(m.dialects)
		m.dialectIdx += d
		if m.dialectIdx < 0 {
			m.dialectIdx = 0
		}
		if m.dialectIdx >= n {
			m.dialectIdx = n - 1
		}
	}
}

// cycleFocus is tab. Saved and env are in the cycle only when the
// session actually has them.
func (m *Model) cycleFocus(d int) {
	order := []connectFocus{focusDialect, focusDSN}
	if len(m.conns) > 0 {
		order = append(order, focusSaved)
	}
	if m.hasEnv {
		order = append(order, focusEnv)
	}
	at := 0
	for i, f := range order {
		if f == m.focus {
			at = i
			break
		}
	}
	at = (at + d + len(order)) % len(order)
	m.focus = order[at]
	if m.focus == focusDSN {
		m.dsnInput.Focus()
	} else {
		m.dsnInput.Blur()
	}
}

// connectEnter dials the focused target. On the dialect list, enter
// moves to the DSN field instead, because no address has been chosen.
func (m Model) connectEnter() (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusSaved:
		return m.connectSaved()
	case focusEnv:
		return m.connectEnv()
	case focusDSN:
		return m.connectTyped()
	default:
		m.focus = focusDSN
		m.dsnInput.Focus()
		return m, textinputBlink()
	}
}

// textinputBlink is the command Focus returns. Dropping it leaves the
// cursor stopped, which is how bubbles signals a focused field.
func textinputBlink() tea.Cmd { return textinput.Blink }

// connectTyped dials the field. passwordEnv is cleared so a password
// variable from a previously highlighted saved connection is not
// spliced into a DSN the user just typed.
func (m Model) connectTyped() (tea.Model, tea.Cmd) {
	dsn := strings.TrimSpace(m.dsnInput.Value())
	if dsn == "" {
		m.note = "dsn is empty"
		return m, nil
	}
	m.dialect = m.dialects[m.dialectIdx]
	m.dsn = dsn
	m.passwordEnv = ""
	m.useEnv = false
	m.note = ""
	return m, m.tablesCmd()
}

// connectSaved dials the highlighted file entry. The DSN stays
// unspliced on the model; the command reads password_env.
func (m Model) connectSaved() (tea.Model, tea.Cmd) {
	if len(m.conns) == 0 || m.savedIdx < 0 || m.savedIdx >= len(m.conns) {
		m.note = "no saved connection"
		return m, nil
	}
	c := m.conns[m.savedIdx]
	m.dialect = c.Dialect
	m.dsn = c.DSN
	m.passwordEnv = c.PasswordEnv
	m.useEnv = false
	m.note = ""
	return m, m.tablesCmd()
}

// connectEnv dials MOCKAGEN_DSN or DATABASE_URL. The value is not
// copied onto the model; resolve reads it inside the command.
func (m Model) connectEnv() (tea.Model, tea.Cmd) {
	if !m.hasEnv {
		m.note = "no env dsn"
		return m, nil
	}
	m.dialect = m.dialects[m.dialectIdx]
	m.useEnv = true
	m.passwordEnv = ""
	// The env value is not copied onto m.dsn. The view prints the
	// variable name, and resolve reads the value inside the command.
	m.note = ""
	return m, m.tablesCmd()
}

// tablesCmd dials and returns a tablesMsg. Services.Tables is the only
// side effect; the model test feeds tablesMsg and does not run this.
func (m Model) tablesCmd() tea.Cmd {
	d := m.target()
	if d.dialect == "" && m.dialectIdx >= 0 && m.dialectIdx < len(m.dialects) {
		d.dialect = m.dialects[m.dialectIdx]
	}
	tables := m.services.Tables
	return func() tea.Msg {
		dialect, dsn, err := d.resolve()
		if err != nil {
			return tablesMsg{err: err}
		}
		if tables == nil {
			return tablesMsg{err: errors.New("schemaui: no tables service")}
		}
		ctx, cancel := m.withTimeout()
		defer cancel()
		got, err := tables(ctx, dialect, dsn)
		return tablesMsg{tables: got, err: err}
	}
}

// startSaveConnection refuses a DSN that already holds a password
// before the name prompt. The model test sees the note without running
// a command, and nothing is written.
func (m Model) startSaveConnection() (tea.Model, tea.Cmd) {
	dsn := strings.TrimSpace(m.dsnInput.Value())
	if dsn == "" {
		m.note = "not saved: no dsn"
		return m, nil
	}
	if hasPassword(dsn) {
		m.note = "not saved: dsn contains a password"
		return m, nil
	}
	m.dialect = m.dialects[m.dialectIdx]
	m.dsn = dsn
	return m, m.ask(promptConnName, "", "connection name")
}

// saveConnectionCmd writes the file from the command. The password
// check runs again because the prompt is a later turn. An existing
// name is replaced so the file does not grow a duplicate.
func (m Model) saveConnectionCmd(name, passEnv string) tea.Cmd {
	dsn := m.dsn
	if hasPassword(dsn) {
		return func() tea.Msg {
			return connSavedMsg{err: errors.New("not saved: dsn contains a password")}
		}
	}
	c := Connection{
		Name:        name,
		Dialect:     m.dialect,
		DSN:         dsn,
		PasswordEnv: passEnv,
	}
	next := append([]Connection(nil), m.conns...)
	replaced := false
	for i := range next {
		if next[i].Name == name {
			next[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		next = append(next, c)
	}
	path := m.connPath
	save := m.services.SaveConnections
	return func() tea.Msg {
		if path == "" {
			var err error
			path, err = defaultConnectionsPath()
			if err != nil {
				return connSavedMsg{err: err}
			}
		}
		if save == nil {
			if err := SaveConnections(path, next); err != nil {
				return connSavedMsg{err: err}
			}
			return connSavedMsg{connections: next}
		}
		if err := save(path, next); err != nil {
			return connSavedMsg{err: err}
		}
		return connSavedMsg{connections: next}
	}
}

// defaultConnectionsPath is the user config file. The command normally
// passes Session.ConnectionsPath so a test never reads the real home;
// this is the fallback when that was left empty.
func defaultConnectionsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mockagen", "connections.yaml"), nil
}
