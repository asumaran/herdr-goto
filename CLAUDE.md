# CLAUDE.md

Guidance for working in this repository.

## What this is

`herdr-goto` is a small tree-style switcher across herdr repos, worktrees and
panes, used as a replacement for herdr's native "goto" navigator. It runs as an
external binary inside a herdr pane (a `type = "pane"` keybind): open full
screen, pick a target, exit. It talks to herdr through its CLI (`workspace
list` / `pane list` to read, `workspace focus` / `agent focus` to act).

Distributed as a prebuilt binary attached to each GitHub Release (no package
registry). There is no published library; the only consumer is the local herdr
keybind.

## Stack & layout

- Go (single module `herdr-goto`, see `go.mod`). Single static binary, no
  runtime deps.
- TUI: Bubble Tea + bubbles (`textinput`, `viewport`, `key`, `help`),
  `lipgloss` for styling, `sahilm/fuzzy` for fuzzy matching/scoring.
- `main.go` — the whole program: herdr CLI JSON shapes, tree building, the
  filter-that-keeps-ancestors, rendering, and `main()`. It's intentionally one
  file; keep it that way unless it clearly outgrows it.
- `scripts/` — release/install helpers (see below).
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
`scripts/build-local.sh` stamps `local-<sha>`).

## How it's wired into herdr

herdr's `~/.config/herdr/config.toml` runs the binary by **fixed path**:

```toml
[[keys.command]]
key = ["prefix+f", "ctrl+alt+f"]
type = "pane"
command = "~/.config/herdr/goto-tui/goto"
```

That path is deliberate and **decoupled from this repo**:

- The repo (source) lives in `~/Developer/herdr-goto`.
- The binary herdr runs lives at `~/.config/herdr/goto-tui/goto`.
- Runtime state (`state.json`, `{"show_panes":bool}`) lives next to it at
  `~/.config/herdr/goto-tui/state.json`. `main.go` resolves it via
  `os.UserConfigDir()`, so it's the same path no matter where the binary runs.

Because herdr points at a fixed path, "switch which version runs" == "replace
the binary at that path". Two ways to do that, never edit `config.toml`:

- **Use the latest release:** `scripts/update-local.sh` downloads the latest
  release asset for this OS/arch into that path.
- **Test local changes:** `scripts/build-local.sh` compiles the current source
  over that same path. Run `scripts/update-local.sh` afterwards to go back to
  the released build.

## Releasing vs. updating the local install

Two separate actions, two separate scripts by design. Do not merge them.

- `scripts/release.sh <X.Y.Z>` only cuts and publishes a release. It gates on a
  clean tree + green `go vet`/`go build`/`go test`, generates the `CHANGELOG.md`
  entry and GitHub release notes from commit subjects since the last tag,
  commits (`chore(release): vX.Y.Z`), tags, pushes, and publishes the GitHub
  release. CI (`.github/workflows/release.yml`) then builds the binary
  (stamping the version from the tag) and attaches `goto-darwin-arm64`. It must
  not touch the local install.
- `scripts/update-local.sh` only refreshes this machine's installed binary to
  the latest published release.

After cutting a release, offer to update the local install, and run
`scripts/update-local.sh` if the user asks (or has already said to do it
automatically). Never update the local install as a side effect of releasing.

The release workflow builds only `darwin/arm64` (the development machine). To
support more platforms, add a build matrix in `release.yml` and upload one
`goto-<os>-<arch>` asset per target; `update-local.sh` already resolves the
asset name from `uname`.

## Behaviour / decisions

- Tree = two levels by default: repo (== main checkout) -> worktrees. Panes are
  hidden by default; `ctrl+t` toggles them, persisted in `state.json`.
- Repos ordered by lowest workspace `number`; worktrees inside a repo too.
  Grouping key: `worktree.repo_key` (falls back to checkout_path, then a pane's
  cwd, then workspace id).
- `1-9` digits ALWAYS jump (never search text): sidebar numbers when unfiltered,
  renumbered 1..N over visible results when filtering.
- Search: fuzzy with scoring + a small kind bonus (repo +8, worktree +4) so
  repo/worktree names outrank panes. Matches keep ancestors visible.
- Enter on repo/worktree -> `workspace focus` (does not change the focused pane
  inside it). Enter on pane -> focus that pane. No autofocus on switch.

## Commits & branches

- Conventional Commits: `type(scope): description` (feat, fix, chore, docs,
  style, refactor, test, perf).
- Never mention AI tooling in commits, PRs, or any repo-visible text.
- Default branch is `main`. Don't commit, tag, or push unless explicitly asked
  (releasing is an explicit, separate request).
