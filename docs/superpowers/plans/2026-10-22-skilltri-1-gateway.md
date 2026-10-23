# skilltri Plan 1: MCP Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A working replacement for 1MCP: `skilltri mcp` (stdio) in a harness exposes only the downstream MCP tools, prompts and resources whose folder rules match the session folder, served by one shared daemon.

**Architecture:** `skilltri daemon` owns the config, the downstream connections (go-sdk clients over stdio / Streamable HTTP) and one go-sdk `mcp.Server` per harness session. `skilltri mcp` is a byte relay between stdio and the daemon's Unix socket, preceded by a one-line JSON hello carrying the session folder. Rules are evaluated by one pure package shared with plan 2.

**Tech Stack:** Go 1.26 (module `github.com/baeroe/skilltri`), `github.com/modelcontextprotocol/go-sdk` v1.8.0, `github.com/zalando/go-keyring` v0.2.8, `golang.org/x/term`.

**Spec:** `CONCEPT.md` (repo root). Read it before starting; it is the source of truth for behavior.

**Plan series** (each produces working software on its own; plans 2–4 are written after plan 1 lands, against the code that exists then):
1. **Gateway** (this plan): scaffold, rules, config, secrets, downstream manager, gateway sessions, daemon, shim, `import 1mcp`, `status`/`which`, launchd/systemd.
2. **Skills:** sources (local/git/marketplace), scheduler + lock file, views, adapters (Claude Code + `.agents/skills` harnesses), `shell-init`/`env`/`run`, MCP skill fallback, plugin MCP servers (opt-in, review on change), `import claude`, `doctor`.
3. **TUI:** all tabs (Servers, Skills, rules editor, Preview, Setup, Doctor), harness snippets.
4. **Release:** README with VHS screenshots, GitHub repo + rulesets, Homebrew formula in `baeroe/homebrew-tap`.

## Global Constraints

