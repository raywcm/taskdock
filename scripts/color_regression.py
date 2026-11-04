#!/usr/bin/env python3
"""Compare app colors in embedded previews and native reconnects on real PTYs."""
import os,pathlib,sys,tempfile
from integration import ENV,Harness,until
from screen_regression import Terminal
from ui_regression import wait_view

FIXTURE=r'''
import os,signal,sys,tty
tty.setraw(0)
style=bytes.fromhex(sys.argv[1]);name=sys.argv[2]
if sys.argv[3]=='alternate':os.write(1,b'\x1b[?1049h')
os.write(1,style)
# Like a cached renderer, repaint cells without repeating unchanged rendition.
def paint(*args):os.write(1,b'\x1b[H\x1b[2J'+('COLOR-'+name).encode())
signal.signal(signal.SIGWINCH,paint);paint()
while os.read(0,1024):paint()
'''

def signature(cell):
 return cell.fg,cell.bg,cell.bold,cell.italics,cell.reverse,cell.underscore

def marker_style(terminal,marker):
 for y,line in enumerate(terminal.display):
  if marker in line:return signature(terminal.current.buffer[y][line.index(marker)])
 raise AssertionError('missing color marker')

def main():
 binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
 saved=dict(ENV)
 ENV.pop('NO_COLOR',None);ENV.pop('COLORFGBG',None);ENV['COLORTERM']='truecolor'
 with tempfile.TemporaryDirectory(prefix='tdock-colors-') as root:
  h=Harness(root,binary)
  try:
   h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
   styles=[('rgb',b'\x1b[1;3;38;2;229;237;247;48;2;20;26;38m'),('indexed',b'\x1b[38;5;202;48;5;17m'),('inverse',b'\x1b[0;7m'),('faint',b'\x1b[0;2;36m')]
   for name,style in styles:
    for mode in ['alternate','inline']:
     task=name+'-'+mode;marker='COLOR-'+task
     ref=h.create(task,sys.executable,'-c',FIXTURE,style.hex(),task,mode)
     until(lambda:marker.encode() in h.cli('logs',ref).stdout)
     original=h.show(ref)['runtime']
     reference=Terminal();reference.feed(b'\x1b[0m'+style+marker.encode())
     expected=marker_style(reference,marker)
     # Deliberately use opposite manager colors. App colors must stay its own.
     for theme in ['light','dark']:
      h.cli('settings','set','theme',theme)
      pid,fd=h.spawn_tty();t=Terminal()
      wait_view(fd,t,lambda s:task in s and 'Memory' in s)
      os.write(fd,b'v');wait_view(fd,t,lambda s:marker in s and 'Control' in s)
      assert marker_style(t,marker)==expected,('preview',task,theme,marker_style(t,marker),expected)
      os.write(fd,b'\x1b');wait_view(fd,t,lambda s:'Memory' in s and 'Control' not in s)
      os.write(fd,b'\r');wait_view(fd,t,lambda s:marker in s and 'Control' not in s)
      assert marker_style(t,marker)==expected,('native reconnect',task,theme,marker_style(t,marker),expected)
      os.write(fd,b'\x1b[24~');wait_view(fd,t,lambda s:'Memory' in s)
      os.write(fd,b'q');assert h.join(pid)==0
     assert h.show(ref)['runtime']==original,'color restoration restarted task'
     h.cli('stop',ref)
   print('PASS RGB/indexed/default inverse/faint colors across preview and native reconnects, inline/alternate apps, Light/Dark manager themes; task processes preserved')
  finally:h.cleanup();ENV.clear();ENV.update(saved)
if __name__=='__main__':main()
