#!/usr/bin/env python3
"""Out-of-viewport historical CSI margins must not crash the owning TUI."""
import os,pathlib,sys,tempfile,termios
from integration import Harness,until
from screen_regression import Terminal
from ui_regression import wait_view
from monitor_dialog_regression import resize

FIXTURE=r'''
import os,signal,tty
name=__import__('sys').argv[1]
tty.setraw(0)
title=('\x1b]0;查看5090当前训练状态 | codex\a').encode()
def paint(*args):
 os.write(1,b'\x1b[1;999r\x1b[H\x1b[L\x1bM\x1b[?69h\x1b[1;999s\x1b[L\x1b[?69l\x1b[r\x1b[H\x1b[2J')
 os.write(1,('PREVIEW-ALIVE-'+name+'\r\n').encode()+title)
signal.signal(signal.SIGWINCH,paint)
paint()
while True:
 p=os.read(0,4096)
 if not p:break
 os.write(1,b'INPUT:'+p.hex().encode()+b'\r\n'+title)
'''

def main():
 binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
 with tempfile.TemporaryDirectory(prefix='tdock-parser-') as root:
  h=Harness(root,binary)
  try:
   h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
   bad=h.create('a-historical',sys.executable,'-c',FIXTURE,'historical')
   good=h.create('b-healthy',sys.executable,'-c',FIXTURE,'healthy')
   before={ref:h.show(ref)['runtime'] for ref in [bad,good]}
   p,f=h.spawn_tty();t=Terminal()
   wait_view(f,t,lambda s:'a-historical' in s and 'Memory' in s)
   for _ in range(3):
    os.write(f,b'v');wait_view(f,t,lambda s:'PREVIEW-ALIVE-historical' in s and 'Control' in s)
    os.write(f,b'\x1b[<64;5;5M')
    wait_view(f,t,lambda s:'INPUT:1b5b3c36343b353b354d' in s)
    assert 'codex' not in ''.join(t.display), 'Unicode title leaked into monitor during wheel redraw'
    for rows,cols in [(30,120),(24,80)]:
     resize(p,f,t,rows,cols)
     wait_view(f,t,lambda s:'TaskDock' in t.display[0] and 'PREVIEW-ALIVE-historical' in s)
    os.write(f,b'usable')
    until(lambda:b'INPUT:757361626c65' in h.cli('logs',bad,'--raw').stdout)
    os.write(f,b'\x1b[B');wait_view(f,t,lambda s:'PREVIEW-ALIVE-healthy' in s)
    os.write(f,b'\x1b[A');wait_view(f,t,lambda s:'PREVIEW-ALIVE-historical' in s)
    os.write(f,b'\x1b');wait_view(f,t,lambda s:'Memory' in s and 'Control' not in s)
   os.write(f,b'\x03');wait_view(f,t,lambda s:not t.alternate)
   settings=termios.tcgetattr(f)
   assert h.join(p,on_output=t.feed)==0
   assert settings[3]&termios.ICANON and settings[3]&termios.ECHO
   assert not t.alternate and not t.main.cursor.hidden
   for ref in [bad,good]:assert h.show(ref)['runtime']==before[ref]
   print('PASS Unicode titles during wheel redraw, oversized vertical/horizontal historical margins, insert/delete/reverse-index, repeated monitor entry, resize/switch/input, clean Ctrl+C termios/cursor and unchanged task PIDs')
  finally:h.cleanup()
if __name__=='__main__':main()
