package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/secrets"
)

// --- args ---------------------------------------------------------------------------------

// splitArgs splits s like a shell: whitespace separates arguments, '…' is literal, "…" allows \"
// and \\, and an unquoted backslash escapes the next character.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inArg := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		case r == '\'':
			inArg = true
			end := slices.Index(rs[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated quote (')")
			}
			cur.WriteString(string(rs[i+1 : i+1+end]))
			i += end + 1
		case r == '"':
			inArg = true
			closed := false
			for i++; i < len(rs); i++ {
				if rs[i] == '"' {
					closed = true
					break
				}
				if rs[i] == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\') {
					i++
				}
				cur.WriteRune(rs[i])
			}
			if !closed {
				return nil, errors.New(`unterminated quote (")`)
			}
		case r == '\\':
			if i+1 == len(rs) {
				return nil, errors.New("backslash at the end")
			}
			inArg = true
			i++
			cur.WriteRune(rs[i])
		default:
			inArg = true
			cur.WriteRune(r)
		}
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out, nil
}

// joinArgs is the inverse of splitArgs: arguments with whitespace, quotes or backslashes (and empty
// ones) are quoted.
func joinArgs(args []string) string {
	var out []string
	for _, a := range args {
		switch {
		case a != "" && !strings.ContainsAny(a, " \t\n\r'\"\\"):
			out = append(out, a)
		case !strings.Contains(a, "'"):
			out = append(out, "'"+a+"'")
		default:
			out = append(out, `"`+strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a)+`"`)
		}
	}
	return strings.Join(out, " ")
}

// --- env and header lines -------------------------------------------------------------------

