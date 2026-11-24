#!/usr/bin/env python3
"""Verify an upgrade from mtask v0.5.2 without restarting its live broker."""
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile

from integration import ENV, Harness, drain, wait_output


def main():
    binary, legacy = map(lambda p: pathlib.Path(p).resolve(), sys.argv[1:3])
    saved = dict(ENV)
    with tempfile.TemporaryDirectory(prefix='tdock-upgrade-') as root:
        root = pathlib.Path(root)
        ENV.update(HOME=str(root), XDG_STATE_HOME=str(root/'data'))
        ENV.pop('TDOCK_STATE_DIR', None)
        ENV.pop('MTASK_STATE_DIR', None)
        h = Harness(root, legacy)
        h.state = root/'data/mtask'
        try:
            h.cli('settings', 'set', 'language', 'en')
            task = h.create('before-upgrade', '/bin/sh', '-c', 'printf LEGACY-READY; read value; printf "GOT:%s\\n" "$value"; read more')
            original = h.show(task)['runtime']
            digest = hashlib.sha256(str(h.state.resolve()).encode()).hexdigest()[:12]
            old_socket = pathlib.Path(f'/tmp/mtask-{os.getuid()}')/digest/(task+'.sock')
            assert old_socket.is_socket()
            h.binary = binary

            def default(*args):
                result = subprocess.run([str(binary), *args], env=ENV, capture_output=True, timeout=10)
                assert result.returncode == 0, result.stderr
                return result.stdout

            rows = json.loads(default('ls', '--json'))
            assert next(r for r in rows if r['spec']['id'] == task)['health'] == 'online'
            assert json.loads(default('settings', '--json'))['language'] == 'en'
            assert not (root/'data/taskdock').exists(), 'upgrade split the existing catalog'
            # Read-only attachment uses the old private socket and retains its lease safety.
            pid, fd = h.spawn_tty('attach', task, '--read-only')
            output = wait_output(fd, b'TaskDock')
            assert b'TaskDock' in output
            os.write(fd, b'NEVER-SEND\r\x1b[24~')
            assert h.join(pid) == 0
            assert b'NEVER-SEND' not in h.cli('logs', task, '--raw').stdout
            pid, fd = h.spawn_tty('attach', task)
            wait_output(fd, b'TaskDock')
            os.write(fd, b'upgrade-input\r')
            wait_output(fd, b'GOT:upgrade-input')
            os.write(fd, b'\x1b[24~'); drain(fd)
            assert h.join(pid) == 0
            after = h.show(task)['runtime']
            for field in ['instance_id', 'broker_pid', 'child_pid']:
                assert original[field] == after[field], ('live process changed', field)
            assert old_socket.is_socket(), 'old socket was moved or removed'
            created = json.loads(default('new', '-d', '--json', '-s', 'after-upgrade', '--', '/bin/sh', '-c', 'read value'))['spec']['id']
            assert (h.state/'tasks'/created/'spec.json').is_file()
            assert (pathlib.Path(f'/tmp/tdock-{os.getuid()}')/digest/(created+'.sock')).is_socket()
            h.cli('rename', task, 'renamed-after-upgrade')
            assert h.show('renamed-after-upgrade')['spec']['id'] == task
            h.cli('stop', task)
            assert b'GOT:upgrade-input' in h.cli('logs', task).stdout
            h.cli('delete', task)
            assert not old_socket.exists()
        finally:
            h.cleanup()
            ENV.clear(); ENV.update(saved)
    print('PASS rename upgrade: default state/settings discovery, old private socket, readonly/input/detach, unchanged process identities, mixed broker namespaces, history and cleanup')


if __name__ == '__main__':
    main()
