#!/usr/bin/env python3
"""Isolated real PTYs: persisted ordering and cooperative multi-window control."""
import json,os,pathlib,re,signal,sys,tempfile,time
from integration import Harness,drain,until,wait_output
from screen_regression import Terminal
from ui_regression import wait_view
from monitor_control_regression import FIXTURE,received
from monitor_dialog_regression import latest_size,resize


def selected(t,name):
 for y,row in enumerate(t.display):
  if name not in row:continue
  x=row.index(name)
  if t.current.buffer[y][x].reverse:return True
 return False


def window(h,rows=24,cols=80):
 pid,fd=h.spawn_tty();t=Terminal()
 resize(pid,fd,t,rows,cols)
 wait_view(fd,t,lambda s:'alpha' in s and 'gamma' in s and 'Memory' in s)
 return pid,fd,t


def list_names(h):return [r['spec']['name'] for r in json.loads(h.cli('ls','--json').stdout)]


def ordering(h):
 p1,f1,t1=window(h);p2,f2,t2=window(h)
 assert all('v' in t.display[0] and '?' in t.display[0] for t in [t1,t2])
 os.write(f1,b'\x1b[1;2B')
 until(lambda:list_names(h)==['beta','alpha','gamma'])
 wait_view(f1,t1,lambda s:selected(t1,'alpha') and t1.display[6].find('alpha')>=0)
 wait_view(f2,t2,lambda s:selected(t2,'alpha') and t2.display[6].find('alpha')>=0)
 os.write(f2,b'\x1b[57352;2u')
 until(lambda:list_names(h)==['alpha','beta','gamma'])
 wait_view(f1,t1,lambda s:selected(t1,'alpha') and 'alpha' in t1.display[5])
 wait_view(f2,t2,lambda s:selected(t2,'alpha') and 'alpha' in t2.display[5])
 # Boundary moves remain harmless, and a fresh process sees the saved order.
 os.write(f1,b'\x1b[1;2A');time.sleep(.15)
 assert list_names(h)==['alpha','beta','gamma']
 for p,f in [(p1,f1),(p2,f2)]:os.write(f,b'q');assert h.join(p)==0
 p,f,t=window(h)
 assert selected(t,'alpha')
 os.write(f,b'\x1b[B\x1b[1;2B')
 until(lambda:list_names(h)==['alpha','gamma','beta'])
 os.write(f,b'q');assert h.join(p)==0
 print('PASS Shift+arrows persist order, cross-window synchronization keeps selection by ID, CSI-u, boundaries and fresh frontend order',flush=True)


def cooperation(h):
 p1,f1,t1=window(h);p2,f2,t2=window(h,30,120)
 original=h.show('alpha')['runtime']
 os.write(f1,b'v');wait_view(f1,t1,lambda s:'READY-alpha' in s and 'Control' in s)
 until(lambda:latest_size(h,'alpha')==(17,54))
 os.write(f2,b'v');wait_view(f2,t2,lambda s:'READY-alpha' in s and 'Control' in s)
 os.write(f1,b'from-one');until(lambda:received(h,'alpha')==b'from-one')
 os.write(f2,b'from-two');until(lambda:received(h,'alpha')==b'from-onefrom-two')
 wait_view(f1,t1,lambda s:'INPUT:' in s);wait_view(f2,t2,lambda s:'INPUT:' in s)
 expected=b'from-onefrom-two'
 for key,arrow in [(b'\x1b[1;5A',b'\x1b[1;5A'),(b'\x1b[1;2A',b'\x1b[A'),(b'\x1b[1;2B',b'\x1b[B'),(b'\x1b[57352;2:2u',b'\x1b[A'),(b'\x1b[57353;2:3u',b'')]:
  os.write(f2,key+b'next');expected+=arrow+b'next';until(lambda:received(h,'alpha')==expected)
 paste=b'\x1b[200~\x1b[1;2A\x1b[201~'
 os.write(f2,paste);expected+=paste;until(lambda:received(h,'alpha')==expected)
 assert selected(t2,'alpha'),'Shift arrows moved the list selection'
 assert 'Shift+' in t1.display[0] and 'Ctrl+Q/F12' in t1.display[0]
 offset=len(h.cli('logs','alpha','--raw').stdout)
 for rows,cols in [(32,100),(28,110),(30,120)]:
  resize(p2,f2,t2,rows,cols)
  wait_view(f2,t2,lambda s:'TaskDock' in t2.display[0] and 'Shift+' in t2.display[0] and 'Control' in s)
 assert latest_size(h,'alpha')==(17,54),'a follower resized the owner PTY'
 assert b'SIZE:' not in h.cli('logs','alpha','--raw').stdout[offset:],'follower geometry bounced the PTY'
 # Closing the owner promotes the other monitor and applies its pane once.
 os.write(f1,b'\x1b[24~');wait_view(f1,t1,lambda s:'Memory' in s and 'Control' not in s)
 until(lambda:latest_size(h,'alpha')==(23,85))
 os.write(f2,b'promoted');expected+=b'promoted';until(lambda:received(h,'alpha')==expected)
 # Ordinary Enter joins shared control while a monitor controls the same task.
 os.write(f1,b'\r');t1.feed(wait_output(f1,b'Shared control'))
 os.write(f1,b'from-native');expected+=b'from-native';until(lambda:received(h,'alpha')==expected)
 ro,rf=h.spawn_tty('attach','alpha','--read-only');rt=Terminal()
 wait_view(rf,rt,lambda s:'Read only' in s)
 os.write(rf,b'FORBIDDEN');time.sleep(.15)
 assert b'FORBIDDEN' not in received(h,'alpha')
 takeover,tf=h.spawn_tty('attach','alpha','--takeover');tt=Terminal()
 wait_view(tf,tt,lambda s:'Take over' in s)
 wait_view(f2,t2,lambda s:'taken over in another terminal' in s and 'Memory' in s)
 wait_view(f1,t1,lambda s:'taken over in another terminal' in s and 'Memory' in s)
 os.write(tf,b'new-owner');expected+=b'new-owner';until(lambda:received(h,'alpha')==expected)
 wait_view(rf,rt,lambda s:b'new-owner'.hex() in s)
 os.write(tf,b'\x1b[24~');assert h.join(takeover)==0
 os.write(rf,b'\x1b[24~');assert h.join(ro)==0
 for p,f in [(p1,f1),(p2,f2)]:os.write(f,b'q');assert h.join(p)==0
 now=h.show('alpha')['runtime']
 for field in ['instance_id','broker_pid','child_pid']:assert now[field]==original[field]
 print('PASS two different-size monitors share viewing/input, Shift arrows/paste, stable owner size, automatic promotion, ordinary Enter shared, readonly isolation and explicit takeover with readers retained; PIDs unchanged',flush=True)


def main():
 binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
 broker=pathlib.Path(sys.argv[2]).resolve() if len(sys.argv)>2 else binary
 with tempfile.TemporaryDirectory(prefix='tdock-multi-') as root:
  h=Harness(root,broker)
  try:
   h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
   for name in ['alpha','beta','gamma']:h.create(name,sys.executable,'-c',FIXTURE,name)
   h.binary=binary
   ordering(h);cooperation(h)
  finally:h.cleanup()
if __name__=='__main__':main()
