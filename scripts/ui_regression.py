#!/usr/bin/env python3
"""Exercise TaskDock's visible tabs, selection, metrics and persistent settings.

Test dependency: scripts/requirements-screen.txt. Every task belongs to temporary state.
"""
import json
import os
import pathlib
import re
import sys
import tempfile
import time

from integration import Harness,drain
from screen_regression import Terminal


def wait_view(fd,terminal,predicate,timeout=6):
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        terminal.feed(drain(fd,.05))
        text='\n'.join(terminal.display)
        if predicate(text):return text
    raise AssertionError('UI did not reach expected state:\n'+'\n'.join(terminal.display))


def main():
    binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-ui-test-') as root:
        h=Harness(root,binary)
        try:
            h.cli('settings','set','language','en')
            h.cli('settings','set','refresh','1')
            live=h.create('live-work','/bin/sh','-c','(while :; do :; done) & wait')
            done=h.create('completed-work','/bin/sh','-c','exit 0');h.finished(done)
            pid,fd=h.spawn_tty();terminal=Terminal()
            text=wait_view(fd,terminal,lambda text:'live-work' in text and 'Memory' in text)
            version=h.cli('--version').stdout.decode().strip().split()[-1]
            assert 'TaskDock' in text and version not in text
            assert live[:8] in text and live not in text
            assert 'completed-work' not in text
            shortcut_rows=[row for row in terminal.display if all(key in row for key in ('TaskDock','Enter','v','?','q'))]
            assert len(shortcut_rows)==1 and shortcut_rows[0]==terminal.display[0], 'shortcuts must share the brand row'
            selected=next(i for i,row in enumerate(terminal.display) if 'live-work' in row)
            row=terminal.display[selected].strip()
            assert row.startswith('│') and row.endswith('│') and '┌' not in text and '┘' not in text, 'selection must occupy one terminal row'
            x=terminal.display[selected].index('live-work')
            assert terminal.current.buffer[selected][x].reverse,'selected session has no inverse styling'
            wait_view(fd,terminal,lambda text:bool(re.search(r'[1-9]\d*(?:\.\d+)?%',text)) and 'MiB' in text)
            os.write(fd,b'\x1b[C')
            text=wait_view(fd,terminal,lambda text:'completed-work' in text)
            assert 'live-work' not in text
            os.write(fd,b'\r')
            wait_view(fd,terminal,lambda text:'Output history' in text and 'No retained output' in text)
            os.write(fd,b'\x1b')
            wait_view(fd,terminal,lambda text:'Output history' not in text and 'completed-work' in text)
            os.write(fd,b'\x1b[Z') # Shift+Tab
            wait_view(fd,terminal,lambda text:'live-work' in text and 'completed-work' not in text)
            os.write(fd,b'\x1bOP') # F1
            wait_view(fd,terminal,lambda text:'Keyboard shortcuts' in text and 'Ctrl+Q' in text and 'F2' in text)
            os.write(fd,b'\x1b')
            wait_view(fd,terminal,lambda text:'live-work' in text and 'Keyboard shortcuts' not in text)
            os.write(fd,b'\x1b[15~') # F5
            wait_view(fd,terminal,lambda text:'live-work' in text)
            os.write(fd,b'?')
            wait_view(fd,terminal,lambda text:'Keyboard shortcuts' in text)
            os.write(fd,b'?')
            wait_view(fd,terminal,lambda text:'live-work' in text and 'Keyboard shortcuts' not in text)
            os.write(fd,b'\x1b[C\x1b[C') # Running -> Stop -> Settings
            text=wait_view(fd,terminal,lambda text:'Language' in text and 'Default tab' in text and 'Enter edit' in text)
            assert version in text and version not in terminal.display[0], 'version belongs in settings'
            tab_row=next(i for i,row in enumerate(terminal.display) if all(label in row for label in ('Running','Stop','Settings')))
            tab_x=terminal.display[tab_row].index('Settings')
            assert terminal.current.buffer[tab_row][tab_x].reverse, 'settings tab was not selected'
            assert 'live-work' not in text and 'completed-work' not in text
            assert 'RSS' not in text and '100%' not in text and 'metrics_hint' not in text
            os.write(fd,b'\x1b[C') # Settings -> Running, without editing
            wait_view(fd,terminal,lambda text:'live-work' in text and 'Memory' in text)
            os.write(fd,b',') # Shortcut directly focuses settings editor
            wait_view(fd,terminal,lambda text:'Language' in text and 'Default tab' in text)
            os.write(fd,b'\x1b[D') # English -> simplified Chinese, preview before saving
            wait_view(fd,terminal,lambda text:'语言' in text and '设置' in text)
            os.write(fd,b'\r')
            wait_view(fd,terminal,lambda text:'设置已保存' in text and '运行中' in text)
            assert json.loads(h.cli('settings','--json').stdout)['language']=='zh-CN'
            assert 'live-work' not in '\n'.join(terminal.display), 'save unexpectedly left the settings tab'
            os.write(fd,b'\x1b[D') # Settings -> Stop
            wait_view(fd,terminal,lambda text:'completed-work' in text)
            os.write(fd,b'\x1b[D') # Stop -> Running
            wait_view(fd,terminal,lambda text:'live-work' in text)
            os.write(fd,b',');wait_view(fd,terminal,lambda text:'刷新间隔' in text)
            os.write(fd,b'\x1b[B\x1b[B\x1b[B\x1b[C\r')
            wait_view(fd,terminal,lambda text:'设置已保存' in text and '刷新间隔' in text)
            assert json.loads(h.cli('settings','--json').stdout)['show_resources'] is False
            os.write(fd,b'\t') # Settings -> Running
            wait_view(fd,terminal,lambda text:'live-work' in text and 'CPU' not in text)
            os.write(fd,b',');wait_view(fd,terminal,lambda text:'已停止自动删除' in text)
            os.write(fd,b'\x1b[A\x1b[D\r') # Wrap to retention, one hour -> thirty minutes, save
            wait_view(fd,terminal,lambda text:'设置已保存' in text and '30 分钟' in text)
            assert json.loads(h.cli('settings','--json').stdout)['history_retention_seconds']==1800
            os.write(fd,b'\t');wait_view(fd,terminal,lambda text:'live-work' in text)
            os.write(fd,b'q');drain(fd);assert h.join(pid)==0
            h.cli('settings','set','default-tab','stop')
            pid,fd=h.spawn_tty();terminal=Terminal()
            text=wait_view(fd,terminal,lambda text:'completed-work' in text and '已停止' in text)
            assert 'live-work' not in text
            os.write(fd,b'q');drain(fd);assert h.join(pid)==0
            for code,native,stopped,language_label in [
                ('ja','日本語','停止済み','言語'),
                ('es','Español','Detenidas','Idioma'),
                ('fr','Français','Arrêtées','Langue'),
            ]:
                h.cli('settings','set','language',code)
                pid,fd=h.spawn_tty();terminal=Terminal()
                wait_view(fd,terminal,lambda text:stopped in text and 'completed-work' in text)
                os.write(fd,b',')
                text=wait_view(fd,terminal,lambda text:language_label in text and native in text and 'completed-work' not in text)
                assert 'RSS' not in text and '100%' not in text
                os.write(fd,b'\x1b');terminal.feed(drain(fd,.15))
                os.write(fd,b'q');drain(fd);assert h.join(pid)==0
            # Confirm/cancel with real terminal rendering in every supported language.
            h.cli('settings','set','default-tab','running')
            for code,prompt,hint in [
                ('en','Stop this session','y confirm'),
                ('zh-CN','确定停止此会话','y 确认'),
                ('ja','このセッション','y 確定'),
                ('es','¿Detener esta sesión','y confirmar'),
                ('fr','Arrêter cette session','y confirmer'),
            ]:
                h.cli('settings','set','language',code)
                pid,fd=h.spawn_tty();terminal=Terminal()
                wait_view(fd,terminal,lambda text:'live-work' in text)
                os.write(fd,b's')
                text=wait_view(fd,terminal,lambda text:prompt in text and hint in text)
                assert 'live-work ['+live[:8]+']' in text
                assert h.show(live)['health']=='online'
                os.write(fd,b'\r') # Enter is deliberately safe: cancel, not stop.
                wait_view(fd,terminal,lambda text:prompt not in text and 'live-work' in text)
                assert h.show(live)['health']=='online'
                if code=='fr':
                    os.write(fd,b's')
                    wait_view(fd,terminal,lambda text:prompt in text)
                    os.write(fd,b'y');h.finished(live)
                    wait_view(fd,terminal,lambda text:'live-work' not in text)
                os.write(fd,b'q');drain(fd);assert h.join(pid)==0
            print('PASS TaskDock/version, single-line inverse frame, short ID, sampled child CPU/RSS, tab filtering, F1/? help, F5 refresh, three-tab navigation/settings editor, one-line shortcuts, five languages, no metrics explanation, saved retention policy and default tab')
            print('PASS five-language stop confirmation with name/ID, Enter cancellation protects process, explicit y stops')
        finally:h.cleanup()

if __name__=='__main__':main()
