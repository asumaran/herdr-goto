#!/usr/bin/env python3
"""Pty driver for the README demo recording (see record.sh).

Runs a command inside a fixed-size pty, relays its output to stdout (which
asciinema records) and answers the terminal queries TUIs send at startup
(colors, cursor position, device attributes) — nothing answers them inside a
bare pty, which would leave termenv-style detection blocked and our scripted
keystrokes swallowed as query replies.

Modes:
  hold — keep the program alive until killed (session setup, not recorded)
  demo — replay the demo keystroke script, then let the program keep the
         final frame briefly before tearing down

Usage: driver.py <hold|demo> <command> [args...]
"""

import fcntl
import os
import pty
import re
import select
import signal
import struct
import sys
import termios
import time

COLS = int(os.environ.get("DEMO_COLS", "170"))
ROWS = int(os.environ.get("DEMO_ROWS", "44"))

PREFIX = b"\x02"  # ctrl+b; prefix+f opens the goto popup (plugin_action)

# (seconds to wait before sending, bytes) — waits are relative to the
# previous step. Pauses leave time for the popup to open and for the async
# PR fetch so the ticket/PR columns are visible before searching.
DEMO_SCRIPT = [
    (3.0, PREFIX), (0.35, b"f"),
    (2.4, b""),
    # fuzzy search: "price" narrows to shopnest's test-format-price-util
    (0.0, b"p"), (0.19, b"r"), (0.16, b"i"), (0.18, b"c"), (0.15, b"e"),
    (1.3, b""),
    (0.0, b"\r"),
    # sidebar selection moved; open the popup again from the new workspace
    (2.2, PREFIX), (0.35, b"f"),
    (2.0, b""),
    (0.0, b"a"), (0.19, b"s"), (0.17, b"d"), (0.16, b"e"), (0.18, b"v"),
    (1.2, b""),
    (0.0, b"\r"),
    # linger on the result, then detach cleanly (prefix+q)
    (2.4, PREFIX), (0.35, b"q"),
    (0.8, b""),
]

QUERY_REPLIES = [
    (re.compile(rb"\x1b\]11;\?(\x07|\x1b\\)"),
     lambda m: b"\x1b]11;rgb:1e1e/1e1e/2e2e" + m.group(1)),
    (re.compile(rb"\x1b\]10;\?(\x07|\x1b\\)"),
     lambda m: b"\x1b]10;rgb:f8f8/f8f8/f2f2" + m.group(1)),
    (re.compile(rb"\x1b\[6n"), lambda m: b"\x1b[1;1R"),
    (re.compile(rb"\x1b\[0?c"), lambda m: b"\x1b[?62;22c"),
    (re.compile(rb"\x1b\[>0?q"), lambda m: b"\x1bP>|demo-driver\x1b\\"),
    (re.compile(rb"\x1b\[\?u"), lambda m: b"\x1b[?0u"),
    (re.compile(rb"\x1b\[\?(\d+)\$p"), lambda m: b"\x1b[?" + m.group(1) + b";0$y"),
]


def answer_queries(fd: int, buf: bytes) -> bytes:
    """Replies to terminal queries found in buf; returns the unmatched tail
    kept for queries split across reads."""
    for rx, reply in QUERY_REPLIES:
        while True:
            m = rx.search(buf)
            if not m:
                break
            os.write(fd, reply(m))
            buf = buf[: m.start()] + buf[m.end():]
    return buf[-32:]


def main() -> int:
    mode, cmd = sys.argv[1], sys.argv[2:]
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(cmd[0], cmd)

    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    out = sys.stdout.buffer
    steps = iter(DEMO_SCRIPT if mode == "demo" else [])
    step = next(steps, None)
    deadline = time.monotonic() + step[0] if step else None
    tail = b""

    while True:
        timeout = max(0.0, deadline - time.monotonic()) if deadline else 0.5
        ready, _, _ = select.select([fd], [], [], timeout)
        if ready:
            try:
                data = os.read(fd, 65536)
            except OSError:
                break
            if not data:
                break
            out.write(data)
            out.flush()
            tail = answer_queries(fd, tail + data)
        if deadline is not None and time.monotonic() >= deadline:
            os.write(fd, step[1])
            step = next(steps, None)
            deadline = time.monotonic() + step[0] if step else None
        if mode == "demo" and step is None and deadline is None:
            break

    if mode == "demo":
        # the demo ends with the client still attached: detach by killing it
        os.kill(pid, signal.SIGTERM)
    os.waitpid(pid, 0)
    return 0


if __name__ == "__main__":
    sys.exit(main())
