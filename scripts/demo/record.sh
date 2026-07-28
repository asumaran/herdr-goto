#!/usr/bin/env bash
# record.sh — record the README demo GIF (docs/demo.gif).
#
# Spins up an isolated herdr session ("gotodemo") populated only with personal
# repos, records a client attached to it with asciinema (driving prefix+f ->
# goto popup -> search -> switch, twice), renders the cast to a GIF with agg,
# and tears the session down. The user's default herdr session is never
# touched.
#
# Requires: herdr >= 0.7.5 with the asumaran.goto plugin registered and the
# prefix+f plugin_action keybind, asciinema >= 3, agg (brew install
# asciinema agg), gh (for live PR info on the demo repos).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

# Build the demo binary stamped as the released version so the popup prompt
# shows the release look ("goto ❯", no "(dev)" marker). cleanup() restores
# the plain dev build for the linked plugin afterwards.
MANIFEST_VERSION="$(sed -n 's/^version = "\(.*\)"/\1/p' herdr-plugin.toml)"
go build -ldflags "-X main.version=v${MANIFEST_VERSION}" -o goto .

# The demo popup opens bigger than the manifest's 45% x 50% so it reads well
# in the GIF; open-pane.sh picks these up from the session server's env.
POPUP_ENV=(GOTO_POPUP_WIDTH=60% GOTO_POPUP_HEIGHT=60%)

SESSION="gotodemo"
DRIVER="$ROOT/scripts/demo/driver.py"

# Repos/worktrees shown in the demo (personal projects only). Main checkouts
# are adopted with `worktree open --workspace`; linked worktrees are opened
# with the owning repo as --cwd context.
REPOS=(
  "$HOME/Developer/shopnest"
  "$HOME/Developer/worktree-cli"
  "$HOME/Developer/asdev"
  "$HOME/Developer/asreviewer"
  "$HOME/Developer/aspage"
)
WORKTREES=(
  "$HOME/Developer/shopnest:$HOME/wt/shopnest/feat-cart-has-product"
  "$HOME/Developer/shopnest:$HOME/wt/shopnest/fix-SHOP-4-product-card-accessibility"
  "$HOME/Developer/shopnest:$HOME/wt/shopnest/test-format-price-util"
)

# The recording may itself run from inside a herdr pane; strip the inherited
# plugin/pane env so the demo client doesn't refuse to nest and the CLI talks
# to the demo session instead of the enclosing one.
CLEAN_ENV=(env -u HERDR_ENV -u HERDR_SOCKET_PATH -u HERDR_PANE_ID
  -u HERDR_TAB_ID -u HERDR_WORKSPACE_ID -u HERDR_STARTUP_CWD
  TERM=xterm-256color COLORTERM=truecolor)
DS="$HOME/.config/herdr/sessions/$SESSION/herdr.sock"

demo() { "${CLEAN_ENV[@]}" HERDR_SOCKET_PATH="$DS" "$@"; }

cleanup() {
  herdr session stop "$SESSION" >/dev/null 2>&1 || true
  sleep 1
  herdr session delete "$SESSION" >/dev/null 2>&1 || true
  [[ -n "${hold_pid:-}" ]] && kill "$hold_pid" 2>/dev/null || true
  rm -rf "${cast:-}" 2>/dev/null || true
  # Restore the plain dev build for the linked plugin (the demo binary is
  # stamped with the release version).
  go build -o goto . 2>/dev/null || true
}
trap cleanup EXIT

# Fresh session every run so the recording is deterministic.
herdr session stop "$SESSION" >/dev/null 2>&1 || true
sleep 1
herdr session delete "$SESSION" >/dev/null 2>&1 || true

# Starting a client is what boots the session server; hold one in a hidden
# pty while we populate the workspaces. Launch it from the first repo so the
# initial workspace is a personal project too. The popup-size overrides ride
# on the server's env (plugin commands inherit it).
(cd "$HOME/Developer/herdr-goto" &&
  "${CLEAN_ENV[@]}" "${POPUP_ENV[@]}" python3 "$DRIVER" hold herdr --session "$SESSION") >/dev/null &
