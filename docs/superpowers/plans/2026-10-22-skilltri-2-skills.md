# skilltri Plan 2: Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Skills from local folders, git repos and Claude plugin marketplaces, kept up to date by the daemon and delivered per session folder: natively to Claude Code (generated plugin dirs via `CLAUDE_CODE_PLUGIN_DIRS`), to `.agents/skills` harnesses (opt-in project-folder links), and through `skilltri mcp` as a fallback. Plus opt-in plugin MCP servers, `import claude` and `doctor`.

**Architecture:** Sources are synced into `DataDir/sources/<name>` (git CLI). A catalog (all skills with their source → plugin → skill hierarchy, plus plugin MCP server definitions) is built from config + checkouts and cached as `CacheDir/catalog.json`. Rules from plan 1 decide the active set per folder. `skilltri env` (called by a shell hook on `cd`) reads config + cached catalog, builds a view and prints exports. The daemon schedules updates and serves the MCP fallback.

**Tech Stack:** Go 1.26, existing packages from plan 1, `gopkg.in/yaml.v3` (SKILL.md frontmatter), the `git` CLI (required at runtime; not a Go dependency).

**Spec:** `CONCEPT.md` sections "Skills", "Delivery to harnesses", "CLI", "Migration", "Spike results".

## Global Constraints

- Everything in plan 1's Global Constraints still applies (English, package doc comments, file locations, secrets only in the keyring, 0600 for config/lock files, demo data only in tests, `make test` green, one commit per task with a Co-Authored-By line naming the Claude model that wrote it).
- New dependency allowed: `gopkg.in/yaml.v3` only. Git operations shell out to `git` (`exec.CommandContext`), never a Go git library.
- Skill spec (agentskills.io): `name` 1–64 chars matching `^[a-z0-9]+(-[a-z0-9]+)*$` and equal to its folder name; `description` 1–1024 chars. Violations are **warnings**, never fatal; a skill without a parseable `name` uses its folder name.
- Skill IDs: `<name>` for `local`/`git` sources, `<plugin>:<name>` for marketplace plugins.
- Claude view: one generated plugin per source plugin (`<plugin>/.claude-plugin/plugin.json` = `{"name":"<plugin>"}` + `skills/<name>` symlink), local/git skills in a plugin named `skilltri`. Symlinks, not copies (spike result 1).
- Project-folder adapters are **opt-in**: `settings.adapters` lists enabled adapter ids; default `["claude-code"]`. They never delete or modify entries in `.agents/skills` that skilltri did not create.
- Tests never touch the real `~/.claude`, `~/.config`, keychain, network or a running real daemon: fake `HOME`, temp dirs, local git repos created in the test (`git init`, `file://` URLs).
- Folder separation is convenience, not a security boundary — but `read_skill_file` must not serve files outside the skill folder.

## Review Focus

1. **Skill ID collisions** (two sources with the same skill name, two plugins with the same name): deterministic winner (sources sorted by name), warning in the catalog, no crash; project-folder link names stay unique. → tests in Tasks 5 and 6.
2. **Update failures** (network down, auth failure, deleted ref, force-pushed branch): the last good checkout stays in use, the error is recorded per source and shown by `doctor`/`update`, no skills disappear. → tests in Tasks 3 and 8.
3. **Project-folder sync safety:** existing user entries in `.agents/skills` are never touched; a read-only folder or a folder outside any git repo works (skip exclude) without failing the hook. → tests in Task 6.
4. **Shell hook robustness:** no daemon, no catalog yet, broken config, folder deleted while the shell is in it → `skilltri env` exits 0, prints only valid shell, finishes fast (no fetch, no network). → tests in Task 7.
5. **Path traversal:** `read_skill_file` with `../`, absolute paths or symlinks leaving the skill folder is refused. → test in Task 9.

---

### Task 1: Config model for sources, nodes and targets

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `internal/target/target.go`, `internal/target/target_test.go`

