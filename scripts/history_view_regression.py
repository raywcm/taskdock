#!/usr/bin/env python3
"""Real-terminal stopped-history viewing; temporary state only."""
import json
import os
import pathlib
import sys
import tempfile
from integration import Harness,drain
from screen_regression import Terminal
from ui_regression import wait_view


def main():
    binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-history-view-') as root:
        h=Harness(root,binary)
        try:
            h.cli('settings','set','history-retention','off')
            code="import os\nfor i in range(100): print('HISTORY-%03d 中文 %s'%(i,'x'*100))\nos.write(1,b'\\x1b]52;c;HISTORY-SECRET\\x07')\nraise SystemExit(7)"
            ref=h.create('history-work',sys.executable,'-c',code);h.finished(ref)
            empty=h.create('empty-work','/bin/sh','-c','exit 0');h.finished(empty)
            before=h.show(ref)['runtime']
            h.cli('settings','set','default-tab','stop')
            for language,title,empty_text in [
                ('en','Output history','No retained output'),
                ('zh-CN','输出历史','暂无保留的输出记录'),
                ('ja','出力履歴','保存された出力はありません'),
                ('es','Historial de salida','No hay salida guardada'),
                ('fr','Historique de sortie','Aucune sortie conservée'),
            ]:
                h.cli('settings','set','language',language)
                pid,fd=h.spawn_tty();terminal=Terminal()
                raw=bytearray();feed=terminal.feed
                def capture(data):raw.extend(data);feed(data)
                terminal.feed=capture
                wait_view(fd,terminal,lambda text:'history-work' in text and 'empty-work' in text)
                os.write(fd,b'\r')
                text=wait_view(fd,terminal,lambda text:title in text and 'HISTORY-099' in text)
                assert 'HISTORY-SECRET' not in text and b'\x1b]52;' not in raw
                os.write(fd,b'\x1b[H')
                wait_view(fd,terminal,lambda text:'HISTORY-000' in text and 'HISTORY-099' not in text)
                os.write(fd,b'\x1b[6~')
                wait_view(fd,terminal,lambda text:'HISTORY-019' in text and 'HISTORY-000' not in text)
                os.write(fd,b'\x1b[F')
                wait_view(fd,terminal,lambda text:'HISTORY-099' in text)
                os.write(fd,b'q')
                wait_view(fd,terminal,lambda text:title not in text and 'empty-work' in text)
                os.write(fd,b'\x1b[B\r')
                wait_view(fd,terminal,lambda text:title in text and empty_text in text)
                os.write(fd,b'\x1b');wait_view(fd,terminal,lambda text:title not in text and 'empty-work' in text)
                os.write(fd,b'q');drain(fd);assert h.join(pid)==0
                assert h.show(ref)['runtime']==before
            assert len(json.loads(h.cli('ls','--json').stdout))==2
            print('PASS five-language stopped Enter history, latest/first/page navigation, empty history, safe OSC stripping, return to History and no task restart or deletion',flush=True)
        finally:h.cleanup()

if __name__=='__main__':main()
