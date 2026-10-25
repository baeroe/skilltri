# skilltri Plan 5: Agent Management Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents can read and change skilltri through its own MCP tools (`skilltri_*`) in every harness session: inspect servers, skills, rules and health, and add/edit/remove servers, sources, rules, local skills, adapters and plugin servers. Access is controlled by one setting and every change is written to an audit log.

**Architecture:** A new package `internal/agenttools` defines the tools (name, description, JSON schema, handler) against a small `Backend` interface. The daemon implements the backend (config path, catalog, status, which, update, doctor, `actions.Env`) and hands a tool provider to each gateway session; the session registers the tools next to the skill fallback tools. All writes go through `internal/actions`, so CLI, TUI and agents share one code path. `internal/audit` appends one JSON line per agent write.

**Tech Stack:** Go 1.26, existing packages, go-sdk v1.8.0.

**Spec:** user decision 2026-10-25: agents may change skilltri directly (no approval step). This plan adds that to CONCEPT.md.

## Global Constraints

- Everything from plans 1–4 applies (English, package doc comments, demo data only in tests, never touching the real `~/.claude`, `~/.config/skilltri`, keychain or a running real daemon, `make test` green, new commits only, Co-Authored-By naming the model).
- Setting `settings.agentAccess`: `"off"` (no `skilltri_*` management tools), `"read"` (read tools only), `"write"` (read + write tools). Empty = `"write"` (the author's choice). The skill fallback tools (`skilltri_load_skill`, `skilltri_read_skill_file`) are not affected by this setting.
- Agents never see secret values: tool output masks env/header values (`${secret:…}` refs shown as-is, literals as `•••`), no tool reads the keyring, no tool writes the keyring. An agent may set env/header values; they are stored as given (a `${secret:NAME}` reference or a literal).
- Every write tool call appends to `StateDir/audit.jsonl`: `{"time", "tool", "target", "summary", "client", "dir", "ok", "error"}` — never argument values of env/headers.
- Tool names: `skilltri_<verb>_<noun>`, ≤ 64 chars; descriptions say what the tool does and its effect on running sessions (config changes reach every session within seconds via the daemon's config watch).
- Write tools return a short confirmation plus warnings (e.g. unknown target warnings from `actions`), errors as `IsError` results with the actions error text.

## Review Focus

1. **Access levels:** switching `agentAccess` in the config takes effect in running sessions (tools appear/disappear via list_changed) without restarting the harness.
2. **Secrets:** no tool output, audit line or error message contains a literal env/header value.
3. **Self-lockout / runaway edits:** an agent removing or disabling skilltri's own access is impossible through the tools (`agentAccess` is not writable by agents; the server name `skilltri` is reserved); a failing write leaves the config byte-identical.
4. **Concurrency:** simultaneous tool calls from several sessions and a TUI edit at the same time all go through `config.Update` without lost updates.

---

### Task 1: Tool framework, read tools, access setting, audit log

**Files:**
- Create: `internal/agenttools/agenttools.go` (types, registry, access filtering, masking helpers), `internal/agenttools/read.go`, `internal/agenttools/agenttools_test.go`, `internal/audit/audit.go`, `internal/audit/audit_test.go`
- Modify: `internal/config/config.go` (`Settings.AgentAccess string` + validation of the three values + `(*Config).AgentAccess() string` defaulting to `"write"`), `internal/gateway/session.go` + `internal/gateway/skills.go` (register provider tools next to the skill tools, re-evaluated on Refresh), `internal/daemon/daemon.go` (+ new `internal/daemon/backend.go` implementing the backend), `internal/paths/paths.go` (`AuditLog() string` = `StateDir()/audit.jsonl`)

**Interfaces:**
- Produces:
  ```go
  type Backend interface {
      Config() *config.Config
      Catalog() *catalog.Catalog
      Status() daemon.StatusReply        // without importing daemon in agenttools: define an equivalent small struct or pass funcs; avoid import cycles (agenttools must not import daemon)
      Which(dir string) daemon.WhichReply // same note
      Doctor(ctx context.Context) []doctor.Finding
      Update(ctx context.Context, sources []string) []catalog.SourceState
      Env() actions.Env
  }
  type Call struct { Client, Dir string }           // session info for audit and defaults
  type Tool struct { Def *mcp.Tool; Write bool; Handle func(ctx context.Context, b Backend, c Call, args json.RawMessage) (*mcp.CallToolResult, error) }
  func Tools(access string) []Tool                  // filtered by access
  ```
  Choose concrete types that avoid an import cycle (agenttools ← daemon). Read tools: `skilltri_get_status` (daemon, servers with state/tools/errors, sources with commit/error, sessions count), `skilltri_which` (`{dir?}` default the session folder: servers, tools, skills with decisions and reasons), `skilltri_list_servers` (config view with masked env/headers, rules, tool settings, origin), `skilltri_list_skills` (`{dir?, source?, filter?}` catalog entries with enabled/active state, description, source/plugin, rules), `skilltri_list_sources` (sources with type, url/path, ref, interval, pluginsDefault, plugins with enabled state, last commit/error), `skilltri_run_doctor`.
  Audit: `audit.Append(path string, e audit.Entry) error` (O_APPEND, 0600, one line, mutex + flock), `audit.Read(path string, n int) ([]audit.Entry, error)` (last n).

- [ ] **Step 1: Failing tests:** access filtering (`off` → none, `read` → read only, `write` → all); masking (literal env value never appears in `skilltri_list_servers` output, refs do); `skilltri_which` defaults to the session folder; each read tool returns valid JSON text content for a demo backend; gateway session registers the tools and drops them after the config switches to `off` (list_changed); audit append/read round trip and concurrent appends; config validation of `agentAccess`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add agent tools framework, read tools and the audit log`.

### Task 2: Write tools for servers, rules, toggles, adapters and plugin servers

**Files:**
- Create: `internal/agenttools/write_servers.go`, `internal/agenttools/write_rules.go`, `internal/agenttools/write_test.go`

**Interfaces:** each tool calls the matching `actions` function with `Backend.Env()`, audits, returns confirmation + warnings.
- `skilltri_add_server` (`{name, type, command?, args?, env?, cwd?, url?, headers?, requestTimeoutMs?, perSession?, disabled?}`), `skilltri_update_server` (`{name, …same fields…, newName?}` — only given fields change; rename via `actions.ServerRename`), `skilltri_remove_server` (`{name}`), `skilltri_restart_server` (`{name}` via backend restart).
- `skilltri_set_enabled` (`{target, enabled}` with target syntax `server/<n>`, `server/<n>/tool/<t>`, `source/<n>`, `source/<n>/plugin/<entry>`, `source/<n>/skill/<id>`).
- `skilltri_add_rule` (`{target, mode, path}`), `skilltri_remove_rule` (`{target, index, mode, path}` — index 1-based plus the expected rule; mismatch → error via `actions.RuleSet` expect), `skilltri_set_rules` (`{target, rules: [{mode,path}]}` replacing the list).
- `skilltri_set_adapter` (`{id, enabled}`), `skilltri_enable_plugin_server` (`{ref: "source/plugin/server", name?}`), `skilltri_accept_plugin_server` (`{name}`).
- Rejected for agents: server name `skilltri`, any change to `settings.agentAccess` (no tool exists for it).

- [ ] **Step 1: Failing tests:** each tool happy path changes the config as specified and appends one audit line without env/header values; validation errors leave the config byte-identical and return IsError; `skilltri_update_server` only changes given fields (keeps rules, tools, origin, disabled unless given); `skilltri_remove_rule` with a stale expected rule fails; concurrent `skilltri_add_rule` calls from two goroutines both land (Review Focus 4); an e2e test through `skilltri mcp` (real binary, sandbox) adds a server and sees its tools appear via list_changed.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add agent write tools for servers, rules and adapters`.

### Task 3: Write tools for sources and skills, TUI/CLI/docs

**Files:**
- Create: `internal/agenttools/write_skills.go`, tests; `internal/cli/audit.go`
- Modify: `internal/tui/setup.go` (Agent access row: `off`/`read`/`write` cycled with space, writes `settings.agentAccess` via a new `actions.SetAgentAccess`), `internal/actions` (`SetAgentAccess(e, level)`, `SkillWrite(e, id, content)` for local-source skills only), `main.go` (`skilltri audit [-n N] [--json]`), `README.md`, `CONCEPT.md` (agent tools section, decision recorded), `internal/doctor` (info finding when agentAccess is `write`: "agents can change skilltri; see skilltri audit")

**Interfaces:**
- `skilltri_add_source` (`{name, type, path?, url?, ref?, subpath?, updateEvery?, plugins?}`), `skilltri_remove_source` (`{name}`), `skilltri_update_sources` (`{sources?: []}` via backend Update; returns per-source commit/error), `skilltri_new_skill` (`{name, description, body}` → local source `local`, writes SKILL.md with frontmatter), `skilltri_write_skill` (`{id, content}` — full SKILL.md content; only skills of `local`-type sources; validates frontmatter name = folder; refuses other sources with an explanation), `skilltri_read_skill` (`{id}` read-only even under `read` access — returns SKILL.md of any active or inactive catalog skill).
- `skilltri audit` prints the last N entries (`time  client  tool  target  ok/error`).

- [ ] **Step 1: Failing tests:** source add/remove/update via tools; new_skill creates file + local source once; write_skill refuses git/marketplace skills and invalid frontmatter; read_skill works under `read`; TUI agent access row cycles and persists; `skilltri audit` output; doctor info finding.
- [ ] **Step 2–4:** RED, implement, GREEN; update README (new "Agent tools" section with the tool list, access levels, audit) and CONCEPT. **Step 5: Commit** `Add agent tools for sources and skills, access setting in the TUI and skilltri audit`.
