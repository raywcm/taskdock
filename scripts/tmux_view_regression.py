#!/usr/bin/env python3
"""Exercise the TaskDock browser against an isolated, real tmux server."""
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

from integration import ENV, Harness, drain, until, wait_output


def main():
    if not shutil.which('tmux'):
        print('SKIP real tmux server test (tmux is not installed)')
        return
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    # Keep the tmux socket within the Unix path limit on macOS as well as Linux.
    with tempfile.TemporaryDirectory(dir='/tmp', prefix='tdock-tmux-view-') as tmp:
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
            native('new-session', '-ds', 'unrelated', '/bin/sh')
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
            client = native('list-clients', '-F', '#{session_id} #{client_readonly}').decode().strip()
            assert client == target + ' 1', client
            os.write(fd, b'echo THIS_INPUT_MUST_STAY_PRIVATE\r')
            # Separate typed input from the detach binding across tmux's paste window.
            drain(fd, .2)
            os.write(fd, b'\x02d')
            assert h.join(pid) == 0
            assert native('has-session', '-t', target) == b''
            assert b'THIS_INPUT_MUST_STAY_PRIVATE' not in native('capture-pane', '-p', '-t', target + ':')

            pid, fd = h.spawn_tty()
            wait_output(fd, b'TaskDock')
            os.write(fd, b't')
            wait_output(fd, b'real-work')
            os.write(fd, b'\r')
            wait_output(fd, b'LIVE_PANE_UPDATE')
            geometry = '#{pane_id} #{pane_pid} #{pane_width} #{pane_height}'
            before = native('display-message', '-p', '-t', target + ':', geometry)
            native('rename-session', '-t', target, 'UPDATED live')
            wait_output(fd, b'UPDATED live')
            assert native('display-message', '-p', '-t', target + ':', geometry) == before
            assert h.cli('tmux', 'capture', 'UPDATED live').stdout == native('capture-pane', '-p', '-t', target + ':')
            native('kill-session', '-t', target)
            wait_output(fd, b'unrelated')
            native('send-keys', '-t', 'unrelated', 'echo REMAINING_PANE', 'Enter')
            os.write(fd, b'\r')
            wait_output(fd, b'REMAINING_PANE')
            os.write(fd, b'\x03')
            assert h.join(pid) == 0
        finally:
            h.cleanup()
            if created:
                native('kill-server')
            ENV.clear()
            ENV.update(old_env)
    print('PASS real tmux discovery, socket selection, exact targets, live rename, pane identity/size and read-only attachment')


if __name__ == '__main__':
    main()