**Interfaces:**
- Produces in `config`:
  ```go
  // added to Config:
  Sources map[string]*Source `json:"sources,omitempty"`
  // added to Settings:
  Adapters []string `json:"adapters,omitempty"` // enabled adapter ids; nil → ["claude-code"]
  type Source struct {
      Type        string           `json:"type"`                  // "local" | "git" | "marketplace"
      Path        string           `json:"path,omitempty"`        // local folder (local; marketplace from a folder)
      URL         string           `json:"url,omitempty"`         // "owner/repo" or a git URL (git, marketplace)
      Ref         string           `json:"ref,omitempty"`         // branch, tag or 40-hex commit (pinned)
      Subpath     string           `json:"subpath,omitempty"`
      UpdateEvery string           `json:"updateEvery,omitempty"` // Go duration; "" → 1h; "0" → manual only
      Disabled    bool             `json:"disabled,omitempty"`
      Rules       []rules.Rule     `json:"rules,omitempty"`
      Plugins     map[string]*Node `json:"plugins,omitempty"`     // marketplace: per plugin (absent = enabled)
      Skills      map[string]*Node `json:"skills,omitempty"`      // per skill id
  }
  type Node struct { Disabled bool `json:"disabled,omitempty"`; Rules []rules.Rule `json:"rules,omitempty"` }
  // added to Server:
  Origin *Origin `json:"origin,omitempty"`
  type Origin struct { Source string `json:"source"`; Plugin string `json:"plugin"`; Server string `json:"server"`; DefHash string `json:"defHash"` }
  func (s *Source) Interval() (time.Duration, error) // "" → time.Hour, "0" → 0
  func (c *Config) EnabledAdapters() []string
  const ReservedName = "skilltri" // not allowed as server name (tool prefix of the skill fallback)
  ```
  `Validate` additionally checks: source names with `ValidServerName`; type known; `local` needs `Path`; `git` needs `URL`; `marketplace` needs `URL` or `Path`; `UpdateEvery` parses; all node rules valid; server name `skilltri` rejected.
- Produces in `target`:
  ```go
  type Target struct { Kind string; Server, Tool, Source, Plugin, Skill string } // Kind: "server" | "tool" | "source" | "plugin" | "skill"
  func Parse(s string) (Target, error) // "server/<n>", "server/<n>/tool/<t>", "source/<n>", "source/<n>/plugin/<p>", "source/<n>/skill/<id>"
  func (t Target) String() string
  type Ref struct { Disabled *bool; Rules *[]rules.Rule }
  func Resolve(cfg *config.Config, t Target, create bool) (Ref, error) // create: make missing Tool/Node entries; missing server/source → error
  ```

- [ ] **Step 1: Failing tests** — `TestSourceValidate` (rows: local without path; git without url; type `"zip"`; `updateEvery: "soon"`; name `skilltri` as server; valid marketplace with only `path` passes), `TestSourceInterval` (`""`→1h, `"0"`→0, `"30m"`→30m), `TestSourcesRoundTrip` (Update keeps sources, nodes, origin), `TestEnabledAdaptersDefault`; `TestParse` (each form; `server/a/tool` → error; skill id with colon `source/m/skill/sp:brainstorming` ok), `TestResolveCreate` (rule added through `Ref` lands in `cfg.Sources["m"].Plugins["p"].Rules`; unknown source → error).
- [ ] **Step 2: Run** `go test ./internal/config/ ./internal/target/` — FAIL. **Step 3: Implement.** **Step 4: Run** — PASS. **Step 5: Commit** `Add sources, nodes and targets to the config`.

### Task 2: Skill parsing and discovery

**Files:**
- Create: `internal/skills/skills.go`, `internal/skills/skills_test.go`

**Interfaces:**
- Produces:
  ```go
  type Skill struct { Name, Description, Dir string; Warnings []string }
  func Parse(dir string) (Skill, error)           // dir/SKILL.md; error only when SKILL.md is missing or unreadable
  func Discover(root string) ([]Skill, []string)  // every folder below root with SKILL.md; does not descend into a skill folder; skips .git and node_modules; result sorted by Dir; second value = warnings ("<path>: <problem>")
  ```

- [ ] **Step 1: Failing tests** — `TestParse` rows: valid frontmatter; folded description (`description: >` multi-line) joined; quoted values; no frontmatter → name = folder, warning `missing frontmatter`; name `Bad_Name` → warning; name differs from folder → warning; description of 1025 chars → warning; empty description → warning. `TestDiscover`: tree `a/SKILL.md`, `a/references/x/SKILL.md` (not separate: inside a skill), `group/b/SKILL.md`, `.git/c/SKILL.md` (skipped) → skills `a`, `b`.
- [ ] **Step 2–4:** RED, implement with `yaml.v3` on the text between the first two `---` lines, GREEN. **Step 5: Commit** `Add skill parsing and discovery`.

### Task 3: Git sources and the skill lock file

