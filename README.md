# herdr-goto

A custom tree-style switcher across herdr repos, worktrees and panes, used as a
replacement for herdr's native "goto" navigator. Built because the native goto
rendered too much and didn't focus its search by default. It runs as a herdr
plugin pane.

## Install as a herdr plugin

Requires herdr >= 0.7.5 on macOS:

```bash
herdr plugin install asumaran/herdr-goto
```

The install's build step (`scripts/fetch-binary.sh`) downloads the prebuilt
binary from the GitHub release matching the manifest's version, so no Go
toolchain is needed on `darwin/arm64`. On platforms without a release asset it
falls back to `go build` (then Go is required); if neither path works the
install aborts.

To always compile locally instead of running the prebuilt binary (requires Go):

```bash
GOTO_BUILD_FROM_SOURCE=1 herdr plugin install asumaran/herdr-goto
```

Then bind a key to the plugin action (herdr has no `plugin_pane` keybind type,
so the pane is opened through the `open` action) in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = ["prefix+f", "ctrl+alt+f"]
type = "plugin_action"
command = "asumaran.goto.open"
description = "goto (bubbletea tree: type to search)"
```

The pane opens as a session-modal `popup` (45% x 50%, sized in the manifest)
with keyboard focus. herdr
injects `HERDR_BIN_PATH` / `HERDR_SOCKET_PATH` (so the binary talks to the same
herdr server) and `HERDR_PLUGIN_STATE_DIR`, where runtime state (`state.json`,
`prcache.json`) lives.

For local development, `herdr plugin link ~/Developer/herdr-goto` registers the
working copy. `plugin link` does **not** run build commands, so build the binary
yourself first (`go build -o goto .` — build from source, don't run
`fetch-binary.sh`, which would fetch the released build instead of your
changes).

## Develop

```bash
go build -o goto .             # local build inside the repo
./goto -dump                   # print the tree (no TUI), for debugging without a TTY
./goto -version                # print the embedded version
go vet ./... && go test ./...
```

Single static binary, no runtime deps.

## Release

```bash
scripts/release.sh 0.2.0       # gate, tag, push, publish the GitHub release; CI attaches the binary
```

The release asset (`goto-darwin-arm64`) is what `fetch-binary.sh` downloads on
plugin installs, so every release must keep attaching it.

## Stack

- Bubble Tea (runtime) + bubbles `textinput` / `viewport` / `key` / `help`.
- `lipgloss` for styling.
- `sahilm/fuzzy` for fuzzy matching + scoring + matched-char highlighting (the
  same matcher `bubbles/list` uses). The tree, the filter-that-keeps-ancestors
  and the grouping are custom (no tree component fits).

## herdr CLI it depends on

- Read: `herdr workspace list`, `herdr pane list` (JSON).
- Act: `herdr workspace focus <wsID>` (repo/worktree), `herdr agent focus <paneID>`
  (a specific pane; resolves pane_id and focuses it even for shell panes).

## Behaviour / decisions

- Tree = two levels by default: repo (== main checkout) -> worktrees. Panes are
  hidden by default; `ctrl+t` toggles them, and that choice persists in
  `state.json` (`{"show_panes":bool}`) under `HERDR_PLUGIN_STATE_DIR` when
  running as a plugin, or `~/.config/herdr/goto-tui/` when run standalone
  (outside herdr, e.g. for debugging).
- Repos are ordered by where they first appear in the sidebar (lowest workspace
  `number`); worktrees inside a repo also by `number`.
- Repo grouping key: `worktree.repo_key` (falls back to checkout_path, then a
  pane's cwd, then workspace id). Workspaces herdr doesn't report worktree
  metadata for may show as their own group — known rough edge.
- Search: fuzzy (substring-tolerant) with scoring; a small kind bonus (repo +8,
  worktree +4) so repo/worktree names outrank panes (typing "h" -> herdr).
  Besides the label, the branch, the Jira ticket and the PR number are matched
  (typing "1234" finds the row showing #1234). Matches keep their ancestors
  visible; cursor jumps to the best match. There used to be a "digits 1-9 jump
  to a numbered repo" mode; it was removed because it conflicted with searching
  by PR/ticket number.
- Enter on a repo/worktree -> `workspace focus` (does NOT change which pane is
  focused inside it; lands where you left it). Enter on a pane -> focus that pane.
- No autofocus: switching repos must not select the agent pane by default.
- A constant 2-col gutter keeps content aligned whether or not a row is selected.

## Keys

Type to fuzzy-search · `↑↓`/`ctrl-n/p` move · `enter` select ·
`ctrl+t` toggle panes · `esc` cancel.
