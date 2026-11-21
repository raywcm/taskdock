#!/usr/bin/env python3
"""New-session monitor, long lists, search routing and Ctrl+Enter in real PTYs."""
import os
import pathlib
import sys
import tempfile

from integration import ENV, Harness, until
from monitor_control_regression import FIXTURE, received
from monitor_dialog_regression import latest_size
from screen_regression import Terminal
from ui_regression import wait_view


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    saved_env = dict(ENV)
    # Exercise the manager with a predictable interactive shell, independent of
    # host startup files that can discard pre-prompt typeahead.
    ENV['SHELL'] = '/bin/sh'
    ENV.pop('ENV', None)
    with tempfile.TemporaryDirectory(prefix='tdock-search-') as root:
        h = Harness(root, binary)
        try:
            h.cli('settings', 'set', 'language', 'en')
            h.cli('settings', 'set', 'history-retention', 'off')
            refs = [h.create('work-%02d' % i, sys.executable, '-c', FIXTURE, str(i)) for i in range(24)]
            old = h.create('archived', '/bin/sh', '-c', 'printf SEARCH-HISTORY')
            h.finished(old)
            before = {ref: h.show(ref)['runtime'] for ref in refs}
            pid, fd = h.spawn_tty()
            t = Terminal()
            wait_view(fd, t, lambda s: 'work-00' in s and 'Memory' in s)
            os.write(fd, b'\x1b[F')
            wait_view(fd, t, lambda s: 'work-23' in s and '24 / 24' in s)
            os.write(fd, b'n')
            wait_view(fd, t, lambda s: 'New shell name' in s)
            os.write(fd, b'added\r')
            wait_view(fd, t, lambda s: 'added [' in s and 'Control' in s and 'work-23' in s)
            assert t.alternate, 'new session opened fullscreen instead of the pane'
            created = h.show('added')
            until(lambda: h.cli('logs','added','--raw').stdout.rstrip().endswith((b'$',b'#')))
            os.write(fd, b"printf 'NEW-PANE-OK\\n'\r")
            try:
                until(lambda: b'NEW-PANE-OK\r\n' in h.cli('logs', 'added', '--raw').stdout)
            except AssertionError:
                raise AssertionError('new shell output: '+repr(h.cli('logs','added','--raw').stdout[-2000:]))
            # Ctrl+F and the query in one read must never reach the application.
            os.write(fd, b'\x06work-23')
            wait_view(fd, t, lambda s: 'Search sessions' in s and '1 / 1' in s)
            assert b'work-23' not in h.cli('logs', 'added', '--raw').stdout
            os.write(fd, b'\r')
            wait_view(fd, t, lambda s: 'work-23 [' in s and 'Control' in s)
            until(lambda: latest_size(h, refs[-1]) == (17, 54))
            os.write(fd, b'\rplain-enter\x1b[13;2u')
            expected = b'\rplain-enter\x1b[13;2u'
            until(lambda: received(h, refs[-1]) == expected)
            assert t.alternate
            for key in (b'\x1b[13;5u', b'\x1b[27;5;13~'):
                os.write(fd, key+b'ignored')
                expected += b'ignored'
                until(lambda: received(h, refs[-1]) == expected)
            assert latest_size(h, refs[-1]) == (17, 54)
            # A cancelled search resumes input on the same lease and at the same size.
            offset = len(h.cli('logs', refs[-1], '--raw').stdout)
            os.write(fd, b'\x06missing')
            wait_view(fd, t, lambda s: 'No matching sessions' in s)
            os.write(fd, b'\x1b')
            wait_view(fd, t, lambda s: 'work-23 [' in s and 'Control' in s)
            os.write(fd, b'resumed')
            expected += b'resumed'
            until(lambda: received(h, refs[-1]) == expected)
            assert b'SIZE:' not in h.cli('logs', refs[-1], '--raw').stdout[offset:]
            # Search includes stopped sessions and opens retained history.
            os.write(fd, b'\x06' + old[-8:].encode() + b'\r')
            wait_view(fd, t, lambda s: 'Output history' in s and 'SEARCH-HISTORY' in s)
            assert h.show(old)['runtime']['state'] == 'exited'
            os.write(fd, b'\x1b')
            wait_view(fd, t, lambda s: 'archived' in s and 'Duration' in s)
            # Search from the list follows list Enter semantics and attaches fullscreen.
            os.write(fd, b'\x06WORK-00\r')
            wait_view(fd, t, lambda s: '[work-00] 0:' in t.display[-1] and 'Control' not in s)
            os.write(fd, b'\x1b[24~')
            wait_view(fd, t, lambda s: 'Memory' in s)
            os.write(fd, b'v')
            wait_view(fd, t, lambda s: 'work-00 [' in s and 'Control' in s)
            os.write(fd, b'\x1b[200~\x06\x1b[13;5u\x1b[201~')
            until(lambda: received(h, refs[0]) == b'\x1b[200~\x06\x1b[13;5u\x1b[201~')
            os.write(fd, b'\x1b[24~')
            wait_view(fd, t, lambda s: 'Memory' in s and 'Control' not in s)
            os.write(fd, b'q')
            assert h.join(pid) == 0
            for ref, runtime in before.items():
                assert h.show(ref)['runtime'] == runtime
            assert h.show('added')['runtime']['child_pid'] == created['runtime']['child_pid']
            print('PASS new selects monitor control, 25-session scrolling, global name/ID/case search, same-read query, no-match/cancel/history, ignored Ctrl+Enter encodings, literal Enter/Shift+Enter/paste, no resize on search cancel and preserved PIDs', flush=True)
        finally:
            h.cleanup()
            ENV.clear(); ENV.update(saved_env)


if __name__ == '__main__':
    main()