**Files:**
- Create: `internal/sources/git.go`, `internal/sources/git_test.go`, `internal/skilllock/lock.go`, `internal/skilllock/lock_test.go`

**Interfaces:**
- Produces in `sources`:
  ```go
  func RepoURL(s string) string        // "owner/repo" → "https://github.com/owner/repo.git"; anything else unchanged
  func Pinned(ref string) bool         // 40 hex characters
  type Fetcher struct { Root string; Git string } // Root = paths.DataDir()/sources; Git "" → "git"
  type Checkout struct { Dir string; Commit string }
  func (f *Fetcher) Dir(key string) string // Root/<key>/repo; key may contain "/" (plugin sources: "<source>/plugins/<plugin>")
  func (f *Fetcher) Sync(ctx context.Context, key, url, ref string) (Checkout, error)
  func (f *Fetcher) Current(key string) (Checkout, bool) // existing checkout without network
  ```
  `Sync`: missing → `git clone --filter=blob:none --no-checkout <url> <tmp>` then rename into place; existing → `git fetch --prune --tags origin` (skipped when `Pinned(ref)` and the commit exists); then `git checkout --detach --force <target>` where target is `ref` resolved as `origin/<ref>` for branches, the tag, the SHA, or `origin/HEAD` when `ref` is empty. Every git call gets `GIT_TERMINAL_PROMPT=0` and a 120 s timeout. On any error the previous checkout stays untouched (fetch failure leaves HEAD where it was) and the error is returned.
- Produces in `skilllock`:
  ```go
  type Lock struct { Version int `json:"version"`; Sources map[string]Source `json:"sources"` }
  type Source struct { Commit string `json:"commit,omitempty"`; FetchedAt time.Time `json:"fetchedAt"`; Error string `json:"error,omitempty"`; Skills map[string]string `json:"skills"` } // skill id → HashDir
  func Path() string // paths.StateDir()/skills.lock
  func Load(path string) (*Lock, error) // missing → empty
  func Save(path string, l *Lock) error // atomic, 0600
  func HashDir(dir string) (string, error) // sha256 over sorted relative paths, file modes and contents; symlinks hashed by their target string
  ```

- [ ] **Step 1: Failing tests** with a helper creating a local repo in `t.TempDir()` (`git init`, commits, branch `main`, tag `v1`): `TestSyncCloneAndUpdate` (clone gives commit A; new commit B on main; Sync → B), `TestSyncRefs` (tag `v1`, branch, pinned SHA; pinned does not fetch when present), `TestSyncFailureKeepsCheckout` (after a good sync, point the remote URL at a missing path → error returned, `Current` still reports the old commit and files exist; Review Focus 2), `TestSyncForcePush` (branch reset to an unrelated commit → checkout follows, no error), `TestRepoURL`, `TestPinned`; `TestHashDir` (stable across runs; changes when a file or a mode changes), `TestLockRoundTrip`.
- [ ] **Step 2–4:** RED, implement, GREEN (skip git tests with `t.Skip` when `git` is not on PATH). **Step 5: Commit** `Add git sources and the skill lock file`.

### Task 4: Marketplaces and plugins

**Files:**
- Create: `internal/marketplace/marketplace.go`, `internal/marketplace/plugin.go`, `internal/marketplace/marketplace_test.go`, `internal/marketplace/testdata/…` (demo marketplace)

**Interfaces:**
- Consumes: `config.Server`.
- Produces:
  ```go
  type Entry struct { Name string; Source json.RawMessage; Skills []string; MCPServers json.RawMessage }
  type Marketplace struct { Name string; PluginRoot string; Plugins []Entry }
  func Load(root string) (*Marketplace, error) // root/.claude-plugin/marketplace.json
  type Remote struct { URL, Ref, Path string } // git URL (shorthand allowed), ref or sha, subdir ("" = repo root)
  func ParseSource(raw json.RawMessage) (local string, remote *Remote, err error) // "./x" → local; {"source":"github",repo,ref,sha} / {"source":"url",url,ref,sha} / {"source":"git-subdir",url|repo,path,ref,sha} → remote; npm/archive/command → error "unsupported plugin source <kind>"
  type Plugin struct {
      Entry      string                   // name in marketplace.json
      Name       string                   // plugin.json name, else Entry
      Dir        string
      SkillRoots []string                 // Dir/skills plus extra `skills` paths from plugin.json and the entry
      Servers    map[string]config.Server // from Dir/.mcp.json and plugin.json mcpServers; ${CLAUDE_PLUGIN_ROOT} replaced with Dir
  }
  func LoadPlugin(entry Entry, dir string) (Plugin, []string, error)
  ```
  `.mcp.json` accepts `{"mcpServers": {...}}` and a flat map; server JSON in Claude format (`command`, `args`, `env`, `type`, `url`, `headers`) maps to `config.Server` (`type` `"http"`/`"streamable-http"` → `"http"`, no type + command → `"stdio"`, `"sse"` → skipped with warning). `plugin.json` `mcpServers` may be an object or a relative path to a JSON file.