var (
	envKey    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerKey = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	badSecret = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// pairKind describes the line syntax of the env (KEY=value) and headers (Key: value) fields.
type pairKind struct {
	sep    string
	want   string
	keyOK  *regexp.Regexp
	format func(k, v string) string
}

var (
	envPairs    = pairKind{sep: "=", want: "KEY=value", keyOK: envKey, format: func(k, v string) string { return k + "=" + v }}
	headerPairs = pairKind{sep: ":", want: "Key: value", keyOK: headerKey, format: func(k, v string) string { return k + ": " + v }}
)

// splitLine splits one line into key and value ("" key when malformed).
func (p pairKind) splitLine(l string) (key, value string, ok bool) {
	k, v, found := strings.Cut(l, p.sep)
	k = strings.TrimSpace(k)
	if !found || !p.keyOK.MatchString(k) {
		return "", "", false
	}
	if p.sep == ":" {
		v = strings.TrimSpace(v)
	}
	return k, v, true
}

// parse reads the lines of a field: empty lines are skipped, `secret:NAME` values become
// ${secret:NAME}. The error names the 1-based line.
func (p pairKind) parse(text string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]int{}
	for i, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, ok := p.splitLine(l)
		if !ok {
			return nil, fmt.Errorf("line %d: want %s", i+1, p.want)
		}
		if j, dup := seen[strings.ToLower(k)]; dup {
			return nil, fmt.Errorf("line %d: %s repeats line %d", i+1, k, j)
		}
		seen[strings.ToLower(k)] = i + 1
		if name, isRef := strings.CutPrefix(v, "secret:"); isRef {
			if !secrets.ValidName(name) {
				return nil, fmt.Errorf("line %d: invalid secret name %q (letters, digits, '.', '_' and '-')", i+1, name)
			}
			v = secrets.Ref(name)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// lines formats a map as sorted lines.
func (p pairKind) lines(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []string
	for _, k := range keys {
		out = append(out, p.format(k, m[k]))
	}
	return out
}

// secretName is the keyring name for a server's env or header key: <server>-<lowercased key>,
// with characters a secret name cannot have replaced by '-'.
func secretName(server, key string) string {
	return badSecret.ReplaceAllString(server+"-"+strings.ToLower(key), "-")
}

// --- the form -------------------------------------------------------------------------------

// serverForm adds (orig == "") or edits the server orig.
type serverForm struct {
	orig string
	form *form
	tab  *serversTab
}

// serverSavedMsg is the result of saving f.
type serverSavedMsg struct {
	f       *serverForm
	name    string
	renamed bool // the rename went through (even if the update after it failed)
	err     error
}

// secretStoredMsg is the result of storing the value of line `line` of the field key in the keyring.
type secretStoredMsg struct {
	f         *serverForm
	key       string
	line      int
	was, with string // the line before and after (with the reference)
	name      string
	err       error
}

func newServerForm(t *serversTab, name string, s *config.Server, home string) *serverForm {
	sf := &serverForm{orig: name, tab: t}
	title := "New server"
	if s == nil {
		s = &config.Server{Type: "stdio"}
	} else {
		title = "Edit " + oneLine(name)
	}
	f := newForm(title)
	sf.form = f
	f.startSection("Server")
	f.addText("name", "Name", name, "letters, digits, '-'")
	typ := f.addChoice("type", "Type", []string{"stdio", "http"}, s.Type)
	isHTTP := func() bool { return typ.value() == "http" }
	isStdio := func() bool { return !isHTTP() }
	f.startSection("Command")
	c := f.addText("command", "Command", s.Command, "program to run (in the daemon's PATH)")
	c.hidden = isHTTP
	a := f.addText("args", "Args", joinArgs(s.Args), "space separated, quote with '…' or \"…\"")
	a.hidden = isHTTP
	cwd := ""
	if s.Cwd != "" {
		cwd = tildify(s.Cwd, home)
	}
	d := f.addText("cwd", "Cwd", cwd, "absolute folder (empty: the daemon's)")
	d.hidden = isHTTP
	// Existing literal env and header values are shown as they are on purpose: editing needs them
	// (the details pane masks them). ctrl+k moves a value into the keyring.
	e := f.addLines("env", "Env", envPairs.lines(s.Env), "KEY=value, one per line")
	e.hidden = isHTTP
	e.help = "enter: new line · secret:NAME refers to the keyring · ctrl+k stores this line's value in the keyring"
	f.startSection("Connection")
	u := f.addText("url", "URL", s.URL, "https://…")
	u.hidden = isStdio
	hd := f.addLines("headers", "Headers", headerPairs.lines(s.Headers), "Key: value, one per line")
	hd.hidden = isStdio
	hd.help = e.help
	f.startSection("Options")
	to := ""
	if s.RequestTimeoutMs != 0 {
		to = strconv.Itoa(s.RequestTimeoutMs)
	}
	f.addText("timeout", "Timeout ms", to, "60000")
	f.addToggle("perSession", "Per session", s.PerSession)
	f.note = "Values entered as secret:NAME are stored as ${secret:NAME}."
	return sf
}

// workKey is the busy key (Model.begin) of this form's work of kind ("save", "secret").
func (sf *serverForm) workKey(kind string) string { return fmt.Sprintf("serverform:%p:%s", sf, kind) }

func (sf *serverForm) hints() []string {
	if fl := sf.form.current(); fl.kind == linesField {
		return []string{"ctrl+s", "save", "ctrl+k", "value to keyring", "enter", "new line", "esc", "cancel"}
	}
	return []string{"ctrl+s", "save", "tab", "next field", "esc", "cancel"}
}

func (sf *serverForm) view(m *Model, w, h int) string { return sf.form.view(w, h) }

func (sf *serverForm) update(m *Model, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		f := sf.form
		if f.busy != "" {
			return nil, false
		}
		f.layout(m.width)
		if fl := f.current(); msg.String() == "ctrl+k" && fl.kind == linesField {
			return sf.storeSecret(m, fl), false
		}
		cmd, submit := f.key(msg)
		if submit {
			return sf.save(m), false
		}
		return cmd, false
	case secretStoredMsg:
		if msg.f != sf {
			return nil, false
		}
		sf.form.busy = ""
		fl := sf.form.get(msg.key)
		if msg.err != nil {
			fl.err = "keyring: " + msg.err.Error()
			return nil, false
		}
		if msg.line < len(fl.lines) && fl.lines[msg.line].Value() == msg.was {
			fl.lines[msg.line].SetValue(msg.with)
		}
		m.flash("stored the value in the keyring as "+msg.name, false)
	case serverSavedMsg:
		if msg.f != sf {
			return nil, false
		}
		sf.form.busy = ""
		if msg.renamed {
			sf.orig = msg.name
		}
		if msg.err == nil {
			return nil, true
		}
		switch {
		case errors.Is(msg.err, actions.ErrExists):
			sf.form.get("name").err = msg.err.Error()
			sf.form.focusKey("name")
		case errors.Is(msg.err, actions.ErrNotFound):
			sf.form.err = "server " + sf.orig + " no longer exists (esc discards the form)"
		default:
			sf.form.err = msg.err.Error()
		}
	}
	return nil, false
}

// storeSecret stores the literal value of the focused line of an env or headers field in the
// keyring (in a command) and replaces it with the reference.
func (sf *serverForm) storeSecret(m *Model, fl *field) tea.Cmd {
	f := sf.form
	pk := envPairs
	if fl.key == "headers" {
		pk = headerPairs
	}
	fl.err = ""
	line := fl.lines[fl.line].Value()
	k, v, ok := pk.splitLine(line)
	switch {
	case !ok:
		fl.err = fmt.Sprintf("line %d: want %s", fl.line+1, pk.want)
		return nil
	case v == "":
		fl.err = fmt.Sprintf("line %d: enter the value first", fl.line+1)
		return nil
	case strings.HasPrefix(v, "secret:") || strings.Contains(v, "${secret:"):
		fl.err = fmt.Sprintf("line %d: already a secret reference", fl.line+1)
		return nil
	}
	server := f.get("name").value()
	if !config.ValidServerName(server) {
		f.get("name").err = "enter a valid name first: the secret is called <name>-<key>"
		return nil
	}
	name := secretName(server, k)
	if !m.begin(sf.workKey("secret"), "storing "+name+" in the keyring …") {
		return nil
	}
	f.busy = "storing the value in the keyring …"
	msg := secretStoredMsg{f: sf, key: fl.key, line: fl.line, was: line, with: pk.format(k, secrets.Ref(name)), name: name}
	return func() tea.Msg {
		msg.err = secrets.Set(name, v)
		return msg
	}
}

// build validates the fields (inline errors) and returns the server; ok=false when invalid.
func (sf *serverForm) build(home string) (name string, s config.Server, ok bool) {
	f := sf.form
	f.clearErrors()
	name = f.get("name").value()
	switch {
	case name == config.ReservedName:
		f.get("name").err = fmt.Sprintf("%q is reserved", config.ReservedName)
	case !config.ValidServerName(name):
		f.get("name").err = "use letters, digits and '-' (max 32 characters, starting with a letter or digit)"
	}
	s.Type = f.get("type").value()
	if s.Type == "stdio" {
		if s.Command = f.get("command").value(); s.Command == "" {
			f.get("command").err = "required for stdio servers"
		}
		args, err := splitArgs(f.get("args").input.Value())
		if err != nil {
			f.get("args").err = err.Error()
		}
		s.Args = args
		if cwd := f.get("cwd").value(); cwd != "" {
			if cwd == "~" || strings.HasPrefix(cwd, "~/") {
				cwd = filepath.Join(home, cwd[1:])
			}
			if !filepath.IsAbs(cwd) {
				f.get("cwd").err = "must be an absolute path (or start with ~/)"
			}
			s.Cwd = filepath.Clean(cwd)
		}
		if s.Env, err = envPairs.parse(f.get("env").text()); err != nil {
			f.get("env").err = err.Error()
		}
	} else {
		s.URL = f.get("url").value()
		switch {
		case s.URL == "":
			f.get("url").err = "required for http servers"
		case !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://"):
			f.get("url").err = "must start with http:// or https://"
		}
		var err error
		if s.Headers, err = headerPairs.parse(f.get("headers").text()); err != nil {
			f.get("headers").err = err.Error()
		}
	}
	if t := f.get("timeout").value(); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 {
			f.get("timeout").err = "a number of milliseconds (empty: 60000)"
		}
		s.RequestTimeoutMs = n
	}
	s.PerSession = f.get("perSession").on
	if f.hasErrors() {
		f.focusFirstError()
		return "", s, false
	}
	check := config.Config{Version: 1, Servers: map[string]*config.Server{name: &s}}
	if err := check.Validate(); err != nil {
		f.err = err.Error()
		return "", s, false
	}
	return name, s, true
}

// save validates and, when valid, writes the server in a command; the result is a serverSavedMsg.
func (sf *serverForm) save(m *Model) tea.Cmd {
	name, s, ok := sf.build(m.opts.Home)
	if !ok {
		return nil
	}
	env, orig := m.opts.Env, sf.orig // ServerUpdate keeps Disabled, Rules, Tools and Origin
	if !m.begin(sf.workKey("save"), "saving "+name+" …") {
		return nil
	}
	sf.form.busy = "saving …"
	return func() tea.Msg {
		msg := serverSavedMsg{f: sf, name: name}
		switch {
		case orig == "":
			msg.err = actions.ServerAdd(env, name, s)
		case orig != name:
			if msg.err = actions.ServerRename(env, orig, name); msg.err == nil {
				msg.renamed = true
				msg.err = actions.ServerUpdate(env, name, s)
			}
		default:
			msg.err = actions.ServerUpdate(env, name, s)
		}
		return msg
	}
}
