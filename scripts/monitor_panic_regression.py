#!/usr/bin/env python3
"""Out-of-viewport historical CSI margins must not crash the owning TUI."""

import os
import pathlib
import sys
import tempfile
import termios

from integration import Harness, until
from screen_regression import Terminal
from ui_regression import wait_view
from monitor_dialog_regression import resize

FIXTURE = r"""
import os
import signal
import tty

name = __import__("sys").argv[1]
tty.setraw(0)
title = ("\x1b]0;查看5090当前训练状态 | codex\a").encode()


def paint(*args):
    os.write(
        1, b"\x1b[1;999r\x1b[H\x1b[L\x1bM\x1b[?69h\x1b[1;999s\x1b[L\x1b[?69l\x1b[r\x1b[H\x1b[2J"
    )
    os.write(1, ("PREVIEW-ALIVE-" + name + "\r\n").encode() + title)


signal.signal(signal.SIGWINCH, paint)
paint()
while True:
    p = os.read(0, 4096)
    if not p:
        break
    os.write(1, b"INPUT:" + p.hex().encode() + b"\r\n" + title)
"""


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "bin/tdock").resolve()
    with tempfile.TemporaryDirectory(prefix="tdock-parser-") as root:
        harness = Harness(root, binary)
        try:
            harness.cli("settings", "set", "language", "en")
            harness.cli("settings", "set", "history-retention", "off")
            historical = harness.create("a-historical", sys.executable, "-c", FIXTURE, "historical")
            healthy = harness.create("b-healthy", sys.executable, "-c", FIXTURE, "healthy")
            before = {
                task_id: harness.show(task_id)["runtime"] for task_id in [historical, healthy]
            }
            pid, tty_fd = harness.spawn_tty()
            terminal = Terminal()
            wait_view(tty_fd, terminal, lambda text: "a-historical" in text and "Memory" in text)
            for _ in range(3):
                os.write(tty_fd, b"v")
                wait_view(
                    tty_fd,
                    terminal,
                    lambda text: "PREVIEW-ALIVE-historical" in text and "Control" in text,
                )
                os.write(tty_fd, b"\x1b[<64;5;5M")
                wait_view(tty_fd, terminal, lambda text: "INPUT:1b5b3c36343b353b354d" in text)
                assert "codex" not in "".join(
                    terminal.display
                ), "Unicode title leaked into monitor during wheel redraw"
                for rows, cols in [(30, 120), (24, 80)]:
                    resize(pid, tty_fd, terminal, rows, cols)
                    wait_view(
                        tty_fd,
                        terminal,
                        lambda text: "TaskDock" in terminal.display[0]
                        and "PREVIEW-ALIVE-historical" in text,
                    )
                os.write(tty_fd, b"usable")
                until(
                    lambda: b"INPUT:757361626c65" in harness.cli("logs", historical, "--raw").stdout
                )
                os.write(tty_fd, b"\x1b[B")
                wait_view(tty_fd, terminal, lambda text: "PREVIEW-ALIVE-healthy" in text)
                os.write(tty_fd, b"\x1b[A")
                wait_view(tty_fd, terminal, lambda text: "PREVIEW-ALIVE-historical" in text)
                os.write(tty_fd, b"\x1b")
                wait_view(tty_fd, terminal, lambda text: "Memory" in text and "Control" not in text)
            os.write(tty_fd, b"\x03")
            wait_view(tty_fd, terminal, lambda text: not terminal.alternate)
            settings = termios.tcgetattr(tty_fd)
            assert harness.join(pid, on_output=terminal.feed) == 0
            assert settings[3] & termios.ICANON and settings[3] & termios.ECHO
            assert not terminal.alternate and not terminal.main.cursor.hidden
            for task_id in [historical, healthy]:
                assert harness.show(task_id)["runtime"] == before[task_id]
            print("PASS monitor parser, Unicode titles, resize and terminal cleanup")
        finally:
            harness.cleanup()


if __name__ == "__main__":
    main()