- [ ] **Step 1: Failing tests** with a demo marketplace in testdata (`demo-market`: plugin `writing` relative `./plugins/writing` with `skills/outline/SKILL.md` and `.mcp.json` server `notes` using `${CLAUDE_PLUGIN_ROOT}/bin/notes`; plugin `remote-one` with `{"source":"github","repo":"demo/remote-one"}`; plugin `pkg` with `{"source":"npm",...}`): `TestLoad`, `TestParseSource` (all kinds incl. git-subdir with `repo` shorthand), `TestLoadPlugin` (Name from plugin.json; SkillRoots; server `notes` command is `<Dir>/bin/notes`; sse server skipped with warning), `TestPluginRootPrefix` (`metadata.pluginRoot: "./plugins"` + source `"writing"`).
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Read Claude plugin marketplaces`.

### Task 5: Catalog and active set

**Files:**
- Create: `internal/catalog/catalog.go`, `internal/catalog/active.go`, `internal/catalog/catalog_test.go`

**Interfaces:**
- Consumes: `config.Source/Node/Origin`, `skills.Discover`, `sources.Fetcher` (`Sync`, `Current`, `Dir`), `marketplace.*`, `rules.Level/Evaluate`, `skilllock`.
- Produces:
  ```go
  type Entry struct { ID, Source, Plugin string; Skill skills.Skill }        // Plugin "" for local/git
  type PluginServer struct { Source, Plugin, Name string; Def config.Server; DefHash string }
  type SourceState struct { Name, Commit, Error string; FetchedAt time.Time }
  type Catalog struct { Skills []Entry; Servers []PluginServer; Sources []SourceState; Warnings []string; Built time.Time }
  type Builder struct { Fetcher *sources.Fetcher; Lock string /* skilllock path */ }
  func (b *Builder) Build(ctx context.Context, cfg *config.Config, sync bool) (*Catalog, error) // sync=false: only existing checkouts (no network); sync=true: Sync git/marketplace sources (and remote plugins) that are due or listed in only
  func (b *Builder) SyncSources(ctx context.Context, cfg *config.Config, only []string) []SourceState
  func Path() string                                   // paths.CacheDir()/catalog.json
  func Save(path string, c *Catalog) error; func Load(path string) (*Catalog, error)
  func Levels(cfg *config.Config, e Entry) []rules.Level // [source, plugin (if any), skill]
  type Decided struct { Entry Entry; Decision rules.Decision }
  func Evaluate(c *Catalog, cfg *config.Config, dir string) []Decided // all entries, sorted by ID
  func Active(c *Catalog, cfg *config.Config, dir string) []Entry
  func DefHash(s config.Server) string                  // = s.ConnKey()
  func ReviewState(cfg *config.Config, c *Catalog) map[string]string // config server name (with Origin) → "ok" | "needs-review" | "gone"
  ```
  Sources are processed in name order; a skill ID already taken keeps the first and adds the warning `skill <id> from <source> hidden by <first source>`. Disabled sources are skipped entirely. Plugin skills of disabled plugins are left out of `Skills`, but their servers still appear in `Servers` (so a server can be reviewed). Remote plugins are synced with key `<source>/plugins/<entry>` and their `Path` subdir. Build writes the lock (commit, error, skill hashes) for synced sources.

- [ ] **Step 1: Failing tests** using a local git repo source, a local folder source and the testdata marketplace from Task 4 (as a `marketplace` source with `Path`): `TestBuildLocalGitMarketplace` (IDs `alpha`, `beta`, `writing:outline`; server `writing/notes` listed), `TestCollision` (two local sources both with `alpha` → first by name wins, one warning; Review Focus 1), `TestEvaluateHierarchy` (marketplace rule `tree P`, skill rule `only Q` → skill active only in Q; plugin disabled → skill gone), `TestBuildOfflineUsesCheckout` (sync=false never runs git — fetcher with `Git` set to a script that fails), `TestReviewState` (enabled server with Origin and matching DefHash → ok; changed `.mcp.json` → needs-review; plugin removed → gone), `TestSaveLoad`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the skill catalog`.

