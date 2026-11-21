#!/usr/bin/env python3
"""Real terminal background replies: automatic palettes and monitor input isolation."""
import json,os,pathlib,sys,tempfile,time
from integration import ENV,Harness,until,wait_output
from screen_regression import Terminal
from ui_regression import wait_view
from monitor_control_regression import FIXTURE,received


def reply(mode):
    value = b'ffff' if mode == 'light' else b'0000'
    return b'\x1b]11;rgb:' + b'/'.join([value] * 3) + b'\a'


def check_status_theme(h, foreground, background='default', detected=None):
    pid, fd = h.spawn_tty('attach', 'theme-work')
    term = Terminal()
    if detected:
        term.feed(wait_output(fd, b'\x1b]11;?'))
        os.write(fd, reply(detected))
    def ready(_):
        line = term.display[-1]
        if 'theme-work' not in line or 'RSS' not in line:
            return False
        cell = term.current.buffer[term.rows-1][line.index('theme-work')]
        return (cell.fg, cell.bg) == (foreground, background)
    wait_view(fd, term, ready)
    if foreground == 'default':
        assert term.display[-1].startswith('┃ [theme-work] 0:')
        assert term.current.buffer[term.rows-1][2].bold
    assert all(cell.bg == background and not cell.reverse for cell in term.current.buffer[term.rows-1].values())
    os.write(fd, b'\x1b[24~')
    assert h.join(pid) == 0


def appearance(h):
    settings = json.loads(h.cli('settings', '--json').stdout)
    return settings['theme'], settings['color_mode']


def check_app_background_query(h):
    # Both the manager and the attached app can request OSC 11 on the same tty.
    query = b'\x1b]11;?'
    app = r"""
import os, tty
tty.setraw(0)
os.write(1, b'WAITING')
assert os.read(0, 1) == b'?'
os.write(1, b'\x1b]11;?\a')
data = bytearray()
while not data.endswith(b'\a'):
    data += os.read(0, 1)
os.write(1, b'APP-COLOR:' + data.hex().encode())
"""
    h.cli('settings', 'set', 'color-mode', 'auto')
    h.cli('settings', 'set', 'theme', 'classic')
    ref = h.create('palette-query', sys.executable, '-c', app)
    until(lambda: b'WAITING' in h.cli('logs', ref).stdout)
    pid, fd = h.spawn_tty('attach', ref)
    wait_output(fd, query)
    os.write(fd, reply('dark') + b'?')
    wait_output(fd, query)
    data = reply('light')
    os.write(fd, data)
    assert h.join(pid) == 0
    assert b'APP-COLOR:' + data.hex().encode() in h.cli('logs', ref).stdout


def check_palettes(h):
    palettes = [
        # Theme, mode, manager foreground/background, status foreground/background.
        ('classic', 'light', '006c70', 'f6f8fc', 'blue', 'white'),
        ('classic', 'dark', '70e1c8', '141a26', 'white', 'blue'),
        ('terminal', 'light', 'blue', 'default', 'default', 'default'),
        ('terminal', 'dark', 'brightcyan', 'default', 'default', 'default'),
    ]
    for theme, mode, accent, background, status_fg, status_bg in palettes:
        h.cli('settings', 'set', 'theme', theme)
        opposite = 'dark' if mode == 'light' else 'light'
        for preference, detected in [('auto', mode), (mode, opposite)]:
            h.cli('settings', 'set', 'color-mode', preference)
            pid, fd = h.spawn_tty()
            term = Terminal()
            term.feed(wait_output(fd, b'\x1b]11;?'))
            os.write(fd, reply(detected))
            wait_view(fd, term, lambda view: 'theme-work' in view and term.current.buffer[0][2].fg == accent)
            assert term.current.buffer[0][2].bg == background, (theme, preference, mode)
            if theme == 'terminal':
                assert term.display[3].strip() == '━' * (term.columns - 4)
                assert term.current.buffer[3][term.display[3].index('━')].fg == 'default'
            line = next(line for line in term.display if 'theme-work' in line)
            cell = term.current.buffer[term.display.index(line)][line.index('theme-work')]
            assert cell.reverse == (theme == 'classic') and ('›' in line) == (theme == 'terminal')
            os.write(fd, b'q')
            assert h.join(pid) == 0
            check_status_theme(h, status_fg, status_bg, detected if theme == 'classic' and preference == 'auto' else None)


