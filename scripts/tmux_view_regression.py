#!/usr/bin/env python3
"""Exercise the TaskDock browser against an isolated, real tmux server."""
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

from integration import ENV, Harness, until, wait_output


def main():
    if not shutil.which('tmux'):
        print('SKIP real tmux server test (tmux is not installed)')
        return
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-tmux-view-') as tmp:
        root = pathlib.Path(tmp)
        sockets = root / 'sockets'
        sockets.mkdir(mode=0o700)
        env = dict(ENV, TMUX_TMPDIR=str(sockets))
        env.pop('TMUX', None)
        h = Harness(root, binary)

        def native(*args):
            return subprocess.check_output(['tmux', *args], env=env, timeout=6)

        old_env = ENV.copy()
        ENV.clear()
        ENV.update(env)
        created = False
        try:
            assert json.loads(h.cli('tmux', 'ls', '--json').stdout) == []
            native('-f', '/dev/null', 'new-session', '-ds', 'real-work', '/bin/sh')
            created = True
            native('send-keys', '-t', 'real-work', 'echo READ_ONLY_MARKER', 'Enter')
            listed = json.loads(h.cli('tmux', '-L', 'default', 'ls', '--json').stdout)
            assert [s['name'] for s in listed] == ['real-work'], listed
            target = listed[0]['id']
            socket = native('display-message', '-p', '#{socket_path}').decode().strip()
            until(lambda: b'READ_ONLY_MARKER' in h.cli('tmux', '-S', socket, 'capture', '-t', target).stdout)
            assert h.cli('tmux', 'capture', 'real', check=False).returncode != 0

            pid, fd = h.spawn_tty()
            wait_output(fd, b'TaskDock')
            os.write(fd, b't')
            wait_output(fd, b'real-work')
            os.write(fd, b'\r')
            wait_output(fd, b'READ_ONLY_MARKER')
            native('send-keys', '-t', target, 'echo LIVE_PANE_UPDATE', 'Enter')
            wait_output(fd, b'LIVE_PANE_UPDATE')
            os.write(fd, b'\x1b')
            wait_output(fd, b'real-work')
            os.write(fd, b'\x1b')
            wait_output(fd, b'TaskDock')
            os.write(fd, b'q')
            assert h.join(pid) == 0

            pid, fd = h.spawn_tty('tmux', 'view', '-t', 'real-work')
            wait_output(fd, b'LIVE_PANE_UPDATE')
            assert native('list-clients', '-F', '#{client_readonly}').strip() == b'1'
            os.write(fd, b'\x02d')
            assert h.join(pid) == 0
            assert native('has-session', '-t', target) == b''
        finally:
            h.cleanup()
            if created:
                native('kill-server')
            ENV.clear()
            ENV.update(old_env)
    print('PASS real tmux discovery, socket selection, exact targets, live TUI preview and read-only attachment')


if __name__ == '__main__':
    main()