### Task 6: Views and adapters

**Files:**
- Create: `internal/views/views.go`, `internal/views/views_test.go`, `internal/adapters/adapters.go`, `internal/adapters/projectdir.go`, `internal/adapters/adapters_test.go`

**Interfaces:**
- Consumes: `catalog.Entry`, `paths.CacheDir`.
- Produces in `views`:
  ```go
  func Claude(cacheDir string, active []catalog.Entry) ([]string, error) // plugin dirs, sorted; dir = cacheDir/views/<sha256 of sorted "id\tdir" lines, 16 hex>/claude/<plugin>; idempotent; touches <view>/.used
  func GC(cacheDir string, maxAge time.Duration) error // removes views whose .used is older than maxAge
  ```
- Produces in `adapters`:
  ```go
  type Adapter struct {
      ID, Display  string
      Method       string   // "claude-plugin-dirs" | "project-dir"
      ProjectDir   string   // ".agents/skills" for project-dir
      ClientNames  []string // MCP clientInfo names of this harness
      DetectPaths  []string // relative to HOME; any existing → detected
  }
  var Registry []Adapter // claude-code (claude-plugin-dirs, ".claude", client "claude-code"), codex (".codex"), gemini-cli (".gemini"), opencode (".config/opencode"), cursor (".cursor"), github-copilot (".copilot"), amp (".config/amp") — all except claude-code use project-dir ".agents/skills"
  func ByID(id string) (Adapter, bool)
  func Detected(home string) []Adapter
  func ForClient(clientName string) (Adapter, bool)
  type SyncResult struct { Linked, Removed []string; Skipped []string /* user entries we left alone */ }
  func SyncProjectDir(sessionDir, rel string, active []catalog.Entry) (SyncResult, error)
  ```
  `SyncProjectDir`: target `sessionDir/rel`; link name = skill name, `<plugin>-<name>` when two active skills share a name; owned links are listed in `rel/.skilltri.json` (`{"links":[...]}`); removes owned links that are no longer active; never touches other entries (a user folder with the same name → skip + listed in `Skipped`); creates `rel` only when there is at least one link; when inside a git work tree (walk up for `.git`), appends `rel/.skilltri.json` and every link path (relative to the repo root) to `.git/info/exclude` once. A non-writable folder returns an error the caller logs; it never panics.

