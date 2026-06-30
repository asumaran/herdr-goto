# herdr-goto

A custom tree-style switcher across herdr repos, worktrees and panes, used as a
replacement for herdr's native "goto" navigator. Built because the native goto
rendered too much and didn't focus its search by default, and because herdr's
plugin/UI model can't replace a built-in dialog — so this runs as an external
binary in a herdr pane.

## Where it's wired

`~/.config/herdr/config.toml`, under `[[keys.command]]`:

```toml
[[keys.command]]
key = ["prefix+f", "ctrl+alt+f"]
type = "pane"
command = "~/.config/herdr/goto-tui/goto"
description = "goto (bubbletea tree: 1-9 jump repo, type to search)"
```

`type = "pane"` opens the binary zoomed (full screen) with keyboard focus. herdr
passes `HERDR_BIN_PATH` and `HERDR_SOCKET_PATH` in the env, so the binary calls
the same herdr server.

The keybind points at a **fixed path** (`~/.config/herdr/goto-tui/goto`) that is
deliberately decoupled from this repo:

- Source lives here (`~/Developer/herdr-goto`).
- The binary herdr runs lives at `~/.config/herdr/goto-tui/goto`.
- Runtime state (`state.json`) lives next to it, resolved via `os.UserConfigDir()`.

So switching which version runs never means editing `config.toml` — just replace
the binary at that path.

## Install / update

Releases attach a prebuilt `goto-darwin-arm64` binary. To run the latest release:

```bash
scripts/update-local.sh        # downloads the latest release into the keybind path
```

## Develop

```bash
go build -o goto .             # local build inside the repo
./goto -dump                   # print the tree (no TUI), for debugging without a TTY
./goto -version                # print the embedded version
go vet ./... && go test ./...

scripts/build-local.sh         # build current source over the keybind path to test it live
                               # (run scripts/update-local.sh to go back to the released build)
```

Single static binary, no runtime deps.

## Release

```bash
scripts/release.sh 0.2.0       # gate, tag, push, publish the GitHub release; CI attaches the binary
```

See `CLAUDE.md` for the full release vs. update-local model.

## Stack

- Bubble Tea (runtime) + bubbles `textinput` / `viewport` / `key` / `help`.
- `lipgloss` for styling.
- `sahilm/fuzzy` for fuzzy matching + scoring + matched-char highlighting (the
  same matcher `bubbles/list` uses). The tree, the filter-that-keeps-ancestors,
  the grouping and the numbering are custom (no tree component fits).

## herdr CLI it depends on

- Read: `herdr workspace list`, `herdr pane list` (JSON).
- Act: `herdr workspace focus <wsID>` (repo/worktree), `herdr agent focus <paneID>`
  (a specific pane; resolves pane_id and focuses it even for shell panes).

## Behaviour / decisions

- Tree = two levels by default: repo (== main checkout) -> worktrees. Panes are
  hidden by default; `ctrl+t` toggles them, and that choice persists in
  `~/.config/herdr/goto-tui/state.json` (`{"show_panes":bool}`).
- Repos are ordered by where they first appear in the sidebar (lowest workspace
  `number`); worktrees inside a repo also by `number`.
- Repo grouping key: `worktree.repo_key` (falls back to checkout_path, then a
  pane's cwd, then workspace id). Workspaces herdr doesn't report worktree
  metadata for may show as their own group — known rough edge.
- `[n]` digits: original sidebar numbers when unfiltered, renumbered 1..N over
  the visible results when filtering. A digit ALWAYS jumps (never search text) —
  decided to keep it simple; no number search (fuzzy finds "ESHOP-1085" by
  typing letters).
- Search: fuzzy (substring-tolerant) with scoring; a small kind bonus (repo +8,
  worktree +4) so repo/worktree names outrank panes (typing "h" -> herdr).
  Matches keep their ancestors visible; cursor jumps to the best match.
- Enter on a repo/worktree -> `workspace focus` (does NOT change which pane is
  focused inside it; lands where you left it). Enter on a pane -> focus that pane.
- No autofocus: switching repos must not select the agent pane by default.
- A constant 2-col gutter keeps content aligned whether or not a row is selected.

## Keys

`1-9` jump repo · type to fuzzy-search · `↑↓`/`ctrl-n/p` move · `enter` select ·
`ctrl+t` toggle panes · `esc` cancel.
