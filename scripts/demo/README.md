# Demo recording

Tooling to (re-)record the README demo GIF (`docs/demo.gif`).

```bash
scripts/demo/record.sh
```

## What it does

1. Builds `./goto` stamped with the manifest version, so the popup prompt
   shows the release look (`goto ❯`, no `(dev)` marker). The plain dev build
   is restored when the script exits.
2. Boots an isolated herdr session (`gotodemo`, own socket and state) and
   populates it only with personal repos: main checkouts adopted via
   `worktree open --workspace`, shopnest's linked worktrees via
   `worktree open --cwd`, plus a bottom split in each visited workspace so
   herdr's pane dividers are visible. The default herdr session is never
   touched.
3. Records a client attached to that session with asciinema (headless), while
   `driver.py` replays the keystroke script: `prefix+f` -> popup -> type
   `price` -> enter -> `prefix+f` -> type `asdev` -> enter -> detach.
4. Trims the detach tail from the cast and renders the GIF with agg using
   Berkeley Mono.
5. Tears the demo session down.

## Requirements

- herdr >= 0.7.5 with the `asumaran.goto` plugin registered and the
  `prefix+f` `plugin_action` keybind.
- `asciinema` >= 3 and `agg` (`brew install asciinema agg`).
- `gh` authenticated (live PR info on the demo repos).
- Berkeley Mono in `~/Library/Fonts` (agg renders the GIF with it).
- The demo repos/worktrees listed at the top of `record.sh` must exist.

## Knobs

- `DEMO_COLS` / `DEMO_ROWS` — recording terminal size (default 170x44).
- `DEMO_KEEP_CAST=/path.cast` — keep the trimmed cast for inspection.
- The popup opens at 60% x 60% (vs the manifest's 45% x 50%) via
  `GOTO_POPUP_WIDTH`/`GOTO_POPUP_HEIGHT`, injected into the demo session
  server's env and honored by `scripts/open-pane.sh`.

## Why driver.py answers terminal queries

TUIs (termenv/lipgloss, herdr itself) query the terminal at startup — OSC
10/11 (colors), CSI 6n (cursor), DA — and block waiting for replies. Nothing
answers inside a bare pty or asciinema's headless mode, which stalls startup
and makes the scripted keystrokes get swallowed as query replies, so the
driver acts as a minimal terminal and answers them.