- [ ] **Step 1: Failing tests:** `TestClaudeView` (entries from two plugins + one local → plugin dirs `skilltri`, `writing`; `writing/.claude-plugin/plugin.json` is `{"name":"writing"}`; `writing/skills/outline` is a symlink to the skill dir; second call returns the same dirs without rewriting), `TestGC`; `TestRegistry` (ids unique; claude-code method), `TestDetected` (fake HOME with `.codex` → codex), `TestSyncProjectDir` (links created, manifest written, exclude lines added once; second sync with fewer skills removes only owned links; a pre-existing user folder `alpha` is untouched and reported in `Skipped`; Review Focus 3), `TestSyncNoGitRepo`, `TestSyncReadOnly` (chmod 0555 → error, no panic), `TestSyncNameCollision` (`writing:outline` and local `outline` → `outline` and `writing-outline`).
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add Claude views and harness adapters`.

### Task 7: `skilltri env`, `shell-init` and `run`

**Files:**
- Create: `internal/hook/env.go`, `internal/hook/shell.go`, `internal/hook/hook_test.go`
- Modify: `main.go` (`env [dir] [--shell zsh|bash|fish]`, `shell-init SHELL`, `run HARNESS [args…]`)

**Interfaces:**
- Consumes: `config.Load`, `catalog.Load/Active/Builder.Build(sync=false)`, `views.Claude`, `adapters.*`, `rules.Normalize`.
- Produces:
  ```go
  type Env struct { Set map[string]string; Unset []string; Notes []string } // Notes: problems for stderr (never on stdout)
  func Compute(dir string, environ []string) Env
  func Render(e Env, shell string) string    // zsh/bash: export K='v' / unset K; fish: set -gx K 'v' / set -e K; single quotes escaped
  func Init(shell string) (string, error)    // hook script; unknown shell → error
  ```
  `Compute`: config load error, missing catalog, missing folder → `Env` with only the marker variables (no error). Without a cached catalog it builds one with `sync=false` and saves it. Variables: `CLAUDE_CODE_PLUGIN_DIRS` = user base (value of `SKILLTRI_BASE_CLAUDE_CODE_PLUGIN_DIRS`, which `Compute` sets from the current `CLAUDE_CODE_PLUGIN_DIRS` on first use) joined with the view dirs (`:`); unset when both are empty. `SKILLTRI_SKILLS_NATIVE` = comma list of enabled adapter ids that delivered natively for this folder. For enabled project-dir adapters `Compute` runs `SyncProjectDir` once per distinct `ProjectDir` (failures → Notes). Shell init scripts: zsh `chpwd_functions` + immediate call; bash `PROMPT_COMMAND` with a `$PWD` change check; fish `--on-variable PWD`; each exports `SKILLTRI_HOOK=<shell>` and calls `command skilltri env --shell <shell> 2>/dev/null`. `run`: harness id → `Compute(cwd)` → `syscall.Exec` the harness binary (`claude` for claude-code, else the id) with the merged env.

- [ ] **Step 1: Failing tests** (fake HOME/config/cache dirs, a local source with two skills, rule `only P` on one): `TestComputeClaude` (dir P → `CLAUDE_CODE_PLUGIN_DIRS` has one view dir containing both skills; dir Q → only one skill), `TestComputeKeepsUserBase` (pre-existing `CLAUDE_CODE_PLUGIN_DIRS=/mine` stays first), `TestComputeNoSkills` (→ unset), `TestComputeRobust` (broken config, missing catalog, deleted dir → valid output, exit path without error; Review Focus 4), `TestComputeProjectDirOptIn` (codex not enabled → no `.agents/skills`; enabled → links), `TestRender` (quote escaping `it's`), `TestInit` (each shell contains its hook mechanism; unknown → error); a benchmark-style test asserting `Compute` with a cached catalog of 200 skills takes < 50 ms.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add the shell hook, env and run`.

### Task 8: Updates in the daemon and `skilltri update`

**Files:**
- Create: `internal/daemon/updater.go`, `internal/daemon/updater_test.go`, `internal/cli/update.go`
- Modify: `internal/daemon/daemon.go`, `internal/daemon/control.go`, `main.go`

**Interfaces:**
- Consumes: `catalog.Builder`, `catalog.Save`, `skilllock`, `views.GC`.
- Produces: daemon `Options.Builder *catalog.Builder` (nil → default from paths), `Options.UpdateTick time.Duration` (0 → 1 min); control command `update` with `Args` = source names (empty = all) replying `UpdateReply{Sources []catalog.SourceState}`; `(*Daemon).Catalog() *catalog.Catalog` for sessions (Task 9); `cli.Update(w io.Writer, names []string, jsonOut bool) int` (via daemon if running, else inline with a local Builder).
  Updater: on start and every tick, sync sources whose `FetchedAt + Interval` is due (interval 0 = manual), rebuild + save the catalog, `views.GC(cacheDir, 7*24h)` once a day, refresh all sessions when the catalog changed. A config change also rebuilds the catalog (sync=false). Update errors are kept per source (SourceState.Error) and never remove skills from the catalog (Review Focus 2).

