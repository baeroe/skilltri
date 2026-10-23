# skilltri — publishing v0.1.0 (prepared, not executed)

Run these by hand, in order, from the repository root, once the release branch is squash-merged into `main`. Every step is a one-off; nothing here is automated.

## 0. Pre-flight

```sh
git switch main
# the release branch is squash-merged (one commit), never fast-forwarded: its early history is not published
git status --short                        # must be empty
# push only main (gh repo create --push does that); never `git push --all` or `--mirror`
make test                                 # must be green
git log --format='%an <%ae>|%cn <%ce>' | sort -u   # only the author's public address
git log --format='%b' | grep -i co-authored | sort -u   # only noreply@anthropic.com
```

## 1. Create the public repository and push `main`

```sh
gh repo create baeroe/skilltri --public --source . --remote origin --push \
  --description "One MCP gateway and agent skill manager for every AI agent harness, with per-folder rules: TUI, stdio MCP server, Claude Code, Codex, Gemini CLI, OpenCode, Cursor and more." \
  --disable-wiki

gh repo edit baeroe/skilltri --enable-issues --enable-wiki=false \
  --add-topic ai-agents --add-topic agent-skills --add-topic mcp --add-topic mcp-gateway \
  --add-topic mcp-server --add-topic claude-code --add-topic go --add-topic bubbletea \
  --add-topic tui --add-topic terminal

gh repo view baeroe/skilltri --json visibility,hasIssuesEnabled,hasWikiEnabled,repositoryTopics,defaultBranchRef
```

Wait for the `tests` workflow on `main` to pass (`gh run watch`), so the required status check `tests` is known to GitHub before the ruleset requires it.

## 2. Rulesets `main` and `release-tags` (copied 1:1 from baeroe/tussh)

```sh
for id in $(gh api repos/baeroe/tussh/rulesets --jq '.[].id'); do
  gh api "repos/baeroe/tussh/rulesets/$id" \
    --jq '{name, target, enforcement, conditions, rules, bypass_actors}' \
  | gh api -X POST repos/baeroe/skilltri/rulesets --input -
done

gh api repos/baeroe/skilltri/rulesets --jq '.[] | "\(.id) \(.name) \(.target) \(.enforcement)"'
```

Expected result (as in tussh): `main` (target branch, `~DEFAULT_BRANCH`: no force push, no deletion, pull request with 1 approval and resolved threads, required status check `tests`, strict) and `release-tags` (target tag, `refs/tags/v*`: no force push, no deletion, no update). Both bypassable by the repository admin role (`actor_id` 5). Compare:

```sh
diff <(gh api repos/baeroe/tussh/rulesets --jq '.[].id' | xargs -I{} gh api repos/baeroe/tussh/rulesets/{} --jq '{name,target,enforcement,conditions,rules,bypass_actors}') \
     <(gh api repos/baeroe/skilltri/rulesets --jq '.[].id' | xargs -I{} gh api repos/baeroe/skilltri/rulesets/{} --jq '{name,target,enforcement,conditions,rules,bypass_actors}')
```

## 3. Tag v0.1.0 (triggers the release workflow)

```sh
git tag -a v0.1.0 -m "skilltri 0.1.0"
git push origin v0.1.0
gh run watch            # Release workflow: gofmt, vet, go test -race, gh release create
gh release view v0.1.0
```

## 4. Homebrew formula in baeroe/homebrew-tap

Compute the tarball checksum once the tag exists on GitHub:

```sh
curl -fsSL https://github.com/baeroe/skilltri/archive/refs/tags/v0.1.0.tar.gz | shasum -a 256
```

New file `Formula/skilltri.rb` in a clone of `baeroe/homebrew-tap` (replace `<SHA256>` with the value above):

```ruby
class Skilltri < Formula
  desc "MCP gateway and agent skill manager with per-folder rules for AI agent harnesses"
  homepage "https://github.com/baeroe/skilltri"
  url "https://github.com/baeroe/skilltri/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "<SHA256>"
  license "MIT"
  head "https://github.com/baeroe/skilltri.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.version=#{version}")
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/skilltri version")
  end
end
```

Add a row to the formula table in the tap's `README.md`:

```markdown
| [`skilltri`](https://github.com/baeroe/skilltri) | MCP gateway and agent skill manager with per-folder rules for AI agent harnesses |
```

Check and publish (in the tap clone). The tap's `tests` workflow builds, tests and audits every formula on macOS on push; from then on its hourly run bumps `url` and `sha256` to each new skilltri release (`scripts/bump.sh skilltri baeroe/skilltri`).

```sh
brew style Formula/skilltri.rb
git add Formula/skilltri.rb README.md
git commit -m "Add skilltri 0.1.0"
git push
gh run watch            # tap workflow "Tests" must pass
```

Then from a clean shell: `brew update && brew install baeroe/tap/skilltri && skilltri version` prints `0.1.0`.
