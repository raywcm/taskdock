#!/usr/bin/env python3
"""Exercise the default attached-session status row and nested-start guard."""

import fcntl
import json
import os
import pathlib
import shlex
import signal
import struct
import sys
import tempfile
import termios

from integration import Harness, until
from screen_regression import Terminal
from ui_regression import wait_view

FIXTURE = r"""
import os
import signal
import tty

tty.setraw(0)


def paint(*args):
    size = os.get_terminal_size(0)
    os.write(
        1,
        (
            "\x1b[H\x1b[2J\x1b[38;2;17;153;221mAPP-TITLE\x1b[0m SIZE-%d-%d\x1b[%d;1HBOTTOM-CELL"
            % (size.lines, size.columns, size.lines)
        ).encode(),
    )
    os.write(1, ("\x1b[2;%dHX" % size.columns).encode())
    os.write(1, ("\x1b]0;查看5090当前训练状态 | codex\a").encode())


signal.signal(signal.SIGWINCH, paint)
os.write(1, b"\x1b[?1049h\x1b[?1006h\x1b[?1000h\x1b[?2004h")
paint()
while True:
    data = os.read(0, 4096)
    if b"Q" in data:
        os.write(1, b"\x1b[3;7H\x1b[6n")
    elif data.endswith(b"R"):
        os.write(1, b"\x1b[5;1HREPLY-" + data.hex().encode())
    elif data.startswith(b"\x1b[<64;"):
        paint()
        os.write(1, b"\x1b[7;1HWHEEL-SEEN")
    elif b"S" in data:
        os.write(1, b"\x1b[?1049l\x1b[r\x1b[2J" + b"LINE\r\n" * 60 + b"SCROLLED")
    else:
        paint()
"""


def check_status(harness):
    ref = harness.create("status-work", sys.executable, "-c", FIXTURE)
    original = harness.show(ref)["runtime"]
    pid, fd = harness.spawn_tty("attach", ref)
    terminal = Terminal()
    wait_view(fd, terminal, lambda text: "SIZE-23-80" in text and "RSS" in text)
    assert "status-work" in terminal.display[-1], terminal.display
    assert "BOTTOM-CELL" in terminal.display[-2], terminal.display
    assert (
        b"SIZE-22-80" not in harness.cli("logs", ref).stdout
    ), "status attach bounced the application dimensions"
    assert terminal.display[1][-1] == "X", "rightmost application cell was erased"
    assert terminal.current.buffer[0][0].fg == "1199dd", "truecolor changed"
    wait_view(
        fd, terminal, lambda text: "MiB" in text or "KiB" in text or "GiB" in text, timeout=15
    )
    os.write(fd, b"\x1b[<64;5;5M" * 6)
    wait_view(fd, terminal, lambda text: "WHEEL-SEEN" in text)
    assert "codex" not in "".join(
        terminal.display
    ), "Unicode window title leaked into the prompt after scrolling"
    os.write(fd, b"Q")
    wait_view(fd, terminal, lambda text: "REPLY-1b5b333b3752" in text)
    harness.cli("rename", ref, "renamed-work")
    wait_view(fd, terminal, lambda text: "renamed-work" in text, timeout=5)
    assert "BOTTOM-CELL" in terminal.display[-2]
    # Physical window height includes the footer. Only a real resize changes it.
    terminal.columns, terminal.rows = 100, 30
    terminal.main.resize(lines=30, columns=100)
    terminal.alt.resize(lines=30, columns=100)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
    os.kill(pid, signal.SIGWINCH)
    wait_view(fd, terminal, lambda text: "SIZE-29-100" in text
              and "renamed-work" in terminal.display[-1] and "BOTTOM-CELL" in terminal.display[-2])
    # A small follower crops the existing screen and keeps its own footer.
    follower, follower_fd = harness.spawn_tty("attach", ref, "-r")
    follow_screen = Terminal()
    wait_view(follower_fd, follow_screen, lambda text: "renamed-work" in text and "RSS" in text)
    assert "renamed-work" in follow_screen.display[-1]
    conn, kind, welcome = harness.socket(ref, "control")
    assert json.loads(welcome)["size"] == {"rows": 29, "cols": 100}
    conn.close()
    os.write(follower_fd, b"\x1b[24~")
    assert harness.join(follower) == 0
    os.write(fd, b"S")
    wait_view(fd, terminal, lambda text: "SCROLLED" in text)
    assert "renamed-work" in terminal.display[-1], terminal.display
    os.write(fd, b"\x1b[24~")
    assert harness.join(pid, on_output=terminal.feed) == 0
    assert not terminal.alternate
    assert harness.show(ref)["runtime"] == original
    native, native_fd = harness.spawn_tty("--no-status", "attach", ref)
    native_screen = Terminal()
    wait_view(native_fd, native_screen, lambda text: "SIZE-24-80" in text)
    assert "BOTTOM-CELL" in native_screen.display[-1] and "RSS" not in "".join(
        native_screen.display
    )
    os.write(native_fd, b"\x1b[24~")
    assert harness.join(native) == 0
    harness.cli("stop", ref)
    print("PASS fullscreen status, Unicode titles, resize and reconnect", flush=True)

