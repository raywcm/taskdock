#!/usr/bin/env python3
"""Real-PTY detach keys, input preservation and TUI return; temporary state only."""
import json
import os
import pathlib
import re
import sys
import tempfile
import time
from integration import Harness, drain, until, wait_output
from screen_regression import Terminal
from ui_regression import wait_view

FIXTURE = r'''
import os,tty
tty.setraw(0)
os.write(1,b'HOTKEY-READY\n')
while True:
    data=os.read(0,4096)
    if not data: break
    os.write(1,b'INPUT:'+data.hex().encode()+b'\n')
'''


def received(h, ref):
    return b''.join(bytes.fromhex(part.decode()) for part in re.findall(rb'INPUT:([0-9a-f]+)', h.cli('logs', ref, '--raw').stdout))


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-detach-keys-') as root:
        h = Harness(root, binary)
        try:
            h.cli('settings', 'set', 'language', 'en')
            h.cli('settings', 'set', 'history-retention', 'off')
            ref = h.create('hotkey-work', sys.executable, '-c', FIXTURE)
            until(lambda: b'HOTKEY-READY' in h.cli('logs', ref).stdout)
            original = h.show(ref)['runtime']
            keys = [b'\x11', b'\x1b[24~', b'\x1b[113;5u', b'\x1b[27;5;113~', b'\x1b[24;1:1~']
            for options in [(), ('--no-status',)]:
                for key in keys:
                    pid, fd = h.spawn_tty('attach', ref, *options)
                    wait_output(fd, b'hotkey-work [')
                    before = received(h, ref)
                    os.write(fd, key)
                    assert h.join(pid) == 0
                    assert received(h, ref) == before, 'detach bytes reached application'
                    assert h.show(ref)['runtime'] == original
            pid, fd = h.spawn_tty('attach', ref)
            wait_output(fd, b'hotkey-work [')
            before = received(h, ref)
            for byte in b'\x1b[24~':
                os.write(fd, bytes([byte])); time.sleep(.005)
            assert h.join(pid) == 0 and received(h, ref) == before

            # Removed shortcuts, Ctrl+D, Esc, modified keys and pasted shortcuts remain input.
            pid, fd = h.spawn_tty('attach', ref)
            wait_output(fd, b'hotkey-work [')
            expected = received(h, ref)
            os.write(fd, b'\x1b')
            expected += b'\x1b'
            until(lambda: received(h, ref) == expected)
            payload = b'[24~\x02d\x02\x04\x1dd\x1b[98;5ud\x1b[113;5:3u\x04\x1b[100;5u\x1b[24;2~\x1b[24;1:3~\x1b[<0;10;12M' + '中文🙂'.encode() + b'\x1b[200~\x11\x1b[113;5u\x1b[24~\x02d\x02\x04\x1dd\x1b[201~'
            os.write(fd, payload); expected += payload
            until(lambda: received(h, ref) == expected)
            os.write(fd, b'\x1b[24~'); assert h.join(pid) == 0
            assert h.show(ref)['runtime'] == original

            # Read-only recognizes Ctrl+Q locally without forwarding input.
            pid, fd = h.spawn_tty('attach', ref, '--read-only')
            wait_output(fd, b'hotkey-work [')
            before = received(h, ref)
            os.write(fd, b'no-input\x04\x11')
            assert h.join(pid) == 0 and received(h, ref) == before

            # Each primary shortcut returns to the same selection in the TUI.
            pid, fd = h.spawn_tty(); terminal = Terminal()
            wait_view(fd, terminal, lambda text: 'hotkey-work' in text and 'Running' in text)
            for key in keys[:3]:
                os.write(fd, b'\r')
                # The main buffer retains the previous attachment's banner.
                # Wait for fresh bytes, after the new client enabled raw mode.
                terminal.feed(wait_output(fd, b'hotkey-work ['))
                os.write(fd, key)
                wait_view(fd, terminal, lambda text: 'Running' in text and 'hotkey-work' in text and 'Enter v' in text)
                assert h.show(ref)['runtime'] == original
            os.write(fd, b'q'); assert h.join(pid) == 0

            # No-escape forwards every shortcut, and the application ends normally.
            payload = b'\x11\x1b[113;5u\x1b[27;5;113~\x1b[24~\x02d\x02\x04\x1dd\x04'
            code = 'import os,tty;tty.setraw(0);os.write(1,b"BYPASS-READY\\n");data=b""\nwhile len(data)<%d:data+=os.read(0,%d-len(data))\nos.write(1,b"BYPASS:"+data.hex().encode()+b"\\n")' % (len(payload), len(payload))
            bypass = h.create('bypass', sys.executable, '-c', code)
            until(lambda: b'BYPASS-READY' in h.cli('logs', bypass).stdout)
            pid, fd = h.spawn_tty('attach', bypass, '--no-escape')
            wait_output(fd, b'bypass ['); os.write(fd, payload)
            until(lambda: b'BYPASS:' + payload.hex().encode() in h.cli('logs', bypass, '--raw').stdout)
            assert h.join(pid) == 0

            # In a canonical shell Ctrl+D still causes EOF, rather than detaching.
            eof = h.create('eof', '/bin/sh', '-c', 'printf EOF-READY; read value; result=$?; printf "EOF-STATUS:%s\\n" "$result"; exit "$result"')
            until(lambda: b'EOF-READY' in h.cli('logs', eof).stdout)
            pid, fd = h.spawn_tty('attach', eof)
            wait_output(fd, b'eof ['); os.write(fd, b'\x04')
            wait_output(fd, b'EOF-STATUS:1'); assert h.join(pid) == 1
            assert h.finished(eof)['runtime']['exit_code'] == 1
            print('PASS Ctrl+Q/F12 and CSI-u detach in native/status views, TUI return, removed shortcuts forwarded, paste/Unicode/mouse preservation, read-only and no-escape', flush=True)
        finally:
            h.cleanup()


if __name__ == '__main__':
    main()
