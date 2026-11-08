#!/usr/bin/env python3
"""Real-PTY embedded control, opaque input, manager keys and task identity."""

import os
import pathlib
import re
import sys
import tempfile
import time
import shutil

from integration import Harness, until
from screen_regression import Terminal
from ui_regression import wait_view
from monitor_dialog_regression import latest_size

FIXTURE = r"""
import os
import tty
import signal

name = __import__("sys").argv[1]
tty.setraw(0)


def paint(*args):
    s = os.get_terminal_size(0)
    os.write(1, ("READY-%s\r\nSIZE:%d,%d\r\n" % (name, s.lines, s.columns)).encode())


signal.signal(signal.SIGWINCH, paint)
paint()
while True:
    p = os.read(0, 4096)
    if not p:
        break
    os.write(1, b"INPUT:" + p.hex().encode() + b"\r\n")
"""


def received(harness, task_id):
    return b"".join(
        bytes.fromhex(hex_input.decode())
        for hex_input in re.findall(
            rb"INPUT:([0-9a-f]+)", harness.cli("logs", task_id, "--raw").stdout
        )
    )


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "bin/tdock").resolve()
    with tempfile.TemporaryDirectory(prefix="tdock-monitor-control-") as root:
        harness = Harness(root, binary)
        try:
            harness.cli("settings", "set", "language", "en")
            harness.cli("settings", "set", "history-retention", "off")
            alpha = harness.create("alpha", sys.executable, "-c", FIXTURE, "alpha")
            beta = harness.create("beta", sys.executable, "-c", FIXTURE, "beta")
            until(
                lambda: b"READY-alpha" in harness.cli("logs", alpha).stdout
                and b"READY-beta" in harness.cli("logs", beta).stdout
            )
            before = {task_id: harness.show(task_id)["runtime"] for task_id in [alpha, beta]}
            pid, tty_fd = harness.spawn_tty()
            terminal = Terminal()
            wait_view(tty_fd, terminal, lambda text: "alpha" in text and "Memory" in text)
            os.write(tty_fd, b"v" + b"nscqvh")
            wait_view(tty_fd, terminal, lambda text: "READY-alpha" in text and "Control" in text)
            expected = b"nscqvh"
            until(lambda: received(harness, alpha) == expected)
            payload = (
                b"\r\x03\x04\x02d\x02\x04\x1dd\x1b[C\x1b[D"
                + "中文🙂".encode()
                + b"\x1b[200~\x11\x1b[113;5u\x1b[24~\x02d\x1b[A\x1b[B\x1b\x1b[201~"
            )
            os.write(tty_fd, payload)
            expected += payload
            until(lambda: received(harness, alpha) == expected)
            os.write(tty_fd, b"\x1b")
            wait_view(tty_fd, terminal, lambda text: "Memory" in text and "Control" not in text)
            assert received(harness, alpha) == expected, "Esc reached application"
            os.write(tty_fd, b"v")
            wait_view(tty_fd, terminal, lambda text: "Control" in text and "READY-alpha" in text)
            os.write(tty_fd, b"\x1b[Bbeta-input")
            wait_view(tty_fd, terminal, lambda text: "READY-beta" in text and "Control" in text)
            until(lambda: received(harness, beta) == b"beta-input")
            assert received(harness, alpha) == expected
            os.write(tty_fd, b"\x1b[Aalpha-input")
            wait_view(tty_fd, terminal, lambda text: "READY-alpha" in text)
            expected += b"alpha-input"
            until(lambda: received(harness, alpha) == expected)
            # SS3 and CSI-u arrow keys are navigation too; literal input follows the selected task.
            os.write(tty_fd, b"\x1bOBmore-beta")
            wait_view(tty_fd, terminal, lambda text: "READY-beta" in text)
            until(lambda: received(harness, beta) == b"beta-inputmore-beta")
            for byte in b"\x1b[57352u":
                os.write(tty_fd, bytes([byte]))
                time.sleep(0.005)
            wait_view(tty_fd, terminal, lambda text: "READY-alpha" in text)
            assert received(harness, alpha) == expected, "navigation bytes reached application"
            alpha_size = latest_size(harness, alpha)
            assert alpha_size == (17, 54), alpha_size
            # Ctrl+Enter is discarded; following ordinary input still reaches this pane.
            for key in [b"\x1b[13;5u", b"\x1b[27;5;13~", b"\x1b[13;5:3u"]:
                os.write(tty_fd, key + b"ignored-tail")
                expected += b"ignored-tail"
                until(lambda: received(harness, alpha) == expected)
            assert latest_size(harness, alpha) == alpha_size
            os.write(tty_fd, b"\x1b")
            wait_view(tty_fd, terminal, lambda text: "Memory" in text and "Control" not in text)
            os.write(tty_fd, b"\x1b[13;5u")
            time.sleep(0.1)
            os.write(tty_fd, b"\r")
            wait_view(tty_fd, terminal, lambda text: "alpha [" in text and "Control" not in text)
            os.write(tty_fd, b"fullscreen")
            expected += b"fullscreen"
            until(lambda: received(harness, alpha) == expected)
            os.write(tty_fd, b"\x1b[24~")
            wait_view(tty_fd, terminal, lambda text: "Memory" in text)
            os.write(tty_fd, b"v")
            wait_view(tty_fd, terminal, lambda text: "READY-alpha" in text and "Control" in text)
            for byte in b"\x1b[24~":
                os.write(tty_fd, bytes([byte]))
                time.sleep(0.005)
            wait_view(tty_fd, terminal, lambda text: "Memory" in text and "Control" not in text)
            os.write(tty_fd, b"v")
            wait_view(tty_fd, terminal, lambda text: "Control" in text and "READY-alpha" in text)
            os.write(tty_fd, b"\x11")
            wait_view(tty_fd, terminal, lambda text: "Memory" in text and "Control" not in text)
            assert received(harness, alpha) == expected
            os.write(tty_fd, b"q")
            assert harness.join(pid) == 0
            for task_id, original in before.items():
                assert harness.show(task_id)["runtime"] == original
            if shutil.which("vim"):
                path = pathlib.Path(root) / "pane.txt"
                task_id = harness.create(
                    "vim-control", shutil.which("vim"), "-u", "NONE", "-n", str(path)
                )
                pid, tty_fd = harness.spawn_tty()
                terminal = Terminal()
                wait_view(tty_fd, terminal, lambda text: "vim-control" in text and "Memory" in text)
                os.write(tty_fd, b"\x1b[B\x1b[Bv")
                wait_view(
                    tty_fd, terminal, lambda text: "vim-control" in text and "Control" in text
                )
                os.write(tty_fd, b"iHello " + "世界".encode())
                os.write(tty_fd, b"\x1b")
                wait_view(tty_fd, terminal, lambda text: "Memory" in text)
                os.write(tty_fd, b"\r")
                wait_view(
                    tty_fd, terminal, lambda text: "Hello 世界" in text and "Control" not in text
                )
                os.write(tty_fd, b"\x1b:w\r")
                until(lambda: path.exists() and path.read_text().strip() == "Hello 世界")
                os.write(tty_fd, b"\x1b[24~")
                wait_view(tty_fd, terminal, lambda text: "Memory" in text)
                os.write(tty_fd, b"q")
                assert harness.join(pid) == 0
            print("PASS monitor input, navigation, detach keys and Vim editing")
        finally:
            harness.cleanup()


if __name__ == "__main__":
    main()