- [ ] **Step 1: Failing tests:** `TestUpdaterSyncsDueSources` (local git source, `UpdateTick` 50 ms, new commit appears in the saved catalog), `TestUpdaterManualSource` (`updateEvery: "0"` never synced by tick, synced by control `update`), `TestUpdateFailureKeepsSkills` (remote removed → SourceState.Error set, skills still in catalog), `TestControlUpdate` (reply lists the source with its commit), `TestCLIUpdateOffline` (no daemon → inline update works).
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Update skill sources in the daemon`.

### Task 9: MCP skill fallback

**Files:**
- Create: `internal/gateway/skills.go`, `internal/gateway/skills_test.go`
- Modify: `internal/gateway/session.go`, `internal/daemon/hello.go`, `internal/daemon/daemon.go`, `internal/shim/shim.go`, `main.go` (`mcp` puts `SkillsNative` into the hello)

**Interfaces:**
- Consumes: `catalog.Active`, `adapters.ForClient`.
- Produces: `Hello.SkillsNative []string` (from env `SKILLTRI_SKILLS_NATIVE`, comma-separated); `SessionInfo.SkillsNative []string`; `gateway.NewSession(info, cfg, mgr, skills func(dir string) []catalog.Entry)` (nil → no skills; update the existing callers); tools `skilltri_load_skill` (`{"name": string}` → SKILL.md text plus a list of the skill's other files, relative) and `skilltri_read_skill_file` (`{"name": string, "path": string}` → file text, max 256 kB, refused outside the skill folder).
  The fallback is on for a session unless its client (initialize clientInfo name) maps through `adapters.ForClient` to an adapter id listed in `SkillsNative`. Until the client name is known (pre-Connect refresh) the skill tools are added only when `SkillsNative` is empty; after `initialized` the daemon refreshes the session so the decision is final. When `SkillsNative` is empty the instructions append `"Agent skills (load with skilltri_load_skill): "` + one line per active skill `"- <id>: <description>"`, capped at 8 kB (then `"… and N more; call skilltri_load_skill with a name"`).

- [ ] **Step 1: Failing tests:** `TestSkillToolsListed` (no native → `skilltri_load_skill` and `skilltri_read_skill_file` present; instructions list `alpha`), `TestSkillToolsHiddenForNativeClient` (SkillsNative `["claude-code"]`, client `claude-code` → tools absent after initialize), `TestLoadSkill` (returns SKILL.md text and `references/a.md`), `TestReadSkillFileTraversal` (`../x`, `/etc/hosts`, symlink to outside → error results; Review Focus 5), `TestSkillsFollowDir` (SetDir to a folder where the skill is inactive → load refuses it), e2e addition in `e2e_test.go`: `skilltri mcp` with `SKILLTRI_SKILLS_NATIVE` unset lists `skilltri_load_skill`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Serve skills over MCP as a fallback`.

### Task 10: CLI for sources, rules, skills and plugin servers

**Files:**
- Create: `internal/cli/sources.go`, `internal/cli/rules.go`, `internal/cli/pluginservers.go`, `internal/cli/cli_sources_test.go`
- Modify: `internal/cli/which.go`, `internal/daemon/control.go` (`WhichReply.Skills`), `main.go`

