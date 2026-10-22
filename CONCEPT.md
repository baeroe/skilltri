# skilltri: concept and roadmap

skilltri is one place to manage the **MCP servers** and **agent skills** your AI agents get, with **per-folder rules** for both. It replaces a plain MCP gateway (such as 1MCP) plus harness-specific skill folders and plugin marketplaces.

- **You** run `skilltri` in any terminal: a TUI to add MCP servers, skill sources (local folders, GitHub repos, Claude plugin marketplaces) and the folder rules that decide where each one is active.
- **Agents** get everything through two channels: one MCP server (`skilltri mcp`, stdio) that exposes only the tools that apply to the session's folder, and native skill folders that skilltri builds per folder for each harness.

skilltri is open source and must work for every harness, not only Claude Code. Claude Code is the first-class target (it is what the author uses); the other harnesses come in through adapters.

## Why

- A gateway like 1MCP exposes every server everywhere. Work, client and private projects need different servers (and must not see each other's tools).
- Skills live in harness-specific folders (`~/.claude/skills`, `.agents/skills`, plugin caches). A user-level folder cannot differ per session, so "everywhere except this project" is impossible today.
- Skills from GitHub repos and marketplaces should stay up to date without manual pulls.

## Scope of v1

v1 is everything needed to replace 1MCP and a plugin/skill-based Claude Code setup, plus the other harnesses:

- folder rules, the MCP gateway (daemon + `skilltri mcp`), all three source types including marketplaces and opt-in plugin MCP servers
- adapters for Claude Code **and** the `.agents/skills` harnesses (Codex, Gemini CLI, OpenCode, Cursor, Copilot CLI, Amp), shell hook and `skilltri run`, the MCP skill fallback
- `import 1mcp`, `import claude`, `doctor` (CLI and TUI tab)
- daemon autostart through launchd (macOS) and a systemd user unit (Linux)
- the full TUI (Servers, Skills, rules editor, Preview, Setup, Doctor)
- release through the existing Homebrew tap `baeroe/homebrew-tap`

After v1: the `herdr-skilltri` plugin (its own repo), native OAuth, the items under "Out of scope for now".

**Folder separation is a convenience, not a security boundary.** It keeps tool lists and skills relevant per folder; it does not defend against a client that lies about its folder. Downstream servers are shared between sessions unless `perSession: true`. `read_skill_file` still only serves files inside the skill's folder.

## Folder rules

Every MCP server, tool, skill source, plugin and skill can carry rules. A rule is a path plus one of four modes:

| Mode | Active when the session folder is … |
|---|---|
| `only` | exactly this folder |
| `tree` | this folder or any folder below it |
| `not` | any folder except exactly this one |
| `not-tree` | any folder except this one and the folders below it |

- **Session folder** is the folder the harness was started in (see "Which folder is the session in?"). Paths are compared after `~` expansion, `filepath.Clean` and symlink resolution (`EvalSymlinks`), so `~/dev` and `/Users/x/dev` match.
- **Several rules on one item:** `only`/`tree` are includes, `not`/`not-tree` are excludes. An item is active if it has no includes or at least one include matches, **and** no exclude matches. Excludes win. *(builder call, confirmed in chat without objection)*
- **No rules = active everywhere.**
- **Matching is path-only in v1.** `internal/rules` matches through a small matcher interface so a later matcher (git remote, repo name) can be added without changing the evaluation; the shell-hook fast path never runs git. *(builder call)*
- **Hierarchy** (source → plugin → skill; server → tool): each level also has an on/off switch. An item is active only if all its ancestors are switched on, and its rules are the rules of the **nearest level that has any** (a skill's own rules replace its plugin's, which replace the source's). *(builder call)*
- One package (`internal/rules`) evaluates this for both MCP and skills, with a table-driven test suite. The TUI "Preview" tab and `skilltri which <dir>` show the result **and the reason** (which rule on which level decided).

## Architecture

| | Human | Agent |
|---|---|---|
| Entry point | `skilltri` TUI | `skilltri mcp` (stdio, registered once in each harness) |
| MCP servers | Servers tab | tools filtered by the session folder |
| Skills | Skills tab (sources → plugins → skills) | native per-folder views (adapters) + MCP fallback |
| Background | `skilltri daemon` (launchd/systemd user unit) | — |

- **`skilltri daemon`** holds the real connections to the downstream MCP servers, pulls skill sources on a schedule, builds the skill views and watches the config (hot reload). It listens on a Unix socket in the state dir.
- **`skilltri mcp`** is a thin stdio process. It sends a hello (session folder, MCP roots if the client sends them, client name from `initialize`) to the daemon and then relays the JSON-RPC stream. The daemon runs the actual MCP session logic, so all policy lives in one process. If no daemon is running, `skilltri mcp` starts one (detached) and connects.
- **Files:** config in `$SKILLTRI_CONFIG_DIR`, else `$XDG_CONFIG_HOME/skilltri`, else `~/.config/skilltri` (also on macOS, like tussh). State (socket, logs, lock file) in `~/.local/state/skilltri`, source checkouts in `~/.local/share/skilltri/sources`, views in `~/.cache/skilltri/views`. Config writes are a locked read-modify-write plus atomic rename (same pattern as tussh's `connections.json`).
- **Secrets** (tokens, API keys in env/headers) live in the keyring (`zalando/go-keyring`, like tussh), referenced from the config. The config file never contains secrets.

### Which folder is the session in?

In this order: `--cwd`/`SKILLTRI_CWD` (for harnesses that need it), the single MCP root if the client sends exactly one, then the process working directory of `skilltri mcp`. Harnesses start stdio servers in the project folder; verifying this per harness is part of the spike.

## MCP gateway

- **Downstream transports in v1:** stdio and Streamable HTTP (custom headers, bearer token). Legacy SSE only if a real server needs it. OAuth remotes run through `mcp-remote <url>` as a stdio server, as they do in 1MCP today; native OAuth is later.
- **Client side** uses the official Go SDK (`github.com/modelcontextprotocol/go-sdk`) *(builder call: Streamable HTTP and session handling are not worth rewriting)*. The harness-facing server may use the SDK too or stay minimal like tussh's; the builder decides.
- **Passed through:** tools, prompts and resources, plus `notifications/*/list_changed`. Sampling and elicitation are out of scope for v1.
- **Naming:** tools are exposed as `<server>_<tool>`, with a collision check and a stable shortening for names over 64 characters.
- **Lifecycle:** a downstream server starts when the first session that may use it asks for its list, and stops after an idle timeout. Servers are shared between sessions by default; `perSession: true` gives each session its own instance (for stateful servers such as a browser). *(builder call)*
- **Different credentials per area** (e.g. a work and a private token for the same service) are two servers with different rules, not variants of one. *(builder call)*
- **Per-server settings:** command/args/env or URL/headers, `requestTimeout` (ms, like 1MCP), enabled, rules, per-tool on/off and rules.
- **Hot reload:** on a config change the daemon recomputes each session's set and sends `tools/list_changed` (and the prompts/resources equivalents) only to sessions whose set changed.
- **Status:** connected / starting / error with the last stderr lines, tool count, last call. Shown in the TUI and in `skilltri status`.

## Skills

### Sources

| Type | What | Updates |
|---|---|---|
| `local` | a folder under `~/.config/skilltri/skills/` (created and edited from the TUI, opens `$EDITOR`) or any folder you point at | none, read live |
| `git` | GitHub shorthand `owner/repo` or any git URL, optional ref (branch/tag/commit) and subpath; may contain many skills | daemon `git fetch` |
| `marketplace` | a repo or folder with `.claude-plugin/marketplace.json` | daemon `git fetch`, then re-resolve plugins |

- **Discovery:** every folder with a `SKILL.md` below the source (or below its subpath) is a skill, following the agentskills.io spec (`name` `[a-z0-9-]`, ≤ 64 characters, must match the folder name; `description` ≤ 1024). Invalid frontmatter is a warning (shown in Doctor), not a crash.
- **Marketplaces:** skilltri reads `marketplace.json` and resolves each plugin's `source`. v1 supports relative paths (`./…`), `github`, `url` (git) and `git-subdir`; `npm`, `archive` and `command` come later. From each plugin it takes `skills/` plus the extra paths of a `skills` key in `plugin.json`. Plugin skills keep their plugin as a namespace: `superpowers:brainstorming`.
- **Plugin MCP servers:** a plugin's `.mcp.json` servers appear in the TUI under the plugin as **available, off**. You switch each one on, and it then becomes a normal, standalone gateway server: at that moment it gets a **copy of the plugin's effective rules** (visible and editable), so a plugin scoped to one folder never leaks its server everywhere; later rule changes on the plugin do not touch it. *(builder call)* If an update changes an enabled server's definition (command, args, URL, env), the server is held at the old definition and marked **needs review** until you accept the change, because an update could change what gets executed.
- **Not taken from plugins in v1:** hooks, subagents, LSP servers, `bin/`, settings. Converting legacy `commands/*.md` into skills is later.
- **Updates:** the daemon fetches every `git`/`marketplace` source on an interval (default 1 h, per source configurable, `0` = manual). `skilltri update [source]` does it now. A ref that is a commit SHA is pinned and never moves. A lock file in the state dir records the resolved commit and a content hash per skill, so Doctor can detect local tampering and an update can be shown as a diff summary in the TUI.

### Delivery to harnesses

Research result that shapes this: Claude Code and Codex load project skills from the start folder **and every parent up to the repo root**. Writing skills into project folders would therefore leak them into sessions started in subfolders and break `only` and `not-tree`. So the default is to build **views outside the project** and point the harness at them.

**Views.** For a session folder the daemon computes the active skill set and builds a view under `~/.cache/skilltri/views/<hash of the set>/<adapter>/`. Folders with the same set share a view. Views are rebuilt when the set changes and garbage-collected when unused.

**Adapters** (one registry table, like lazyskills' agent table: id, display name, detection, delivery method, project/user dirs):

1. **Claude Code (v1).** Claude Code loads extra plugin folders from `CLAUDE_CODE_PLUGIN_DIRS` (colon-separated, like `--plugin-dir`, since v2.1.280). The view contains one generated plugin per source plugin (`<plugin>/.claude-plugin/plugin.json` + `skills/`), so the original namespace survives, and one plugin named `skilltri` (configurable) for skills from `local` and `git` sources. Claude rejects symlinks that leave a plugin, so the view **copies** skill folders (or hardlinks them where possible) instead of symlinking; this is verified in the spike.
2. **`.agents/skills` harnesses (Codex, Gemini CLI, OpenCode, Cursor, Copilot CLI, Amp, …).** Where a harness has an env variable or config path for its skill folder, the adapter uses a view the same way. Where it has none, the adapter falls back to symlinks in `<session folder>/.agents/skills/skilltri-*`, adds them to `.git/info/exclude`, and removes them when they no longer apply. The TUI marks that adapter with the parent-folder caveat.

**Getting the env into the harness:**

- **Shell hook** (default): `eval "$(skilltri shell-init zsh)"` (zsh, bash, fish). On every `cd` it asks the daemon for the view of the new folder and exports the adapter variables (direnv-style, fast path from a local cache so `cd` never blocks noticeably).
- **Launcher:** `skilltri run <harness> [args…]` sets the variables and execs the harness, for setups without the hook.
- The view exists before the harness starts, so no hot-reload or `/reload-skills` is needed.

**MCP fallback.** `skilltri mcp` lists the active skills (name + description) in its server instructions and offers `load_skill(name)` and `read_skill_file(name, path)`. This keeps the rules exact for any harness with MCP but no adapter. It switches itself off for a session whose harness already gets a native view (detected from the client name plus an env marker the hook sets), so skills never show up twice.

## TUI

Bubble Tea v1 + bubbles + lipgloss, built like tussh's TUI (two panes, single column below 90 columns, header with status chips, help overlay, ANSI palette colors only, sectioned form dialogs). Tabs:

- **Servers:** list with status, tool count, rules summary; details with tools (each switchable), last errors; add/edit form; plugin servers waiting for review.
- **Skills:** tree source → plugin → skill with update status, last fetch, warnings; add source (local / git / marketplace), new local skill, open in `$EDITOR`, update now.
- **Rules editor** (dialog from both tabs): list of rules, add rule with path autocomplete and mode.
- **Preview:** pick a folder (defaults to the TUI's working directory) and see the effective servers, tools and skills with the deciding rule.
- **Setup:** harness registration snippets (reuse tussh's `internal/harness`), shell hook line, daemon install, detected duplicates in harness configs.
- **Doctor:** results of `skilltri doctor`.

## CLI

```
skilltri                      open the TUI
skilltri mcp                  MCP server on stdio (registered in your harness)
skilltri daemon               run the daemon in the foreground
skilltri daemon install       install/remove the launchd agent (systemd user unit on Linux)
skilltri update [source]      fetch sources now
skilltri status [--json]      daemon, servers, sources
skilltri which [dir] [--json] effective servers/tools/skills for a folder and why
skilltri doctor [--json]      broken skills, invalid frontmatter, duplicates, stale views, review-pending servers
skilltri shell-init SHELL     print the shell hook
skilltri env [dir]            print the adapter variables for a folder (used by the hook)
skilltri run HARNESS [args]   start a harness with the folder's view
skilltri import 1mcp [file]   import ~/.config/1mcp/mcp.json (servers, timeouts; secrets go to the keyring)
skilltri import claude        import ~/.claude/skills and installed marketplaces/plugins
skilltri version
```

## Migration from the current setup

1. `skilltri import 1mcp`, check the servers in the TUI, set rules.
2. `skilltri import claude`, set rules; then uninstall the same plugins in Claude Code and remove the imported user skills there (Doctor lists the duplicates).
3. Register `skilltri mcp` in the harnesses instead of the 1MCP endpoint, add the shell hook, install the daemon, stop 1MCP.

## Spike before the plan

Phase 0 of the plan, short and throwaway, with a decision point: the plan names both branches and continues with the one the spike confirms. It settles the assumptions everything else rests on:

1. `CLAUDE_CODE_PLUGIN_DIRS` with a generated plugin folder: are skills found, namespaced as expected, are copied/hardlinked/symlinked skill folders accepted?
2. Working directory and `roots` that Claude Code (and Codex) pass to a stdio MCP server.
3. Whether Codex (and the other `.agents/skills` harnesses) can be pointed at a skill folder through env or config.

If (1) fails, the Claude adapter falls back to `--add-dir <view>` through the launcher (Claude loads `.claude/skills` of added dirs) and the shell hook only covers MCP.

## Repository setup (same as tussh)

- Public repo `baeroe/skilltri` on GitHub, module `github.com/baeroe/skilltri`, Go version from `go.mod` (1.26 like tussh), single binary from `main.go` with `internal/…` packages.
- README in English (structure like tussh's: what it is, why, screenshots, rules, setup per harness), `CONCEPT.md` (this file), MIT license `Copyright (c) 2026 Rafael Haußmann`, `.gitignore` with `bin/` and `.vhs/`.
- `Makefile`: `build`, `test` (vet + `go test -race -count=1 ./...`), `test-unit`, `vet` (gofmt check + `go vet`), `fmt`, `install` (to `~/.local/bin`), `clean`, `screenshots`; version through `-ldflags -X main.version`.
- `.github/workflows/tests.yml` with job id `tests` (gofmt, vet, `go test -race`), `release.yml` on `v*` tags creating a GitHub release with generated notes; Homebrew formula in `baeroe/homebrew-tap`.
- GitHub: issues on, wiki off, topics (e.g. `ai-agents`, `agent-skills`, `mcp`, `mcp-gateway`, `claude-code`, `go`, `bubbletea`, `tui`, `terminal`); rulesets `main` and `release-tags` copied 1:1 from `baeroe/tussh` (`gh api …/rulesets`).
- Screenshots reproducible with VHS tapes under `docs/tapes`, images under `docs/screenshots`, demo data only (no real hosts, servers, company or client names, also not in tests).
- Tests like tussh: unit tests per package, the rules table, an end-to-end test that runs the real binary as an MCP server against fake downstream servers (stdio and HTTP), TUI model tests, adapter tests on temp dirs.
- Before the first push: no secrets, tokens or binaries, no company or client references.
- Later: a `herdr-skilltri` plugin (popup and shortcut, like herdr-tussh / herdr-lazy1mcp).

## Decisions

- **Replace 1MCP, not sit on top of it.** Per-folder filtering needs the session folder, which a shared HTTP endpoint never sees.
- **One daemon, thin stdio shims.** Downstream servers start once and are shared; policy is evaluated in one place.
- **Views outside the project instead of files in the project**, because harnesses also read parent folders. Project-folder symlinks are only the fallback for harnesses without a redirect.
- **Plugin namespace through generated plugins**, not by renaming skills (skill names may not contain `:` and must match their folder).
- **Plugin MCP servers are opt-in and re-reviewed on change.**
- **Excludes win; nearest level with rules decides; no rules = everywhere.**
- **Secrets in the keyring, config in `~/.config`, ANSI palette colors only** (as in tussh).

## Out of scope for now

- Native OAuth for remote MCP servers (use `mcp-remote`)
- Sampling and elicitation passthrough, legacy SSE unless needed
- Plugin hooks, subagents, LSP servers, `bin/`, settings; `npm`/`archive`/`command` plugin sources
- A skill registry search (skills.sh) inside the TUI
- Windows support

## Open questions

- Should a rule be able to match by git remote or repo name instead of a path (so a clone anywhere gets the same set)? (Not in v1; the matcher interface leaves room.)
- Should `local` skills be versioned (a git repo under `~/.config/skilltri/skills`) so edits can be undone?
