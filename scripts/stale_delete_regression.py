#!/usr/bin/env python3
"""Delete dead records from CLI/TUI while retaining unreachable live brokers."""
import hashlib,os,pathlib,signal,sys,tempfile
from integration import Harness,until
from screen_regression import Terminal
from ui_regression import wait_view

def crash(h,ref):
 record=h.show(ref)
 os.kill(record['runtime']['broker_pid'],signal.SIGKILL)
 until(lambda:h.show(ref)['health']=='unreachable')

def main():
 binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
 with tempfile.TemporaryDirectory(prefix='tdock-stale-delete-') as root:
  h=Harness(root,binary)
  try:
   h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
   dead=h.create('dead-ui','/bin/sh','-c','read value');crash(h,dead)
   live=h.create('healthy','/bin/sh','-c','read value');original=h.show(live)['runtime']
   pid,fd=h.spawn_tty();t=Terminal();wait_view(fd,t,lambda s:'dead-ui' in s and 'Unreachable' in s)
   os.write(fd,b'\x1b[3~');wait_view(fd,t,lambda s:'Delete this session and its logs?' in s)
   os.write(fd,b'\r');wait_view(fd,t,lambda s:'Delete this session and its logs?' not in s)
   assert h.show(dead)['runtime']['state']=='running','default Cancel changed the stale record'
   os.write(fd,b'\x1b[3~');wait_view(fd,t,lambda s:'Delete this session and its logs?' in s)
   os.write(fd,b'y');wait_view(fd,t,lambda s:'dead-ui' not in s and 'healthy' in s)
   assert h.cli('show',dead,check=False).returncode!=0
   os.write(fd,b'q');assert h.join(pid)==0
   dead=h.create('dead-cli','/bin/sh','-c','read value');crash(h,dead)
   h.cli('delete',dead)
   assert h.cli('show',dead,check=False).returncode!=0
   # A broken socket alone does not prove a dead broker. Its lock still wins.
   digest=hashlib.sha256(str(h.state.resolve()).encode()).hexdigest()[:12]
   socket=pathlib.Path('/tmp/tdock-'+str(os.getuid()))/digest/(live+'.sock')
   hidden=socket.with_suffix('.hidden')
   socket.rename(hidden)
   try:
    assert h.show(live)['health']=='unreachable'
    assert h.cli('delete',live,check=False).returncode!=0,'deleted a living unreachable broker'
    assert (h.state/'tasks'/live/'spec.json').exists()
   finally:hidden.rename(socket)
   assert h.show(live)['health']=='online' and h.show(live)['runtime']==original
   print('PASS direct dead-record deletion in CLI/TUI, default Cancel and confirmation; unreachable live broker protected by its lock and original processes preserved')
  finally:h.cleanup()
if __name__=='__main__':main()