def check_scrollback(harness):
    inline = r"""
import os
import signal
import tty
tty.setraw(0)
signal.signal(signal.SIGWINCH, lambda *_: os.write(1, b"\r\nINLINE-PROMPT"))
for i in range(75):
    os.write(1, f"Earlier row {i:03d}\r\n".encode())
os.write(1, b"INLINE-PROMPT")
while True:
    data = os.read(0, 4096)
    if not data:
        break
    os.write(1, b"\r\nAPPLICATION-INPUT:" + data.hex().encode())
"""
    ref = harness.create("inline-agent", sys.executable, "-c", inline)
    until(lambda: b"INLINE-PROMPT" in harness.cli("logs", ref).stdout)
    for monitor in (False, True):
        args = () if monitor else ("attach", "--read-only", ref)
        pid, fd = harness.spawn_tty(*args)
        terminal = Terminal()
        if monitor:
            wait_view(fd, terminal, lambda text: "inline-agent" in text)
            os.write(fd, b"v")
        reporting = 1002 if monitor else 1000
        live_marker = "Earlier row 060" if monitor else "INLINE-PROMPT"
        wait_view(fd, terminal, lambda text: live_marker in text
                  and reporting << 5 in terminal.current.mode
                  and 1006 << 5 in terminal.current.mode)
        os.write(fd, b"\x1b[<64;30;8M" * 30)
        wait_view(fd, terminal, lambda text: "Earlier row 000" in text
                  and live_marker not in text)
        os.write(fd, b"\x1b[<65;30;8M" * 30)
        wait_view(fd, terminal, lambda text: live_marker in text
                  and "Earlier row 000" not in text)
        os.write(fd, b"\x1b[24~")
        if monitor:
            wait_view(fd, terminal, lambda text: "Control" not in text and "inline-agent" in text)
            os.write(fd, b"q")
        assert harness.join(pid) == 0
    assert b"APPLICATION-INPUT:" not in harness.cli("logs", ref).stdout
    harness.cli("stop", ref)
    print("PASS inline app scrollback in read-only fullscreen and monitor without injecting wheel input", flush=True)


def check_nesting(harness):
    binary = shlex.quote(str(harness.binary))
    state = shlex.quote(str(harness.state))
    # Simulate an already-running old shell. Its broker has published runtime
    # identity by this point, unlike a child racing its initial readiness write.
    settled = r"""
import json
import os
import pathlib
import time
path = pathlib.Path(__import__('sys').argv[1])/'tasks'/os.environ['TDOCK_SESSION']/'runtime.json'
deadline = time.monotonic()+10
while time.monotonic()<deadline:
 try:
  if json.loads(path.read_text()).get('state')=='running':break
 except (OSError,ValueError):pass
 time.sleep(.01)
else:raise SystemExit('fixture broker did not become ready')
"""
    wait_old = shlex.quote(sys.executable) + " -c " + shlex.quote(settled) + " " + state
    cases = [
        ("manager", f"{binary} --state-dir {state}"),
        ("attach", f"{binary} --state-dir {state} attach missing"),
        ("new", f"{binary} --state-dir {state} new -ds forbidden -- /bin/sh"),
        ("old-broker", f"{wait_old}; unset TDOCK_SESSION; {binary} --state-dir {state}"),
        ("other-root", f"{binary} --state-dir {state}-other new -ds forbidden -- /bin/sh"),
    ]
    for name, command in cases:
        ref = harness.create(
            "nested-" + name, "/bin/sh", "-c", command + "; printf END-GUARD; sleep 30"
        )
        until(lambda: b"END-GUARD" in harness.cli("logs", ref).stdout)
        output = harness.cli("logs", ref).stdout
        assert b"already inside a TaskDock session" in output, (name, output)
        harness.cli("stop", ref)
    ref = harness.create(
        "management",
        "/bin/sh",
        "-c",
        f"{binary} --state-dir {state} ls --json; printf END-MANAGEMENT",
    )
    until(lambda: b"END-MANAGEMENT" in harness.cli("logs", ref).stdout)
    assert b"already inside" not in harness.cli("logs", ref).stdout
    assert not any(
        r["spec"]["name"] == "forbidden" for r in json.loads(harness.cli("ls", "--json").stdout)
    )
    print("PASS nested-session guard and management commands", flush=True)


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "bin/tdock").resolve()
    with tempfile.TemporaryDirectory(prefix="tdock-status-") as root:
        harness = Harness(root, binary)
        try:
            harness.cli("settings", "set", "language", "en")
            harness.cli("settings", "set", "history-retention", "off")
            check_status(harness)
            check_scrollback(harness)
            check_nesting(harness)
        finally:
            harness.cleanup()


if __name__ == "__main__":
    main()
