# skilltri Plan 3: TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `skilltri` without arguments opens a TUI built like tussh's: tabs Servers, Skills, Preview, Setup, Doctor, a rules editor dialog for every node, forms for servers and sources, and every action the CLI offers — without blocking on the daemon, git or the network.

**Architecture:** A new `internal/actions` package holds every config-changing operation (today spread over `internal/cli`) as plain functions returning results and errors; both `cli` and `tui` call it. `internal/tui` is a Bubble Tea v1 model: a data snapshot (config, catalog, daemon status, review state) loaded by async commands and refreshed on a tick and on file changes; one sub-model per tab; shared dialogs (form, rules editor, confirm, help). Layout helpers are ported from tussh (`internal/tui/layout.go`).

**Tech Stack:** Go 1.26, `github.com/charmbracelet/bubbletea` v1.3.10, `github.com/charmbracelet/bubbles` v1.0.0, `github.com/charmbracelet/lipgloss` v1.1.0, `github.com/charmbracelet/x/ansi` v0.11.6 (same versions as tussh).

**Spec:** `CONCEPT.md` sections "TUI", "CLI", "Decisions" (ANSI palette colors only, width-safe icons). Reference implementation for look and feel: `~/development/baeroe/tussh/internal/tui` (read-only).

## Global Constraints