hold_pid=$!
for _ in $(seq 1 40); do [[ -S "$DS" ]] && break; sleep 0.5; done
[[ -S "$DS" ]] || { echo "demo session socket never appeared" >&2; exit 1; }
sleep 2

# The launch workspace (herdr-goto) exists but has no worktree metadata yet;
# adopt it, then open the rest.
first_ws="$(demo herdr workspace list | python3 -c \
  'import json,sys; print(json.load(sys.stdin)["result"]["workspaces"][0]["workspace_id"])')"
demo herdr worktree open --workspace "$first_ws" --path "$HOME/Developer/herdr-goto" --no-focus >/dev/null
for repo in "${REPOS[@]}"; do
  wid="$(demo herdr workspace create --cwd "$repo" --no-focus | python3 -c \
    'import json,sys; print(json.load(sys.stdin)["result"]["workspace"]["workspace_id"])')"
  demo herdr worktree open --workspace "$wid" --path "$repo" --no-focus >/dev/null
done
for pair in "${WORKTREES[@]}"; do
  demo herdr worktree open --cwd "${pair%%:*}" --path "${pair#*:}" --no-focus >/dev/null
done
# Give the workspaces the demo visits a bottom split so herdr's pane
# dividers are visible, like a real working layout.
for target in "$HOME/Developer/herdr-goto" \
              "$HOME/wt/shopnest/test-format-price-util" \
              "$HOME/Developer/asdev"; do
  pane_id="$(demo herdr pane list | python3 -c '
import json, sys
target = sys.argv[1]
for p in json.load(sys.stdin)["result"]["panes"]:
    if p.get("cwd") == target:
        print(p["pane_id"]); break
' "$target")"
  [[ -n "$pane_id" ]] &&
    demo herdr pane split --pane "$pane_id" --direction down --ratio 0.72 --no-focus >/dev/null
done

demo herdr workspace focus "$first_ws" >/dev/null

# Drop the setup client; the session server keeps running.
kill "$hold_pid" 2>/dev/null || true
wait "$hold_pid" 2>/dev/null || true
hold_pid=""
sleep 1

cast="$(mktemp -t goto-demo).cast"
# Terminal size for the recording; the goto popup is 45% x 50% of this, so
# keep it wide enough that ticket/PR columns and labels fit untruncated.
COLS="${DEMO_COLS:-170}"
ROWS="${DEMO_ROWS:-44}"

# --headless: don't touch the invoking terminal. --window-size matches the
# driver's pty. asciicast-v2 because agg doesn't read the newer v3 format.
"${CLEAN_ENV[@]}" DEMO_COLS="$COLS" DEMO_ROWS="$ROWS" \
  asciinema rec --headless --overwrite -f asciicast-v2 \
  --window-size "${COLS}x${ROWS}" \
  -c "python3 $DRIVER demo herdr session attach $SESSION" "$cast"

# Trim the detach tail (the PREFIX indicator from pressing prefix+q, the
# alt-screen exit and the "detached from server" message) so the GIF ends —
# and loops — on the final UI frame. Cut 1.5s before the detach marker to
# also drop the prefix-indicator redraw.
python3 - "$cast" <<'EOF'
import json, sys
path = sys.argv[1]
lines = open(path).read().splitlines(True)
events = [json.loads(l) for l in lines[1:]]
cut = None
for t, kind, data in events:
    if kind == "o" and ("\x1b[?1049l" in data or "detached from server" in data):
        cut = t - 1.5
        break
out = [lines[0]]
for line, (t, _, _) in zip(lines[1:], events):
    if cut is not None and t >= cut:
        break
    out.append(line)
open(path, "w").writelines(out)
EOF

agg --theme dracula --font-dir "$HOME/Library/Fonts" --font-family "Berkeley Mono" \
  --font-size 14 --last-frame-duration 2 "$cast" docs/demo.gif
echo "wrote docs/demo.gif"

# Debug aid: keep the trimmed cast around for inspection.
[[ -n "${DEMO_KEEP_CAST:-}" ]] && cp "$cast" "$DEMO_KEEP_CAST"