- `go.mod`: `module github.com/baeroe/skilltri`, `go 1.26.0`. New dependencies only the ones listed in Tech Stack (plan 3 adds bubbletea/bubbles/lipgloss).
- Code, comments, CLI output and docs in English. Every package has a package doc comment (tussh style: what it is, one or two sentences on the key decision).
- Files: config `$SKILLTRI_CONFIG_DIR` → `$XDG_CONFIG_HOME/skilltri` → `~/.config/skilltri` (also macOS); state `$SKILLTRI_STATE_DIR` → `$XDG_STATE_HOME/skilltri` → `~/.local/state/skilltri`; data `$SKILLTRI_DATA_DIR` → `$XDG_DATA_HOME/skilltri` → `~/.local/share/skilltri`; cache `$SKILLTRI_CACHE_DIR` → `$XDG_CACHE_HOME/skilltri` → `~/.cache/skilltri`.
- The config file never contains secrets; secrets are `${secret:NAME}` references resolved from the keyring service `skilltri`. Config and lock files are written with mode `0600`.
- Folder separation is convenience, not a security boundary (CONCEPT.md "Scope of v1").
- Tool and prompt names exposed to harnesses: `<server>_<name>`, at most 64 characters.
- Tests use demo data only: no real hosts, servers, company or client names. Keyring tests use `keyring.MockInit()`.
- `make test` (gofmt check, `go vet`, `go test -race -count=1 ./...`) passes after every task. Commit after every task; commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **macOS path aliases:** a session in `/tmp/x` reports `/private/tmp/x` (seen in the spike); rules written with either spelling must match. → test in Task 2.
2. **A downstream server that fails to start or crashes mid-session:** the other servers' tools stay listed, calls to the dead server return an MCP error result (not a transport error), status shows the error and stderr tail, the next call restarts it. → tests in Tasks 6 and 7.
3. **Stale socket / concurrent starts:** a socket file left by a crashed daemon, or three harness sessions starting at once, must end with exactly one daemon and all shims connected. → tests in Tasks 8 and 9.
4. **Broken config file edited by hand:** the daemon keeps serving the last good config and reports the parse error in `status`; a broken config at first start means zero servers plus the error, not a crash. → test in Task 8.
5. **Unknown client methods and protocol versions** (Claude Code's `server/discover` probe, protocol `2026-07-28`): answered with JSON-RPC "method not found" without hanging the session; `initialize` with `2025-11-25` succeeds. → test in Task 9.

---

### Task 1: Scaffold, paths, build and CI

**Files:**
- Create: `go.mod`, `main.go`, `Makefile`, `.gitignore`, `LICENSE`, `.github/workflows/tests.yml`, `.github/workflows/release.yml`
- Create: `internal/paths/paths.go`, `internal/paths/paths_test.go`

**Interfaces:**
- Produces: `paths.ConfigDir() string`, `paths.StateDir() string`, `paths.DataDir() string`, `paths.CacheDir() string`, `paths.ConfigFile() string` (= `ConfigDir()/config.json`), `paths.SocketPath() string` (= `$SKILLTRI_SOCKET` else `StateDir()/daemon.sock`), `paths.DaemonLock() string` (= `StateDir()/daemon.lock`), `paths.LogFile() string` (= `StateDir()/daemon.log`).
- Produces: `main.run(args []string) int` dispatching subcommands; `var version = "0.1.0"` set via `-ldflags -X main.version`.

- [ ] **Step 1: Copy the conventions from tussh** — `Makefile`, `.gitignore`, `LICENSE`, `tests.yml`, `release.yml` from tussh (`github.com/baeroe/tussh`), replacing `tussh` with `skilltri`. Keep target names (`build test test-unit vet fmt install clean screenshots`), job id `tests`, release title `skilltri ${GITHUB_REF_NAME#v}`. Drop the docker/integration comment from `tests.yml`. `screenshots` target stays but points at `docs/screenshots.sh` (created in plan 4).
- [ ] **Step 2: Write failing tests `TestDirsEnvOverride`, `TestDirsXDG`, `TestDirsDefault`, `TestSocketOverride`** — `t.Setenv` each variable; assert e.g. `SKILLTRI_CONFIG_DIR=/a` → `ConfigDir()=="/a"`; `XDG_CONFIG_HOME=/x` (and no override) → `/x/skilltri`; neither → `$HOME/.config/skilltri`; `SKILLTRI_SOCKET=/s.sock` → `SocketPath()=="/s.sock"`.
- [ ] **Step 3: Run** `go test ./internal/paths/` — expect FAIL (package missing).
- [ ] **Step 4: Implement `internal/paths`** with the precedence from Global Constraints.
- [ ] **Step 5: `main.go`** with usage text listing the plan-1 commands (`mcp`, `daemon`, `daemon install`, `daemon uninstall`, `status`, `which`, `import 1mcp`, `secret set|delete`, `version`) and `skilltri` without args printing usage plus "the TUI arrives in a later version" until plan 3. Unknown command → usage on stderr, exit 2.
- [ ] **Step 6: Run** `make test` — expect PASS; `make build && ./bin/skilltri version` prints `0.1.0`.
- [ ] **Step 7: Commit** `Scaffold skilltri: paths, Makefile, CI`.

### Task 2: Folder rules

**Files:**
- Create: `internal/rules/rules.go`, `internal/rules/rules_test.go`

**Interfaces:**
- Produces:
  ```go
  type Mode string
  const (Only Mode = "only"; Tree Mode = "tree"; Not Mode = "not"; NotTree Mode = "not-tree")
  type Rule struct { Path string `json:"path"`; Mode Mode `json:"mode"` }
  func (r Rule) Validate() error              // known mode, non-empty path, absolute or ~-prefixed
  type Level struct { Name string; Disabled bool; Rules []Rule }
  type Decision struct { Active bool; Reason string }
  func Normalize(path string) string          // ~ expansion, Abs, Clean, EvalSymlinks
  func Evaluate(levels []Level, dir string) Decision // levels outermost → innermost; dir is normalized
  ```

- [ ] **Step 1: Write the failing table test `TestEvaluate`** with `dir` and rules built under `t.TempDir()` (normalized). Rows (rule path `P` = `<tmp>/proj`):
  - no levels with rules → active, reason `no rules: active everywhere`
  - `only P`: dir `P` active; dir `P/sub` inactive; dir `<tmp>/other` inactive
  - `tree P`: `P` active; `P/sub/deep` active; `<tmp>/projX` inactive (prefix must end at a path separator)
  - `not P`: `P` inactive; `P/sub` active; `<tmp>/other` active
  - `not-tree P`: `P` inactive; `P/sub` inactive; `<tmp>/other` active
  - `tree <tmp>` + `not-tree P`: `P/sub` inactive (exclude wins); `<tmp>/other` active
  - two includes `only A`, `only B`: dir `B` active
  - hierarchy: `[{source, Rules: tree A}, {skill, Rules: only B}]` → dir `A` inactive, dir `B` active (nearest level with rules decides)
  - hierarchy: `[{source, Rules: tree A}, {skill}]` → dir `A/x` active (inherits)
  - any level `Disabled` → inactive, reason `disabled: <level name>`, even when rules match
  - reasons for decided rows name the level and rule, e.g. `skill: excluded by not-tree <path>`, `source: included by tree <path>`, `skill: no include matches`
- [ ] **Step 2: Write failing tests `TestNormalize`** — `~/x` → `$HOME/x`; `P/../P/./sub` → `P/sub`; a symlink `L → P` → `Normalize(L/sub)` equals `Normalize(P/sub)`; a non-existing path below an existing symlinked parent resolves the parent and appends the rest; on macOS `/tmp/…` and `/private/tmp/…` normalize equal (Review Focus 1; skip the check when `/tmp` is not a symlink).
- [ ] **Step 3: Write failing test `TestRuleValidate`** — mode `"subtree"` → error; empty path → error; `relative/path` → error; `~/a` ok.
- [ ] **Step 4: Run** `go test ./internal/rules/` — FAIL.
- [ ] **Step 5: Implement.** `Evaluate` normalizes each rule path (memoize per call), then: any disabled level → inactive; pick the innermost level with ≥1 rule (none → active); active = (no include or ≥1 include matches) && no exclude matches. `tree`/`not-tree` match `dir == p || strings.HasPrefix(dir, p+"/")` (special case `p == "/"`).
- [ ] **Step 6: Run** `go test -race ./internal/rules/` — PASS.
- [ ] **Step 7: Commit** `Add folder rules`.

### Task 3: Config store

**Files:**
- Create: `internal/config/config.go`, `internal/config/lock_unix.go`, `internal/config/lock_other.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: `rules.Rule`, `rules.Rule.Validate`, `paths.ConfigFile`.
- Produces:
  ```go
  type Config struct {
      Version  int                `json:"version"` // 1
      Servers  map[string]*Server `json:"servers"`
      Settings Settings           `json:"settings"`
  }
  type Settings struct { IdleTimeoutSec int `json:"idleTimeoutSec,omitempty"` } // 0 → 600
  type Server struct {
      Type             string            `json:"type"` // "stdio" | "http"
      Command          string            `json:"command,omitempty"`
      Args             []string          `json:"args,omitempty"`
      Env              map[string]string `json:"env,omitempty"`
      Cwd              string            `json:"cwd,omitempty"`
      URL              string            `json:"url,omitempty"`
      Headers          map[string]string `json:"headers,omitempty"`
      RequestTimeoutMs int               `json:"requestTimeoutMs,omitempty"` // 0 → 60000
      Disabled         bool              `json:"disabled,omitempty"`
      PerSession       bool              `json:"perSession,omitempty"`
      Rules            []rules.Rule      `json:"rules,omitempty"`
      Tools            map[string]*Tool  `json:"tools,omitempty"`
  }
  type Tool struct { Disabled bool `json:"disabled,omitempty"`; Rules []rules.Rule `json:"rules,omitempty"` }
  func Load(path string) (*Config, error)                  // missing file → empty Version-1 config, no error
  func Update(path string, fn func(*Config) error) error   // flock path+".lock", Load, fn, Validate, atomic write (tmp + rename), 0600
  func (c *Config) Validate() error
  func (s *Server) ConnKey() string                         // stable hash of Type/Command/Args/Env/Cwd/URL/Headers (decides restarts)
  func ValidServerName(name string) bool                   // ^[A-Za-z0-9][A-Za-z0-9-]{0,31}$
  ```
  Leave room for plan 2: unknown JSON fields must survive `Update` (keep a `json.RawMessage` map for unknown top-level keys and round-trip it).

- [ ] **Step 1: Write failing tests:**
  - `TestLoadMissing` → empty config, `Version==1`, no error.
  - `TestUpdateRoundTrip` → `Update` adds a server; `Load` returns it; file mode `0600`.
  - `TestUpdateConcurrent` → 20 goroutines each add one server via `Update`; all 20 present.
  - `TestValidate` → rows: stdio without command; http without URL; type `"sse"`; name `bad name`; invalid rule; a literal secret is fine (validation cannot know) → only the first five fail.
  - `TestUnknownKeysPreserved` → file with `"sources": {...}` survives an `Update`.
  - `TestConnKey` → changing `Rules` or `Disabled` keeps the key; changing `Args` or an env value changes it.
- [ ] **Step 2: Run** `go test ./internal/config/` — FAIL.
- [ ] **Step 3: Implement** (lock files copied in spirit from tussh `internal/config/lock_unix.go`).
- [ ] **Step 4: Run** `go test -race ./internal/config/` — PASS.
- [ ] **Step 5: Commit** `Add config store`.

### Task 4: Secrets

**Files:**
- Create: `internal/secrets/secrets.go`, `internal/secrets/secrets_test.go`
- Modify: `main.go` (commands `secret set NAME`, `secret delete NAME`)

**Interfaces:**
- Produces: `const Service = "skilltri"`; `secrets.Get(name string) (string, error)`; `secrets.Set(name, value string) error`; `secrets.Delete(name string) error`; `secrets.Expand(s string) (string, error)` replacing every `${secret:NAME}` (NAME `[A-Za-z0-9._-]+`) with its keyring value, error naming the missing secret; `secrets.Ref(name string) string` returning `${secret:NAME}`.

- [ ] **Step 1: Write failing tests** (with `keyring.MockInit()`): `TestExpand` (`"Bearer ${secret:tok}"` → `"Bearer abc"`; two refs in one string; no refs unchanged), `TestExpandMissing` (error message contains `tok`), `TestSetGetDelete`.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement.** `secret set NAME` reads the value from stdin; when stdin is a terminal it prompts without echo (`term.ReadPassword`).
- [ ] **Step 4: Run** `go test -race ./internal/secrets/` — PASS. **Step 5: Commit** `Add keyring secrets`.

### Task 5: Import from 1MCP

**Files:**
- Create: `internal/importer/onemcp.go`, `internal/importer/onemcp_test.go`, `internal/importer/testdata/1mcp.json`
- Modify: `main.go` (`import 1mcp [file] [--force]`, default file `~/.config/1mcp/mcp.json`)

**Interfaces:**
- Consumes: `config.Server`, `config.Update`, `config.ValidServerName`, `secrets.Set`, `secrets.Ref`.
- Produces: `importer.From1MCP(data []byte) (Result, error)` with `type Result struct { Servers map[string]*config.Server; Secrets map[string]string; Warnings []string }` (pure, no I/O).

1MCP format: `{"mcpServers": {"<name>": {"type"?, "command"?, "args"?, "env"?, "url"?, "headers"?, "tags"?, "requestTimeout"?, "disabled"?}}}`.

- [ ] **Step 1: Write the fixture** with demo servers only: `docs` (http + `Authorization: Bearer xyz` header + `requestTimeout: 120000`), `browser` (stdio `demo-browser-mcp --headless`, tags `["web"]`), `wiki` (stdio `mcp-remote https://wiki.example.com/mcp`), `legacy` (`type: "sse"`), `off` (`disabled: true`), `notes` (stdio with env `NOTES_API_TOKEN=s3cret` and `NOTES_MODE=fast`).
- [ ] **Step 2: Write failing test `TestFrom1MCP`** asserting: `docs.Type=="http"`, `RequestTimeoutMs==120000`, header value `${secret:docs-authorization}` and `Secrets["docs-authorization"]=="Bearer xyz"`; `browser.Type=="stdio"`; `off.Disabled`; `notes` env `NOTES_API_TOKEN` became `${secret:notes-notes_api_token}` while `NOTES_MODE` stayed literal; `legacy` not imported and a warning mentions `sse`; a warning lists `browser` tags `web` ("set folder rules instead"). Secret rule: every header value, and env values whose key matches `(?i)(token|key|secret|password|auth)`.
- [ ] **Step 3: Run** — FAIL. **Step 4: Implement** `From1MCP`; then the command: parse, skip names that already exist (warning) unless `--force`, `secrets.Set` each secret, one `config.Update`, print imported names and warnings.
- [ ] **Step 5: Run** `go test -race ./internal/importer/` — PASS. **Step 6: Commit** `Import servers from 1MCP`.

### Task 6: Downstream manager

**Files:**
- Create: `internal/downstream/manager.go`, `internal/downstream/conn.go`, `internal/downstream/stderr.go`, `internal/downstream/manager_test.go`
- Create: `internal/testutil/fakemcp/fakemcp.go` (library), `internal/testutil/fakemcp/cmd/fakemcp/main.go` (stdio binary for tests)

**Interfaces:**
- Consumes: `config.Server`, `config.Server.ConnKey`, `secrets.Expand`.
- Produces:
  ```go
  type Options struct {
      Lookup        func(name string) (*config.Server, bool) // current config
      Expand        func(string) (string, error)            // secrets.Expand
      IdleTimeout   time.Duration
      OnListChanged func(server string)                      // downstream tools/prompts/resources changed
  }
  func NewManager(o Options) *Manager
  func (m *Manager) Acquire(ctx context.Context, server, sessionID string) (*Conn, error) // starts on demand; shared unless PerSession
  func (m *Manager) Release(server, sessionID string)                                    // idle timer starts at refcount 0
  func (m *Manager) Restart(server string)                                                // after ConnKey change; open sessions reconnect on next use
  func (m *Manager) Status() []ServerStatus
  func (m *Manager) Close()
  type ServerStatus struct {
      Name string; State string // "stopped" | "starting" | "ready" | "error"
      Error string; StderrTail []string; Tools int; Sessions int; LastCall time.Time
  }
  func (c *Conn) Tools(ctx context.Context) ([]*mcp.Tool, error)       // cached until list_changed
  func (c *Conn) Prompts(ctx context.Context) ([]*mcp.Prompt, error)
  func (c *Conn) Resources(ctx context.Context) ([]*mcp.Resource, error)
  func (c *Conn) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) // RequestTimeoutMs applied
  func (c *Conn) GetPrompt(ctx context.Context, p *mcp.GetPromptParams) (*mcp.GetPromptResult, error)
  func (c *Conn) ReadResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, error)
  ```
  `fakemcp.New(spec Spec) *mcp.Server` with `type Spec struct { Tools []string; Prompts []string; Resources []string; SlowTool string; SlowMs int }`; each tool echoes its arguments as text and its server name. The `cmd/fakemcp` binary reads the spec from env `FAKEMCP_TOOLS`, `FAKEMCP_PROMPTS`, `FAKEMCP_RESOURCES` (comma lists), `FAKEMCP_SLOW` (`name:ms`), `FAKEMCP_EXIT_AFTER` (exit after N tool calls, for crash tests), writes `os.Getwd()` to `FAKEMCP_CWD_FILE` if set, and prints one line to stderr at start.

- [ ] **Step 1: Write the fake server** (library + binary). Tests build the binary once in `TestMain` with `go build -o <tmp>/fakemcp ./internal/testutil/fakemcp/cmd/fakemcp`.
- [ ] **Step 2: Write failing tests:**
  - `TestStdioToolsAndCall` → server `a` (stdio fakemcp, tools `x,y`): `Tools` returns `x,y`; `CallTool("x", {"v":1})` echoes `{"v":1}`.
  - `TestHTTPWithHeaders` → `httptest` server with `mcp.NewStreamableHTTPHandler` around `fakemcp.New`, middleware rejecting requests without `Authorization: Bearer abc`; config header `${secret:t}` with mock keyring `t=Bearer abc` → `Tools` works.
  - `TestSharedVsPerSession` → two sessions on a shared server get the same `*Conn`; with `PerSession` different ones (fakemcp writes its PID into the echo; assert two PIDs).
  - `TestIdleStop` → `IdleTimeout=100ms`; after `Release` and 300 ms, `Status()` shows `stopped`; next `Acquire` starts it again.
  - `TestRequestTimeout` → `FAKEMCP_SLOW=slow:2000`, `RequestTimeoutMs=200` → `CallTool` returns an error within 1 s mentioning `timeout`.
  - `TestStartFailure` → command `/nonexistent` → `Acquire` error; `Status()` state `error` with message (Review Focus 2).
  - `TestCrashRestart` → `FAKEMCP_EXIT_AFTER=1`: first call ok, second call returns an error, `Status()` shows `error` with the stderr tail line, third call (after `Acquire` again) succeeds on a fresh process (Review Focus 2).
  - `TestListChanged` → library fakemcp over in-memory or HTTP transport adds a tool at runtime → `OnListChanged("a")` fires and `Tools` returns the new list.
- [ ] **Step 3: Run** `go test ./internal/downstream/` — FAIL.
- [ ] **Step 4: Implement.** stdio: `mcp.CommandTransport` with `exec.Cmd` (env = `os.Environ()` + expanded `Env`, `Dir = Cwd`, stderr into a 50-line ring buffer); http: `mcp.StreamableClientTransport` with an `http.Client` whose `RoundTripper` adds expanded headers. Client options set `ToolListChangedHandler`, `PromptListChangedHandler`, `ResourceListChangedHandler` → invalidate cache + `OnListChanged`. A `Conn` whose session ended is marked dead; `Acquire` replaces dead conns.
- [ ] **Step 5: Run** `go test -race ./internal/downstream/` — PASS. **Step 6: Commit** `Add downstream connection manager`.

### Task 7: Gateway sessions

**Files:**
- Create: `internal/gateway/effective.go`, `internal/gateway/names.go`, `internal/gateway/session.go`, `internal/gateway/gateway_test.go`

**Interfaces:**
- Consumes: `rules.Evaluate`, `rules.Level`, `config.Config`, `downstream.Manager`, `downstream.Conn`.
- Produces:
  ```go
  type ServerDecision struct { Name string; Decision rules.Decision }
  func Servers(cfg *config.Config, dir string) []ServerDecision                    // all servers, sorted by name
  func ToolDecision(cfg *config.Config, server, tool, dir string) rules.Decision    // levels [server, tool]
  func ExposedName(server, name string) string  // "<server>_<name>"; if > 64 chars: first 55 chars + "_" + 8 hex of sha256(server+"/"+name)
  var Version = "0.1.0"                          // serverInfo version; main sets it like tussh sets mcp.Version
  type SessionInfo struct { ID string; Dir string; ClientName string }
  func NewSession(info SessionInfo, cfg func() *config.Config, mgr *downstream.Manager) *Session
  func (s *Session) MCPServer() *mcp.Server               // to Connect on the session transport
  func (s *Session) Refresh(ctx context.Context) error     // recompute and add/remove tools, prompts, resources
  func (s *Session) SetDir(ctx context.Context, dir string) // from roots; Refresh if changed
  func (s *Session) Info() SessionInfo
  func (s *Session) UsesServer(name string) bool
  func (s *Session) Close()                                // Release all acquired servers
  ```
  Server names cannot contain `_`, so `<server>_` prefixes never collide; only the 64-character shortening needs the hash.
  Resources keep their original URIs; a route table maps URI → server; on collision the server first by name wins and the other is logged. Server instructions (set at `NewSession`): `"skilltri gateway. Tools and prompts are named <server>_<name>. Servers active for <dir>: <comma list>."`

- [ ] **Step 1: Write failing tests:**
  - `TestServersAndToolDecision` → config with `a` (no rules), `b` (`only P`), `c` (`Disabled`), tool rule `a.tools.x = not-tree P`: for dir `P` → `a` active, `b` active, `c` inactive; `ToolDecision(a, x, P)` inactive; `ToolDecision(a, y, P)` active.
  - `TestExposedName` → `("docs","search")` = `docs_search`; a 70-char tool name gives a 64-char result that is stable across calls and differs for two names sharing the first 60 characters.
  - `TestSessionLists` → in-memory client (`mcp.NewInMemoryTransports`) connected to `MCPServer()`; downstream fakemcp `a` (tools `x,y`), `b` (`only P`, tool `z`); session dir `P/sub` → client sees `a_x`, `a_y` only; `CallTool("a_x")` returns the echo.
  - `TestSessionSetDir` → same setup, `SetDir(P)` → client receives a tools list-changed notification and now also sees `b_z`.
  - `TestSessionRefreshOnConfigChange` → swap the config returned by `cfg` to disable `a`, `Refresh` → `a_*` gone, notification received.
  - `TestSessionDeadServer` → `a` crashes (`FAKEMCP_EXIT_AFTER=1`): second call to `a_x` returns `CallToolResult.IsError` with text naming server `a` (not a protocol error); `b`'s tools stay listed (Review Focus 2).
  - `TestSessionPromptsResources` → prompt `p` on `a` listed as `a_p` and `GetPrompt` forwards; resource `demo://a/r` readable through the session.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.** One `mcp.NewServer(&mcp.Implementation{Name: "skilltri", Version: Version}, opts)` per session; `Refresh` diffs the wanted tool set against the registered one and calls `AddTool`/`RemoveTools` (go-sdk sends list-changed). Proxy handlers forward `req.Params.Arguments` raw. Servers that fail to `Acquire` are skipped and logged, never fatal.
- [ ] **Step 4: Run** `go test -race ./internal/gateway/` — PASS. **Step 5: Commit** `Add gateway sessions`.

### Task 8: Daemon

**Files:**
- Create: `internal/daemon/daemon.go`, `internal/daemon/hello.go`, `internal/daemon/control.go`, `internal/daemon/watch.go`, `internal/daemon/daemon_test.go`
- Modify: `main.go` (`daemon` runs in the foreground, logs to stderr)

**Interfaces:**
- Consumes: `config.Load`, `config.Server.ConnKey`, `downstream.NewManager`, `gateway.NewSession`, `rules.Normalize`, `secrets.Expand`, `paths.*`.
- Produces:
  ```go
  var ErrAlreadyRunning = errors.New("skilltri daemon already running")
  type Options struct { ConfigPath, SocketPath, LockPath string; Version string; Logger *log.Logger; PollInterval time.Duration } // PollInterval 0 → 1s
  func New(o Options) *Daemon
  func (d *Daemon) Run(ctx context.Context) error
  // Hello is the first line (JSON + "\n") a client writes on the socket.
  type Hello struct {
      V           int      `json:"v"`             // 1
      Kind        string   `json:"kind"`          // "session" | "control"
      Dir         string   `json:"dir,omitempty"`
      DirExplicit bool     `json:"dirExplicit,omitempty"` // from --cwd/SKILLTRI_CWD: roots never override it
      Cmd         string   `json:"cmd,omitempty"` // control: "status" | "which"
      Args        []string `json:"args,omitempty"`
  }
  type StatusReply struct {
      PID int; Version string; ConfigError string
      Servers  []downstream.ServerStatus
      Sessions []gateway.SessionInfo
  }
  type WhichReply struct { Dir string; Servers []WhichServer }
  type WhichServer struct { Name string; Decision rules.Decision; State string; Tools []WhichTool } // Tools only for servers that are ready
  type WhichTool struct { Name string; Exposed string; Decision rules.Decision }
  func Control(socket string, h Hello, reply any) error // client helper used by status/which
  ```

- [ ] **Step 1: Write failing tests** (socket in `os.MkdirTemp("/tmp", "st")` because macOS limits socket paths to 104 bytes):
  - `TestSingleInstance` → second `Run` on the same lock returns `ErrAlreadyRunning`.
  - `TestStaleSocket` → create a plain file at the socket path, `Run` removes it and listens (Review Focus 3).
  - `TestSessionOverSocket` → write a session hello, then drive MCP with a go-sdk client over `mcp.IOTransport` on the conn → `tools/list` filtered by `Dir`.
  - `TestRootsOverrideDir` → client with roots capability returning one `file://` root `P` while hello `Dir` is `P/sub` → after the roots answer the session's dir is `P` and a list-changed notification arrived; with `DirExplicit` the dir stays.
  - `TestConfigReload` → rewrite config adding a server, wait ≤ 3 × PollInterval → client sees its tools; changing only rules does not restart the downstream process (same PID in echo); changing args does.
  - `TestBrokenConfig` → write `{` → status `ConfigError` non-empty, previous tools still served; start a daemon on a broken config → zero servers, `ConfigError` set, no exit (Review Focus 4).
  - `TestControlStatusWhich` → `Control(status)` lists the session with its dir; `Control(which, [P])` returns decisions with reasons.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.** `Run`: non-blocking `flock` on `LockPath` (→ `ErrAlreadyRunning`), remove any existing socket file (safe: we hold the lock), listen, socket mode `0600`. Per connection: read the hello with a `bufio.Reader`, then for sessions use `mcp.IOTransport{Reader: <that bufio.Reader>, Writer: conn}` so no bytes are lost. Session `Dir` = `rules.Normalize(hello.Dir)`; after `initialized`, if the client has roots and not `DirExplicit`, call `ListRoots` and `SetDir` when exactly one `file://` root. Config watch: poll mtime+size; on change `Load`; on error keep the old config and set `ConfigError`; on success `Restart` servers whose `ConnKey` changed or that were removed, then `Refresh` all sessions. `OnListChanged(server)` refreshes sessions with `UsesServer(server)`.
- [ ] **Step 4: Run** `go test -race ./internal/daemon/` — PASS. **Step 5: Commit** `Add daemon`.

### Task 9: `skilltri mcp` shim and end-to-end test

**Files:**
- Create: `internal/shim/shim.go`, `internal/shim/shim_test.go`, `e2e_test.go` (package `main`, repo root)
- Modify: `main.go` (`mcp [--cwd DIR]`)

**Interfaces:**
- Consumes: `daemon.Hello`, `paths.SocketPath`, `paths.LogFile`.
- Produces:
  ```go
  func Dial(socket string, start func() error) (net.Conn, error) // on ENOENT/ECONNREFUSED: start(), retry every 100 ms up to 5 s
  func StartDetached(exe string) error                            // exe "daemon", Setsid, stdin /dev/null, stdout+stderr → paths.LogFile()
  func Run(ctx context.Context, in io.Reader, out io.Writer, conn net.Conn, h daemon.Hello) error // write hello, relay both ways, CloseWrite on stdin EOF
  ```
  `main mcp`: dir = `--cwd` or `SKILLTRI_CWD` (`DirExplicit=true`) else `os.Getwd()`; `exe` from `os.Executable()`.

- [ ] **Step 1: Write failing tests:**
  - `TestDialStartsDaemon` (shim) → `start` func that launches an in-process daemon after 300 ms → `Dial` succeeds; `start` called once.
  - `e2e TestMCPEndToEnd` → `TestMain` builds `skilltri` and `fakemcp` into a temp dir; env `SKILLTRI_CONFIG_DIR/STATE_DIR/DATA_DIR/CACHE_DIR` temp, `SKILLTRI_SOCKET` short `/tmp` path; config with `alpha` (fakemcp, no rules) and `beta` (`only <proj>`); start `skilltri mcp` with `cmd.Dir=<proj>/sub` and drive it with a go-sdk client over `mcp.CommandTransport` → sees `alpha_*`, not `beta_*`; a second client in `<proj>` sees both; `skilltri status --json` lists two sessions; after the test the daemon is stopped (kill the PID from status).
  - `e2e TestConcurrentStarts` → three `skilltri mcp` processes started at once with no daemon → all three list tools; `status` reports one PID (Review Focus 3).
  - `e2e TestUnknownMethod` → raw newline JSON over the shim's stdin: `{"jsonrpc":"2.0","id":"p1","method":"server/discover","params":{}}` → response with error code `-32601` within 2 s; then `initialize` with `protocolVersion "2025-11-25"` succeeds (Review Focus 5).
- [ ] **Step 2: Run** `go test ./internal/shim/ .` — FAIL.
- [ ] **Step 3: Implement** shim and `mcp` command.
- [ ] **Step 4: Run** `make test` — PASS. **Step 5: Commit** `Add skilltri mcp shim and end-to-end tests`.

### Task 10: `status`, `which`, autostart service

**Files:**
- Create: `internal/service/service.go`, `internal/service/service_test.go`
- Create: `internal/cli/status.go`, `internal/cli/which.go`, `internal/cli/cli_test.go`
- Modify: `main.go`

**Interfaces:**
- Consumes: `daemon.Control`, `daemon.StatusReply`, `daemon.WhichReply`, `gateway.Servers`, `config.Load`.
- Produces: `service.Label = "io.github.baeroe.skilltri"`; `service.LaunchdPlist(exe string) string`; `service.SystemdUnit(exe string) string`; `service.Install(exe string) error`; `service.Uninstall() error`; `cli.Status(w io.Writer, jsonOut bool) int`; `cli.Which(w io.Writer, dir string, jsonOut bool) int`.

- [ ] **Step 1: Write failing tests:**
  - `TestLaunchdPlist` → contains `<string>io.github.baeroe.skilltri</string>`, `ProgramArguments` = `[exe, "daemon"]`, `RunAtLoad` true, `KeepAlive` true, `StandardErrorPath` = `paths.LogFile()`.
  - `TestSystemdUnit` → `ExecStart=<exe> daemon`, `Restart=on-failure`, `WantedBy=default.target`.
  - `TestWhichOffline` → no daemon running: `Which` still prints server decisions from the config (with reasons) and notes `daemon not running: tool lists unavailable`; exit 0.
  - `TestStatusOffline` → prints `daemon not running`, exit 1.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.** `Install`: darwin writes `~/Library/LaunchAgents/io.github.baeroe.skilltri.plist` and runs `launchctl bootstrap gui/<uid> <plist>` (bootout first if loaded); linux writes `~/.config/systemd/user/skilltri.service` and runs `systemctl --user enable --now skilltri.service`; other OS → error. Human output of `status`: one line per server `name  state  tools  sessions  [error]`, then sessions `dir  client`. `which`: `✓`/`✗` per server with reason, indented tools for ready servers.
- [ ] **Step 4: Run** `make test` — PASS.
- [ ] **Step 5: Manual check on the author's machine (not in CI):** `make install`; `skilltri import 1mcp`; `skilltri daemon install`; `skilltri status` shows all imported servers starting/ready; `claude -p` in a test folder with `skilltri mcp` registered via `--mcp-config` lists the tools. Report the output; do not change the harness registration or stop 1MCP (the user does that).
- [ ] **Step 6: Commit** `Add status, which and daemon autostart`.
