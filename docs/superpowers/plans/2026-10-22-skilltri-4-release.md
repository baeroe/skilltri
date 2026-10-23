# skilltri Plan 4: Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** skilltri is ready to publish like tussh: reproducible README screenshots from VHS tapes with demo data, an English README, a clean pre-push audit, and the publishing steps (GitHub repo, rulesets, release tag, Homebrew formula in `baeroe/homebrew-tap`).

**Architecture:** `docs/screenshots.sh` builds skilltri and the fake MCP server into a throwaway sandbox (`HOME`, `SKILLTRI_*` dirs, file-free keyring via env), seeds demo data with `docs/demo/seed.go`, starts a sandbox daemon, and runs every `docs/tapes/*.tape` with VHS. Publishing is done by the controller after the user confirms; the tasks here only prepare it.

**Tech Stack:** VHS (`brew install vhs`, already installed), the existing Go code, `gh`.

**Spec:** `CONCEPT.md` "Repository setup (same as tussh)"; reference implementation tussh's `docs/screenshots.sh`, `docs/tapes/*.tape`, `docs/demo/`, `README.md` (read-only).

## Global Constraints

- Demo data only: no real hosts, servers, company or client names, no real paths of the author's machine in screenshots or docs (use `*.example`, `demo-*`, `acme`-style names and a sandbox `HOME`). Never read or touch the real `~/.claude`, `~/.config/skilltri`, keychain or a running real daemon.
- README in English, structure modelled on tussh's README (what it is, why, screenshots with captions, concepts, install, quick start, reference). License MIT `Copyright (c) 2026 Rafael Haußmann` (already present).
- Screenshots: PNG under `docs/screenshots/`, tapes under `docs/tapes/`, generated only via `make screenshots`; `.vhs/` stays git-ignored.
- Everything from plans 1–3 still applies (new commits only, Co-Authored-By line naming the model, `make test` green).

## Review Focus

1. Screenshots show real TUI output for every tab with plausible demo data (not empty states), at a readable size (120×34 cells or similar), nothing cut off.
2. README commands are correct for the current CLI (every command and flag in the README exists; copy-paste works).
3. No secret, token, personal path, company or client name anywhere in the tree or in commit metadata.

---

### Task 1: Demo sandbox, VHS tapes and screenshots

**Files:**
- Create: `docs/screenshots.sh`, `docs/demo/seed.go` (`//go:build ignore`, run with `go run`), `docs/tapes/config.tape` (shared settings: shell bash, font size, 120×34 window, theme), `docs/tapes/servers.tape`, `skills.tape`, `rules.tape`, `preview.tape`, `setup.tape`, `doctor.tape`, `tour.tape` (gif, tabs walkthrough), `docs/screenshots/*.png` (generated)
- Modify: `Makefile` (`screenshots` target runs `bash docs/screenshots.sh`), `internal/actions/timeout_test.go` (raise the hung-daemon bound from 2.5 s to 4 s; carried minor)

**Interfaces:**
- `seed.go` writes into the sandbox: a config with ~6 demo MCP servers (stdio fakemcp instances named e.g. `docs`, `browser`, `tickets`, `notes`, an http server pointing at a local fake HTTP MCP started by the script or a stdio stand-in, one disabled, one plugin-originated needing review) with folder rules on some; a local skill source with 3 demo skills; a git source (local bare repo created by the script) with 2 skills; a marketplace source (demo marketplace folder like `internal/marketplace/testdata/demo-market`, with several plugins, one disabled, one with a plugin MCP server); rules on a source, a plugin and a skill; demo folders `~/dev/acme-shop`, `~/dev/side-project`; a fake Claude Code registration in the sandbox `~/.claude.json` (skilltri registered, plus one duplicate entry so Setup/Doctor show findings).
- The script starts `skilltri daemon` in the sandbox (background, killed on exit) and opens one `skilltri mcp` session from a demo folder (so status shows a session and tool counts), then runs the tapes. Tapes start `skilltri` (or `skilltri --tab X`) in the sandbox, navigate with keys, and `Screenshot` PNGs named after the tab (`servers.png`, `skills.png`, `rules.png`, `preview.png`, `setup.png`, `doctor.png`) plus `tour.gif`.

- [ ] **Step 1:** Write seed.go + screenshots.sh + tapes (mirror tussh's script: sandbox, cleanup trap, `HOME` switch, real `HOME` only for go caches and VHS's browser).
- [ ] **Step 2:** Run `make screenshots`; inspect every PNG (open them with the Read tool) and iterate on demo data and tape timing until each tab shows meaningful content without cut-offs or error flashes.
- [ ] **Step 3:** Raise the timeout test bound; run `make test`.
- [ ] **Step 4: Commit** `Add demo sandbox, VHS tapes and screenshots`.

### Task 2: README

**Files:**
- Create/replace: `README.md`

Contents (English): one-paragraph intro (one MCP gateway + agent skills, per-folder rules, every harness); screenshot of the Servers tab with caption; **Why**; **How it works** (daemon, `skilltri mcp`, views via `CLAUDE_CODE_PLUGIN_DIRS`, `.agents/skills` links, MCP fallback) with a short diagram in text; **Folder rules** table (four modes, excludes win, nearest level decides, no rules = everywhere; examples); **Skill sources** (local, git, marketplace; updates; pinning; plugins default; plugin MCP servers opt-in with review); **Install** (`brew install baeroe/tap/skilltri`, `go install github.com/baeroe/skilltri@latest`, from source `make install`); **Quick start** (import 1MCP, import Claude Code, register the harness — snippet for Claude Code `claude mcp add --scope user skilltri -- skilltri mcp`, others in the Setup tab —, shell hook line per shell, `skilltri daemon install`, `skilltri doctor`); **TUI** with the other screenshots and their keys; **CLI reference** (every command from `skilltri help`, grouped); **Files** (config/state/data/cache locations and env overrides); **Security notes** (convenience not a boundary; secrets in the keyring; plugin server review; what `doctor` checks); **Development** (`make test`, `make screenshots`); **License**.

- [ ] **Step 1:** Write the README from the current code (run `skilltri help` and each `--help`/usage to verify every command and flag).
- [ ] **Step 2:** Verify: a script extracts every `skilltri …` command line from the README and runs it with `--help`-safe arguments or checks it against `main.go`'s dispatch (document the check in the report); links to screenshots resolve.
- [ ] **Step 3: Commit** `Add the README`.

### Task 3: Pre-push audit

**Files:** whatever the audit needs to fix.

- [ ] **Step 1:** Scan the whole tree and git history: secrets/tokens (regexes for keys, `Bearer `, `ghp_`, `sk-`, private keys), binaries (`git ls-files` with file type check; no files > 1 MB except screenshots/gif), personal/company/client references (the author's employer, clients and private project names, the author's real home path, real hostnames; `tussh` is fine), commit metadata (`git log --format='%an <%ae>|%cn <%ce>|%b'` — only the author's public address and `noreply@anthropic.com`).
- [ ] **Step 2:** Fix findings (new commits), re-run the scan until clean; write the scan commands and results to the report.
- [ ] **Step 3:** Prepare (do not run) the publishing commands in `docs/superpowers/plans/2026-10-22-skilltri-4-release-publish.md`: `gh repo create baeroe/skilltri --public --source . --description "…" --push`, topics, issues on / wiki off, rulesets copied from `baeroe/tussh` via `gh api`, tag `v0.1.0` + push, Homebrew formula `Formula/skilltri.rb` (tussh's formula as template; sha256 of the release tarball) for `baeroe/homebrew-tap`.
- [ ] **Step 4: Commit** `Prepare the first release`.
