# CLAUDE.md

Guidance for working in this repository.

## What this is

`herdr-goto` is a small tree-style switcher across herdr repos, worktrees and
panes, used as a replacement for herdr's native "goto" navigator. It runs as a
herdr plugin pane (session-modal popup): open, pick a target, exit. It
talks to herdr through its CLI (`workspace list` / `pane list` to read,
`workspace focus` / `agent focus` to act).

Distributed as a herdr plugin (`herdr plugin install asumaran/herdr-goto`; the
manifest's `[[build]]` runs `scripts/fetch-binary.sh`, which downloads the
release binary matching the manifest version and falls back to `go build`).
Each GitHub Release attaches the `goto-darwin-arm64` asset that
`fetch-binary.sh` depends on. There is no published library.

## Stack & layout

- Go (single module `herdr-goto`, see `go.mod`). Single static binary, no
  runtime deps.
- TUI: Bubble Tea + bubbles (`textinput`, `viewport`, `key`, `help`),
  `lipgloss` for styling, `sahilm/fuzzy` for fuzzy matching/scoring.
- `main.go` — the whole program: herdr CLI JSON shapes, tree building, the
  filter-that-keeps-ancestors, rendering, and `main()`. It's intentionally one
  file; keep it that way unless it clearly outgrows it.
- `herdr-plugin.toml` — the herdr plugin manifest (id `asumaran.goto`): a
  `[[build]]` (runs `scripts/fetch-binary.sh` on install), the `goto` popup
  pane, and the `open` action that opens it (keybind entry point).
- `scripts/` — `release.sh` (see below) plus two plugin pieces:
  `open-pane.sh`, the `open` action's command (plugin commands are argv without
  shell, so the wrapper resolves `HERDR_BIN_PATH` at runtime), and
  `fetch-binary.sh`, the `[[build]]` command (downloads the release binary
  matching the manifest's `version`, falls back to `go build -ldflags
  "-X main.version=v<version>-source"`, aborts the install if neither works;
  `GOTO_BUILD_FROM_SOURCE=1` skips the download and always compiles).
- The compiled binary (`goto`, `goto-darwin-arm64`) is **never committed**
  (`.gitignore`); it is built locally or in CI.

## Build & run

```bash
go build -o goto .     # local build in the repo
./goto -dump           # print the built tree (no TUI) for debugging without a TTY
./goto -version        # print the embedded version
go vet ./... && go test ./...
```

`version` in `main.go` defaults to `"dev"` and is stamped at build time via
`-ldflags "-X main.version=<tag>"` (CI does this from the release tag;
`fetch-binary.sh`'s source fallback stamps `v<version>-source`).

## How it's wired into herdr

The plugin requires herdr >= 0.7.5. The manifest
declares the `goto` popup pane (45% x 50%) and the `open` action; herdr has no
`plugin_pane` keybind type, so the key binds the action, which runs
`scripts/open-pane.sh` -> `herdr plugin pane open`:

```toml
[[keys.command]]
key = ["prefix+f", "ctrl+alt+f"]
type = "plugin_action"
command = "asumaran.goto.open"
```

- Install: `herdr plugin install asumaran/herdr-goto` (clones, runs `[[build]]`
  = `scripts/fetch-binary.sh`: release download first, `go build` fallback, so
  a Go toolchain is only needed off `darwin/arm64`).
- Local dev: `herdr plugin link ~/Developer/herdr-goto` registers the working
  copy. `plugin link` does **not** run build commands — run `go build -o goto .`
  yourself (not `fetch-binary.sh`, which would fetch the released build over
  your local changes); the pane runs `./goto` from the plugin root.
- Runtime state (`state.json`, `prcache.json`) lives in
  `HERDR_PLUGIN_STATE_DIR` (herdr injects it; never store state in the plugin
  checkout). When run standalone (outside herdr, e.g. `./goto -dump`),
  `main.go` falls back to `~/.config/herdr/goto-tui/`.

## Releasing

`scripts/release.sh <X.Y.Z>` cuts and publishes a release. It gates on a clean
tree + green `go vet`/`go build`/`go test`, generates the `CHANGELOG.md` entry
and GitHub release notes from commit subjects since the last tag, syncs
`version` in `herdr-plugin.toml` to the tag, commits (`chore(release):
vX.Y.Z`), tags, pushes, and publishes the GitHub release. CI
(`.github/workflows/release.yml`) then builds the binary (stamping the version
from the tag) and attaches `goto-darwin-arm64` — the asset `fetch-binary.sh`
downloads on plugin installs, so it must keep being published.

Releasing does not touch this machine's linked plugin: local dev runs whatever
`./goto` is built in the working copy (`go build -o goto .`).

The release workflow builds only `darwin/arm64` (the development machine). To
support more platforms, add a build matrix in `release.yml` and upload one
`goto-<os>-<arch>` asset per target; `fetch-binary.sh` already resolves the
asset name from `uname`.

## Behaviour / decisions

- Tree = two levels by default: repo (== main checkout) -> worktrees. Panes are
  hidden by default; `ctrl+t` toggles them, persisted in `state.json`.
- Repos ordered by lowest workspace `number`. Worktrees inside a repo sort
  oldest-first by checkout creation time (directory birth time, which tracks PR
  order in practice), workspace `number` as tiebreaker.
  Grouping key: `worktree.repo_key` (falls back to checkout_path, then a pane's
  cwd, then workspace id).
- Search: fuzzy with scoring + a small kind bonus (repo +8, worktree +4) so
  repo/worktree names outrank panes. Besides the label, the branch, the Jira
  ticket and the PR number are matched (typing "1234" finds the row showing
  #1234). Matches keep ancestors visible. Digits are plain search text; the
  old "1-9 jumps to a numbered repo" mode was removed on purpose — do not
  reintroduce it.
- Enter on repo/worktree -> `workspace focus` (does not change the focused pane
  inside it). Enter on pane -> focus that pane. No autofocus on switch.
- Rows are prefixed with the Jira ticket (`KEY-123` regex over branch, then
  label, then PR title as fallback) and the branch's PR number colored by
  state (open green, draft dim, merged purple, closed red). Columns align per
  sibling group; rows with neither ticket nor PR get no prefix. PR data comes
  from one async `gh pr list` per unique GitHub repo, fired after the TUI is
  on screen, and cached in `prcache.json` next to `state.json`
  (stale-while-revalidate; entries fresher than 60s skip the refresh).
  Missing `gh` or non-GitHub remotes degrade silently to no PR info.

## Commits & branches

- Conventional Commits: `type(scope): description` (feat, fix, chore, docs,
  style, refactor, test, perf).
- Never mention AI tooling in commits, PRs, or any repo-visible text.
- Default branch is `main`. Don't commit, tag, or push unless explicitly asked
  (releasing is an explicit, separate request).