- Everything from plans 1 and 2 still applies (English, package doc comments, demo data only in tests, never touching the real `~/.claude`, `~/.config/skilltri`, keychain, network or a running real daemon, `make test` green, one commit per task with a Co-Authored-By line naming the Claude model that wrote it, new commits only).
- New dependencies: only the four charmbracelet modules above, at those versions.
- Colors: ANSI palette colors only (`lipgloss.Color("1")`…`"15"`), so the TUI follows the terminal theme. Icons only from characters every width table agrees on (tussh's rule): `✓ ✗ ● ○ ▸ ▾ ! i` and ASCII; no emoji whose width varies.
- Layout: two panes (list + details) at ≥ 90 columns, a single column below; header line with tab names and status chips; footer with key hints; `?` help overlay. Nothing may overflow the terminal width at any size ≥ 40×12; long values are ellipsized in the middle (`ellipsizeMiddle`) and paths shown with `~` (`tildify`).
- The UI never blocks: every daemon control call, catalog build, `git` sync, import or doctor run happens in a `tea.Cmd` with a timeout (control 2 s, update/import unbounded but cancellable with `esc`), showing a spinner and the result or error in the status line.
- Destructive actions (remove server, remove source, uninstall service, overwrite on import) always ask for confirmation in a modal.
- Keys (global): `tab`/`shift+tab` or `1`–`5` switch tabs, `?` help, `q`/`ctrl+c` quit (`esc` closes dialogs first), `/` filter in list tabs, `j`/`k`/arrows move, `enter` details/expand, `space` toggle enabled, `r` rules editor, `a` add, `e` edit, `d` delete, `u` update, `R` reload.
- Tests drive the model with `tea.Msg`s (`Update`) and assert on `View()` strings and on the resulting config, like tussh's tui tests; no real terminal.

## Review Focus

1. **Small and huge terminals, long names:** 40×12, 89 vs 90 columns, 300 columns; server/source/skill names of 60 characters and paths of 200 → no line wider than the terminal, no wrapped garbage. → test in Task 2 and each tab task.
2. **Daemon absent, starting or dying while the TUI is open:** header chip changes, tabs fall back to offline data, actions that need the daemon say so; no call blocks the UI. → tests in Tasks 2 and 3.
3. **Config changed elsewhere while a form is open:** the TUI reloads its snapshot without discarding the form's input; saving merges through `config.Update` and reports a conflict only when the edited item itself disappeared. → test in Task 3.
4. **Long-running work** (updating 60 sources, import, doctor): UI stays responsive, `esc` cancels, results land in the status line, no goroutine left after quit. → tests in Tasks 4 and 7.
5. **Invalid input in forms** (bad server name, missing command/URL, unknown rule mode, relative path, malformed env line): inline error next to the field, nothing written. → tests in Tasks 3, 4 and 5.

---

### Task 1: `internal/actions` — one place for config-changing operations

**Files:**
- Create: `internal/actions/actions.go`, `internal/actions/servers.go`, `internal/actions/sources.go`, `internal/actions/rules.go`, `internal/actions/pluginservers.go`, `internal/actions/actions_test.go`
- Modify: `internal/cli/sources.go`, `internal/cli/rules.go`, `internal/cli/pluginservers.go`, `internal/cli/adaptercmd.go` (become thin wrappers that print), their tests stay green

**Interfaces:**
- Produces (every function edits through `config.Update(paths.ConfigFile(), …)` unless a path is passed, and returns typed errors the CLI and TUI can show):
  ```go
  type Env struct { ConfigPath, CatalogPath string; Builder *catalog.Builder; Socket string } // zero value → paths defaults
  func (e Env) RefreshCatalog() error                               // offline rebuild when no daemon answers (moved from cli)
  func ServerAdd(e Env, name string, s config.Server) error          // validates, rejects existing
  func ServerUpdate(e Env, name string, s config.Server) error       // keeps Rules/Tools/Origin of the existing entry unless set; missing → ErrNotFound
  func ServerRename(e Env, old, new string) error
  func ServerRemove(e Env, name string) error
  func SourceAdd(e Env, name string, s config.Source) error
  func SourceUpdate(e Env, name string, s config.Source) error
  func SourceRemove(e Env, name string) error
  func SkillNew(e Env, name string) (dir string, err error)
  func RuleAdd(e Env, t target.Target, r rules.Rule) (warnings []string, err error)
  func RuleRemove(e Env, t target.Target, index int) error            // 1-based
  func SetDisabled(e Env, t target.Target, disabled bool) (warnings []string, err error)
  func SetAdapter(e Env, id string, enabled bool) error
  func PluginServerEnable(e Env, ref, name string) (string, error)    // ref "source/plugin/server"; returns the server name
  func PluginServerAccept(e Env, name string) error
  var ErrNotFound, ErrExists = errors.New("not found"), errors.New("already exists")
  ```
  Server/source/skill name validation and the unknown-target warnings from plan 2 move here unchanged.

- [ ] **Step 1: Failing tests** for each function against a temp config (one table test per group: success, `ErrExists`, `ErrNotFound`, validation error leaves the file byte-identical). `ServerUpdate` keeps rules and tool settings; `ServerRename` keeps them and fails on an existing target name.
- [ ] **Step 2–4:** RED, implement by moving the logic out of `internal/cli` (no behaviour change for the CLI; its tests must stay green unchanged), GREEN. **Step 5: Commit** `Move config-changing operations into internal/actions`.

### Task 2: TUI skeleton — model, data snapshot, layout, header, help

**Files:**
- Create: `internal/tui/model.go`, `internal/tui/data.go`, `internal/tui/layout.go` (port of tussh's helpers: `width`, `fit`, `padLeft`, `ellipsizeMiddle`, `tildify`, `san`, `oneLine`, `wrap`, `panel`, `hjoin`, `overlay`, `modalBox`, `keyHints`, `ago`), `internal/tui/styles.go`, `internal/tui/help.go`, `internal/tui/tui_test.go`
- Modify: `go.mod`, `main.go` (no args → TUI; `--tab NAME` option)

**Interfaces:**
- Produces:
  ```go
  type Tab int // TabServers, TabSkills, TabPreview, TabSetup, TabDoctor
  type Options struct {
      Tab      Tab
      Env      actions.Env
      Home     string                // for Setup/Doctor (default os.UserHomeDir)
      Bin      string                // absolute skilltri path for snippets
      Cwd      string                // initial Preview folder
      NoTick   bool                  // tests drive ticks
      Now      func() time.Time
      Control  func(h daemon.Hello, reply any) error // default daemon.ControlTimeout with 2 s
      Editor   func(path string) *exec.Cmd           // default $VISUAL/$EDITOR/vi
  }
  func New(o Options) *Model
  func (m *Model) Init() tea.Cmd; Update(tea.Msg) (tea.Model, tea.Cmd); View() string
  type Snapshot struct {
      Config *config.Config; ConfigErr error
      Catalog *catalog.Catalog
      Status *daemon.StatusReply; DaemonErr error // nil Status = not running
      Review map[string]string
      Loaded time.Time
  }
  func loadSnapshot(o Options) tea.Cmd // async; result msg snapshotMsg
  ```
  Refresh: on start, every 2 s tick (status only) and when `config.json`/`catalog.json` mtime changes (checked on the tick), plus `R`. Header: tab names (active highlighted), chips `daemon ● running`/`○ stopped`, `servers 7/9 ready`, `skills 42`, `! 3` (doctor-relevant warnings from the snapshot: config error, source errors, needs-review servers). Footer: key hints of the active tab; status line for action results (auto-clears after 5 s).

- [ ] **Step 1: Failing tests:** `TestTabsSwitch` (keys `tab`, `2`, `shift+tab`), `TestHeaderChips` (snapshot with stopped daemon → `○ stopped`; with status → `● running` and `7/9`), `TestHelpOverlay`, `TestNoOverflow` (render every tab at 40×12, 89×24, 90×24, 300×50 with 60-character names and 200-character paths; assert `ansi.StringWidth` of every line ≤ width and line count ≤ height; Review Focus 1), `TestDaemonGoesAway` (status msg then error msg → chip flips, no panic; Review Focus 2), `TestQuitWaitsForNothing` (pending command on quit does not block).
- [ ] **Step 2–4:** RED, implement (tab bodies may be placeholders here; later tasks fill them), GREEN. **Step 5: Commit** `Add the TUI skeleton`.

### Task 3: Servers tab

**Files:**
- Create: `internal/tui/servers.go`, `internal/tui/serverform.go`, `internal/tui/form.go` (generic sectioned form: text, choice, toggle, multiline key=value fields, inline errors), `internal/tui/confirm.go`, `internal/tui/servers_test.go`
- Modify: `internal/daemon/control.go` (control command `restart` with `Args[0]` = server → `Manager.Restart`), `internal/cli` nothing

**Interfaces:**
- Consumes: `actions.Server*`, `actions.SetDisabled`, `actions.PluginServerAccept`, snapshot.
- List columns: icon (`●` ready, `○` stopped, `!` error, `?` needs review), name, type, `N tools`, sessions, rules summary (`only ~/x`, `+2 rules`, `everywhere`), `(plugin <source>/<plugin>)` for Origin servers. Details pane: command + args or URL, cwd, timeout, perSession, env/header keys (values shown as `${secret:…}` or `•••`, never literal secret values), rules, tools (from status/which: each with ✓/✗ and its own rules, `space` toggles the tool), last error and stderr tail.
- Keys: `a` add, `e` edit, `d` delete (confirm), `space` enable/disable, `t` focus tool list, `r` rules editor (Task 5), `x` restart (daemon only), `A` accept reviewed plugin server (shows old vs new definition in a modal first).
- Form fields: name, type (stdio/http), command, args (space separated, quotes respected), cwd, env (multiline `KEY=value`), url, headers (multiline `Key: value`), timeout ms, perSession. Secret handling: a value entered as `secret:NAME` is stored as `${secret:NAME}`; the form offers `ctrl+s` on an env/header line to store its current literal value in the keyring as `<server>-<key>` and replace it with the reference (uses `secrets.Set`; tests use `keyring.MockInit()`).

- [ ] **Step 1: Failing tests:** list and details rendering from a snapshot with demo servers (ready, error with stderr, needs-review plugin server); `TestServerAddForm` (fill fields, submit → config has the server; invalid name → inline error, file unchanged; Review Focus 5); `TestServerEditKeepsRules`; `TestServerDeleteConfirm` (cancel keeps, confirm removes); `TestToggleTool`; `TestSecretLineToKeyring` (mock keyring gets the value, config holds the ref, details show `${secret:…}`); `TestFormSurvivesReload` (snapshot msg while the form is open keeps typed input; Review Focus 3); `TestRestartOffline` (daemon stopped → status line says the daemon is not running); daemon control test for `restart`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the Servers tab`.

### Task 4: Skills tab

**Files:**
- Create: `internal/tui/skills.go`, `internal/tui/sourceform.go`, `internal/tui/skills_test.go`

**Interfaces:**
- Consumes: `actions.Source*`, `actions.SkillNew`, `actions.SetDisabled`, `actions.PluginServerEnable`, `daemon.Update`/control `update`, snapshot catalog + config + lock state.
- Tree: source rows (`▸`/`▾`, type, `@ref`, commit(7), `fetched 3h ago`, error marker), for marketplaces plugin rows (entry name, plugin.json name if different, enabled/disabled per `PluginEnabled`, `N skills`, extras like `hooks agents` dimmed, plugin MCP servers as child rows `server notes — available | enabled as writing-notes | needs review`), skill rows (`✓`/`✗` enabled state, ID, description one line). Collapsed by default except sources with ≤ 10 children. `/` filters by ID/description across the tree (auto-expands matches).
- Details pane for the selected node: source (url/path, ref, interval, last error, warnings), plugin (dir, extras, servers), skill (dir, description wrapped, warnings, rules).
- Keys: `a` add source (form: name, type, path or url, ref, subpath, update interval, plugins default), `n` new local skill (name prompt → `actions.SkillNew` → opens editor with `tea.ExecProcess`), `o` open selected skill's `SKILL.md` in the editor, `u` update selected source, `U` update all (async, spinner, per-source results; `esc` cancels; Review Focus 4), `space` enable/disable node, `r` rules editor, `d` remove source (confirm), `E` enable the selected plugin MCP server (name prompt prefilled with the default name).

- [ ] **Step 1: Failing tests:** tree rendering with the plan-2 testdata marketplace + a local source (collapsed/expanded, disabled plugin dimmed, extras shown); `TestFilterExpands`; `TestAddSourceForm` (local, git, marketplace with plugins default; invalid → inline error; Review Focus 5); `TestNewSkillOpensEditor` (fake `Editor` records the path; file exists with frontmatter); `TestUpdateAllCancellable` (fake update func blocking until ctx done; `esc` cancels; status line reports cancelled; no goroutine left — use `goleak`-style check via `runtime.NumGoroutine` before/after with tolerance); `TestTogglePluginAndSkill`; `TestEnablePluginServer`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the Skills tab`.

### Task 5: Rules editor dialog

**Files:**
- Create: `internal/tui/rules.go`, `internal/tui/pathinput.go` (text input with directory autocomplete: `tab` completes the common prefix, shows up to 8 candidates, `~` expanded for lookup but kept in the stored value), `internal/tui/rules_test.go`

**Interfaces:**
- Consumes: `actions.RuleAdd/RuleRemove`, `target.Target`, `rules.Evaluate`, snapshot.
- Opened with `r` on any server, tool, source, plugin or skill row. Shows: the node's own rules (numbered), the inherited rules from the nearest ancestor level that has rules (read-only, labelled `inherited from source official`), a one-line explanation of the effective behaviour (`active only in ~/dev/client-a and below`, `active everywhere except ~/dev/work`), and a live check row: folder input (defaults to the Preview folder) with the resulting ✓/✗ and reason from `rules.Evaluate`.
- Keys: `a` add (mode choice cycling `only`/`tree`/`not`/`not-tree` with `←`/`→`, path input with autocomplete; relative paths and unknown modes → inline error; Review Focus 5), `d` delete selected rule (no confirm; `u` undoes the last delete while the dialog is open), `esc` close. Warnings from `actions.RuleAdd` (unknown target) show in the dialog.

- [ ] **Step 1: Failing tests:** dialog for a skill with inherited source rules shows both sections and the explanation; add `tree ~/dev/x` → config updated, list updated; relative path → inline error, config unchanged; delete + undo; live check row flips when the folder input changes; path autocomplete on a temp dir tree (common prefix, candidate list, `~`).
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the rules editor`.

### Task 6: Preview tab

**Files:**
- Create: `internal/tui/preview.go`, `internal/tui/preview_test.go`

**Interfaces:**
- Consumes: control `which` (daemon) or the offline `which` builder from `internal/cli`/`daemon.WhichSkills` (move a shared `func Which(cfg, cat, dir, status) daemon.WhichReply` into `internal/daemon` if needed — no duplicated logic), `pathinput` from Task 5.
- Folder input at the top (default `Options.Cwd`, autocomplete), then three sections: servers (✓/✗, reason, state), tools of ready servers (indented, ✓/✗), skills (✓/✗, ID, source/plugin, reason), with counts in the section titles; `f` toggles "only active"; `enter` on an item jumps to it in its tab; `c` copies the `skilltri env` line for this folder (clipboard func injectable, like tussh).

- [ ] **Step 1: Failing tests:** offline preview for two folders with different rules; daemon reply rendering with tools; `f` filter; jump to Skills tab selects the skill; input change re-queries (debounced 200 ms; tests use `NoTick` and send the debounce msg).
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the Preview tab`.

### Task 7: Setup and Doctor tabs

**Files:**
- Create: `internal/harness/harness.go`, `internal/harness/harness_test.go` (port of tussh `internal/harness`: `Snippets(bin)` for claude, codex, gemini, opencode, cursor registering `skilltri mcp`; `Registered(home)` detection incl. JSONC; plus `OneMCPRegistered(home) []string` naming harness configs that still point at the 1MCP endpoint `http://127.0.0.1:3050/mcp`), `internal/tui/setup.go`, `internal/tui/doctor.go`, `internal/tui/setup_test.go`, `internal/tui/doctor_test.go`

**Interfaces:**
- Setup tab sections (each a selectable row with details and actions):
  - **Harnesses:** per harness: registered ✓/✗, 1MCP still registered `!`, the snippet (wrapped), `c` copy snippet.
  - **Shell hook:** per shell the `eval "$(skilltri shell-init <shell>)"` line, whether `SKILLTRI_HOOK` is set in the TUI's env, `c` copy.
  - **Daemon service:** installed? (plist/unit exists), running?, `i` install, `x` uninstall (confirm), `s` stop daemon; uses `service.Install/Uninstall` and control `shutdown` (all injectable for tests — never touch launchctl in tests).
  - **Adapters:** registry rows with detected/enabled, `space` toggles via `actions.SetAdapter`, method shown (`plugin dirs` / `.agents/skills in git repos`).
  - **Import:** `1` import from 1MCP (preview list of servers + warnings, confirm, then apply), `2` import from Claude Code (preview sources, enabled plugins, skills to copy, next steps; confirm; result shows the steps). Both async.
- Doctor tab: runs `doctor.Run` on open and with `r` (async, spinner); findings grouped by severity with `✗`/`!`/`i`, subject, message, wrapped `fix:` line; `enter` on a finding whose subject is a server/source jumps to it.

- [ ] **Step 1: Failing tests:** harness snippets and registration detection on a fake home (ported tests from tussh plus 1MCP detection); Setup rendering with mixed states; copy action; install/uninstall with fake service funcs and confirm; adapter toggle; import previews from the plan-1/plan-2 testdata (1MCP fixture, fake Claude home) and apply after confirm; Doctor rendering of a crafted findings list and rerun; long-running import cancellable (Review Focus 4).
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the Setup and Doctor tabs`.
