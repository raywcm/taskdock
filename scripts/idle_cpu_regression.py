#!/usr/bin/env python3
"""Detect idle terminal busy loops using real PTYs and per-process CPU time."""
import os
import pathlib
import subprocess
import sys
import tempfile
import time

from integration import Harness, drain
from monitor_control_regression import FIXTURE
from screen_regression import Terminal
from ui_regression import wait_view


def cpu(pid):
    if sys.platform.startswith('linux'):
        fields = pathlib.Path('/proc/%d/stat' % pid).read_text().rsplit(')', 1)[1].split()
        return (int(fields[11]) + int(fields[12])) / os.sysconf('SC_CLK_TCK')
    value = subprocess.check_output(['ps', '-p', str(pid), '-o', 'time='], text=True).strip()
    total = 0.0
    for part in value.split(':'):
        total = total * 60 + float(part)
    return total


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-idle-cpu-') as root:
        h = Harness(root, binary)
        try:
            h.cli('settings', 'set', 'language', 'en')
            ref = h.create('idle-work', sys.executable, '-c', FIXTURE, 'idle')
            runtime = h.show(ref)['runtime']
            for mode in ['attach', 'read-only', 'list', 'monitor']:
                args = ['attach', ref]
                if mode == 'read-only':
                    args += ['-r']
                elif mode in ['list', 'monitor']:
                    args = []
                pid, fd = h.spawn_tty(*args)
                terminal = Terminal()
                if mode in ['attach', 'read-only']:
                    wait_view(fd, terminal, lambda text: '[idle-work] 0:' in terminal.display[-1])
                else:
                    wait_view(fd, terminal, lambda text: 'Memory' in text and 'idle-work' in text)
                    if mode == 'monitor':
                        os.write(fd, b'v')
                        wait_view(fd, terminal, lambda text: 'Control' in text and 'READY-idle' in text)
                drain(fd, .15)
                before, start = cpu(pid), time.monotonic()
                while time.monotonic() - start < 2:
                    drain(fd, .05)
                seconds, elapsed = cpu(pid) - before, time.monotonic() - start
                percent = 100 * seconds / elapsed
                # A generous ceiling catches a hot core without relying on a
                # runner-specific sub-percent idle reading (including race builds).
                assert percent < 35, (mode, 'idle busy loop', percent)
                if mode == 'monitor':
                    os.write(fd, b'\x1b[24~')
                    wait_view(fd, terminal, lambda text: 'Memory' in text and 'Control' not in text)
                os.write(fd, b'\x1b[24~' if mode in ['attach', 'read-only'] else b'q')
                assert h.join(pid) == 0
                assert h.show(ref)['runtime'] == runtime
                print('PASS idle %s CPU %.2f%% (<35%% guard); task identity preserved' % (mode, percent), flush=True)
        finally:
            h.cleanup()


if __name__ == '__main__':
    main()
