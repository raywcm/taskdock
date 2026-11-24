#!/usr/bin/env python3
"""Verify TUI exits preserve the shell and running tasks; isolated state only."""
import fcntl
import os
import pathlib
import signal
import struct
import sys
import tempfile
import termios

from integration import Harness, drain, until
from screen_regression import Terminal
from ui_regression import wait_view


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-tui-exit-') as root:
        h = Harness(root, binary)
        try:
            h.cli('settings', 'set', 'language', 'en')
            live = h.create('exit-live', '/bin/sh', '-c', 'printf LIVE; read value')
            stopped = h.create('exit-history', '/bin/sh', '-c', 'printf HISTORY')
            h.finished(stopped)
            before = h.show(live)['runtime']
            for page in ('list', 'settings', 'dialog', 'monitor', 'tmux'):
                for method in ('ctrl-c', 'SIGINT', 'SIGTERM', 'SIGHUP'):
                    terminal = Terminal()
                    terminal.feed(b'OUTER-SHELL-KEEP\r\nPROMPT> ')
                    shell = list(terminal.main.display)
                    pid, fd = h.spawn_tty()
                    wait_view(fd, terminal, lambda text: 'exit-live' in text)
                    if page == 'settings':
                        os.write(fd, b',')
                        wait_view(fd, terminal, lambda text: 'Language' in text)
                    elif page == 'dialog':
                        os.write(fd, b's')
                        wait_view(fd, terminal, lambda text: 'Stop this session' in text)
                    elif page == 'monitor':
                        os.write(fd, b'v')
                        wait_view(fd, terminal, lambda text: 'LIVE' in text)
                    elif page == 'tmux':
                        os.write(fd, b'\t')
                        wait_view(fd, terminal, lambda text: 'Tmux' in text)
                    if method == 'ctrl-c':
                        if page == 'monitor':
                            os.write(fd, b'\x1b[24~')
                            wait_view(fd, terminal, lambda text: 'Memory' in text and 'LIVE' not in text)
                        os.write(fd, b'\x03')
                    else:
                        os.kill(pid, getattr(signal, method))
                    wait_view(fd, terminal, lambda text: not terminal.alternate)
                    terminal.feed(drain(fd, .1))
                    settings = termios.tcgetattr(fd)
                    assert h.join(pid, on_output=terminal.feed) == 0, (page, method, 'interruption treated as failure')
                    assert not terminal.alternate, (page, method, 'alternate screen retained')
                    assert terminal.main.display == shell, (page, method, terminal.main.display)
                    assert not terminal.main.cursor.hidden, (page, method, 'cursor left hidden')
                    assert settings[3] & termios.ICANON and settings[3] & termios.ECHO, (page, method, 'raw mode retained')
                    assert h.show(live)['runtime'] == before, 'exit changed the task runtime'
                    if page == 'monitor':
                        # Cleanup must release the display lease even when Run returns an error.
                        def released():
                            conn, kind, _ = h.socket(live)
                            conn.close()
                            return kind == 2
                        until(released)
            # Resize while the TUI is running, then quit without erasing shell history.
            terminal = Terminal(100, 30)
            terminal.feed(b'RESIZED-SHELL-KEEP\r\nPROMPT> ')
            shell = list(terminal.main.display)
            pid, fd = h.spawn_tty()
            fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 30, 100, 0, 0))
            os.kill(pid, signal.SIGWINCH)
            wait_view(fd, terminal, lambda text: 'exit-live' in text)
            os.write(fd, b'q')
            terminal.feed(drain(fd, .2))
            assert h.join(pid) == 0 and terminal.main.display == shell and not terminal.alternate
            print('PASS Ctrl+C/SIGINT/SIGTERM/SIGHUP across list/settings/dialog/monitor/history; shell cells, cursor and termios restored, task unchanged, monitor lease released; resized q exit', flush=True)
        finally:
            h.cleanup()


if __name__ == '__main__':
    main()