**Interfaces:**
- Consumes: `config.Update`, `target.Parse/Resolve`, `catalog.*`, `marketplace` (plugin rules), `paths`.
- Produces (each returns an exit code and writes human output, `--json` where listed):
  - `skilltri source add local NAME PATH` / `add git NAME URL [--ref R] [--subpath P] [--every D]` / `add marketplace NAME URL|PATH [--ref R] [--every D]`, `source remove NAME`, `source list [--json]` (type, url/path, ref, last commit, fetched at, error, skill count).
  - `skilltri skill new NAME` → creates `ConfigDir/skills/NAME/SKILL.md` with frontmatter (`name`, `description: TODO`) and ensures a `local` source named `local` with `Path = ConfigDir/skills`; prints the path.
  - `skilltri rule add TARGET MODE PATH`, `rule remove TARGET INDEX|all`, `rule list [TARGET] [--json]`, `skilltri enable TARGET`, `skilltri disable TARGET`.
  - `skilltri plugin-server list [--json]` (source/plugin/name, state: available | enabled as <name> | needs-review | gone), `plugin-server enable SOURCE/PLUGIN/SERVER [--name N]` (default name `<plugin>-<server>`, invalid characters → `-`; copies the plugin's effective rules: rules of the nearest level among [plugin node, source] that has any; sets Origin with DefHash), `plugin-server accept NAME` (replace connection fields with the current definition, update DefHash).
  - `skilltri adapter list [--json]` (id, display, method, detected, enabled), `adapter enable ID`, `adapter disable ID` (edits `settings.adapters`; unknown id → exit 2).
  - `which` lists skills too: `WhichReply.Skills []WhichSkill{ID, Source, Plugin string; Decision rules.Decision}`, human output section `skills:`.

- [ ] **Step 1: Failing tests** against temp config dirs: `TestSourceAddListRemove`, `TestSkillNew` (file content, local source created once), `TestRuleAddRemove` (rule on `source/m/plugin/writing` persisted; remove by index), `TestEnableDisable`, `TestAdapterEnableDisable`, `TestPluginServerEnableAccept` (testdata marketplace: enable → server `writing-notes` with Origin and copied rules; change `.mcp.json` → list shows needs-review, server definition unchanged; accept → definition and DefHash updated), `TestWhichSkills`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add CLI for sources, rules, skills and plugin servers`.

### Task 11: `skilltri import claude`

**Files:**
- Create: `internal/importer/claude.go`, `internal/importer/claude_test.go`, `internal/importer/testdata/claude-home/…`
- Modify: `main.go` (`import claude [--home DIR]`)

**Interfaces:**
- Consumes: `config.Update`, `marketplace.Load`, `paths.ConfigDir`.
- Produces: `importer.FromClaude(home string) (ClaudeResult, error)` with `type ClaudeResult struct { Sources map[string]*config.Source; UserSkills []string /* dirs to copy */; Steps []string /* manual follow-ups to print */; Warnings []string }` and `importer.ApplyClaude(cfgPath, skillsDir string, r ClaudeResult, force bool, out io.Writer) error`.
  Reads `home/.claude/plugins/known_marketplaces.json` (`{name: {source: {source: "github", repo} | {source: "git", url} | {source: "directory", path}}, installLocation}`), `home/.claude/plugins/installed_plugins.json` (`plugins` keys `<plugin>@<marketplace>`) and `home/.claude/settings.json` `enabledPlugins` (`"<plugin>@<marketplace>": bool`). Each marketplace becomes a `marketplace` source with the same name (invalid characters → `-`); plugins listed in the marketplace (read from `installLocation`) that are not installed **and** enabled get `Plugins[p] = {Disabled: true}`. `home/.claude/skills/*` except `synced` are copied into `skillsDir` (skip existing, warning) and a `local` source `local` is ensured. Steps list `claude plugin uninstall <plugin>@<marketplace>` per imported plugin and `remove ~/.claude/skills/<name>` per copied skill — printed, never executed. Plugin MCP servers are not enabled (mention `skilltri plugin-server list` in Steps).

- [ ] **Step 1: Failing tests** with the testdata fake home (two marketplaces: github + directory; one plugin installed+enabled, one installed but disabled, one not installed; user skills `demo-skill` and `synced/`): sources and Disabled flags as specified; `demo-skill` copied, `synced` ignored; Steps contain the uninstall lines; existing source name skipped without `--force`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Import marketplaces and skills from Claude Code`.

### Task 12: `skilltri doctor`

**Files:**
- Create: `internal/doctor/doctor.go`, `internal/doctor/checks.go`, `internal/doctor/doctor_test.go`, `internal/cli/doctor.go`
- Modify: `main.go` (`doctor [--json]`)

**Interfaces:**
- Consumes: everything above (read-only).
- Produces:
  ```go
  type Finding struct { Severity string /* "error" | "warning" | "info" */; Check, Subject, Message, Fix string }
  type Env struct { Home, ConfigPath, Socket, CacheDir, StatePath string; Environ []string } // injectable for tests
  func Run(ctx context.Context, e Env) []Finding // sorted: errors, warnings, infos; then by Check, Subject
  func cli.Doctor(w io.Writer, jsonOut bool) int  // exit 1 when any error
  ```
  Checks (one function each, all read-only): `config` (load/validate error → error); `daemon` (not running → warning; version differs from this binary → warning); `sources` (SourceState.Error → error; never fetched → warning); `skills` (catalog warnings → warning); `lock` (skill hash differs from the lock for git/marketplace sources → warning "modified checkout"); `views` (broken symlinks in views → error); `project-dirs` (owned links in `.agents/skills` of the current folder that are broken → warning); `duplicates` (an active skill id that Claude Code also loads itself: same name under `home/.claude/skills` or a plugin enabled in `home/.claude/settings.json` that is also a skilltri marketplace plugin → warning with the uninstall hint); `plugin-servers` (needs-review / gone → warning); `harness` (`home/.claude.json` `mcpServers` without an entry running `skilltri mcp` → info; an entry pointing at the 1MCP endpoint `http://127.0.0.1:3050/mcp` or a `1mcp` command → warning "1MCP still registered"); `shell-hook` (`SKILLTRI_HOOK` unset → info with the `shell-init` line); `adapters` (detected harness whose adapter is not enabled → info).

- [ ] **Step 1: Failing tests** with a fake home and a crafted state for each check (one test per check asserting severity and subject), plus `TestRunSorted` and `TestDoctorExitCode`.
- [ ] **Step 2–4:** RED, implement, GREEN. **Step 5: Commit** `Add skilltri doctor`.