def test_terminal_theme(h):
    h.cli('settings', 'set', 'theme', 'classic')
    h.cli('settings', 'set', 'color-mode', 'light')
    pid, fd = h.spawn_tty()
    term = Terminal()
    def selected(label):
        row = next(row for row in term.display if label in row)
        return term.current.buffer[term.display.index(row)][row.index(label)]

    wait_view(fd, term, lambda view: 'theme-work' in view)
    os.write(fd, b',\x1b[B\x1b[C')  # Settings, Theme, Classic -> Terminal
    wait_view(fd, term, lambda view: '› Theme' in view and '‹ Terminal ›' in view)
    assert selected('› Theme').fg == 'blue' and appearance(h) == ('classic', 'light')
    os.write(fd, b'\x1b[B\x1b[D')  # Color mode, Light -> Dark
    wait_view(fd, term, lambda view: '› Color mode' in view and '‹ Dark ›' in view)
    assert selected('› Color mode').fg == 'brightcyan' and appearance(h) == ('classic', 'light')
    os.write(fd, b'\x1b')
    wait_view(fd, term, lambda view: '‹ Classic ›' in view and '‹ Light ›' in view)
    h.cli('settings', 'set', 'refresh', '5')
    h.cli('settings', 'set', 'color-mode', 'dark')
    os.write(fd, b',\x1b[A\x1b[C\r')  # Save only the style, merging the external color change.
    until(lambda: appearance(h) == ('terminal', 'dark'))
    wait_view(fd, term, lambda view: '5 seconds' in view and '‹ Dark ›' in view)
    os.write(fd, b',\x1b[B\x1b[C\r')
    until(lambda: appearance(h) == ('terminal', 'light'))
    wait_view(fd, term, lambda view: '‹ Light ›' in view and '‹ Terminal ›' in view)
    os.write(fd, b'\x1b')
    wait_view(fd, term, lambda view: '› ● theme-work' in view)
    assert selected('theme-work').fg == 'blue'
    os.write(fd, b's')
    wait_view(fd, term, lambda view: '› [ Cancel ]' in view)
    os.write(fd, b'\x1b[C')
    wait_view(fd, term, lambda view: '› [ stop ]' in view)
    for row in term.current.buffer.values():
        assert all(not cell.reverse and cell.bg == 'default' for cell in row.values())
    os.write(fd, b'\x1b')
    wait_view(fd, term, lambda view: 'Confirm stop' not in view)
    os.write(fd, b'c')
    wait_view(fd, term, lambda view: '› Shared' in view)
    os.write(fd, b'\x1b')
    wait_view(fd, term, lambda view: '› ● theme-work' in view)
    os.write(fd, b'\x06')
    wait_view(fd, term, lambda view: '› theme-work [' in view)
    assert selected('> ').fg == 'default', 'search prompt has a fixed foreground'
    os.write(fd, b'\x1b')
    wait_view(fd, term, lambda view: '› ● theme-work' in view)
    os.write(fd, b'v')
    wait_view(fd, term, lambda view: '› ● theme-work' in view and 'READY-theme' in view)
    assert selected('› ● theme-work').fg == 'blue'
    os.write(fd, b'\x1b[24~')
    wait_view(fd, term, lambda view: 'Memory' in view)
    os.write(fd, b'q')
    assert h.join(pid) == 0

    check_status_theme(h, 'default')
    h.create('second', '/bin/sh', '-c', 'read hold')
    ENV['NO_COLOR'] = '1'
    pid, fd = h.spawn_tty()
    term = Terminal()
    wait_view(fd, term, lambda view: '› ● theme-work' in view and 'second' in view)
    os.write(fd, b'\x1b[B')
    wait_view(fd, term, lambda view: '› ● second' in view)
    assert selected('second').fg == selected('second').bg == 'default' and not selected('second').reverse
    os.write(fd, b'q')
    assert h.join(pid) == 0
    check_status_theme(h, 'default')


def main():
    binary = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else 'bin/tdock').resolve()
    saved = dict(ENV)
    ENV.pop('NO_COLOR', None)
    ENV.pop('COLORFGBG', None)
    ENV['COLORTERM'] = 'truecolor'
    with tempfile.TemporaryDirectory(prefix='tdock-theme-') as root:
        h = Harness(root, binary)
        try:
            h.cli('settings', 'set', 'language', 'en')
            h.cli('settings', 'set', 'history-retention', 'off')
            ref = h.create('theme-work', sys.executable, '-c', FIXTURE, 'theme')
            check_palettes(h)
            assert not received(h, ref), 'manager background replies reached the application'
            check_app_background_query(h)
            pid, fd = h.spawn_tty()
            term = Terminal()
            wait_view(fd, term, lambda view: 'theme-work' in view)
            os.write(fd, b'v')
            wait_view(fd, term, lambda view: 'READY-theme' in view and 'Control' in view)
            data = reply('light')
            os.write(fd, data[:7])
            time.sleep(.12)
            os.write(fd, data[7:] + b'safe-input')
            until(lambda: received(h, ref) == b'safe-input')
            os.write(fd, b'\x1b[24~')
            wait_view(fd, term, lambda view: 'Memory' in view and 'Control' not in view)
            os.write(fd, b'q')
            assert h.join(pid) == 0
            test_terminal_theme(h)
            print('PASS Classic and Terminal on light/dark backgrounds, automatic/manual palettes and status rows, '
                  'independent edits and restart, isolated terminal replies, transparent dialogs and monochrome selection')
        finally:
            h.cleanup()
            ENV.clear()
            ENV.update(saved)


if __name__ == '__main__':
    main()
