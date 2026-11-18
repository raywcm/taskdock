#!/usr/bin/env python3
"""Retire dead sessions from the background worker while protecting live brokers."""
import hashlib,json,os,pathlib,signal,sys,tempfile
from integration import Harness,until
from screen_regression import Terminal
from ui_regression import wait_view

def crash(h,ref):
 record=h.show(ref)
 os.kill(record['runtime']['broker_pid'],signal.SIGKILL)

def main():
 binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
 with tempfile.TemporaryDirectory(prefix='tdock-stale-delete-') as root:
  h=Harness(root,binary)
  try:
   h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
   dead=h.create('dead-ui','/bin/sh','-c','echo retained-after-loss; read value')
   until(lambda:b'retained-after-loss' in h.cli('logs',dead).stdout)
   crash(h,dead)
   # Read the file directly so no frontend or CLI can trigger recovery.
   runtime=h.state/'tasks'/dead/'runtime.json'
   until(lambda:json.loads(runtime.read_text())['state']=='failed',seconds=8)
   live=h.create('healthy','/bin/sh','-c','read value');original=h.show(live)['runtime']
   pid,fd=h.spawn_tty();t=Terminal()
   wait_view(fd,t,lambda s:'healthy' in s and 'History 1' in s and 'dead-ui' not in s)
   os.write(fd,b'\x1b[C');wait_view(fd,t,lambda s:'dead-ui' in s and 'healthy' not in s)
   os.write(fd,b'\r');wait_view(fd,t,lambda s:'retained-after-loss' in s)
   os.write(fd,b'\x1b');wait_view(fd,t,lambda s:'History 1' in s and 'retained-after-loss' not in s)
   os.write(fd,b'\x1b[3~');wait_view(fd,t,lambda s:'Delete this session and its logs?' in s)
   os.write(fd,b'\r');wait_view(fd,t,lambda s:'Delete this session and its logs?' not in s)
   assert h.show(dead)['runtime']['state']=='failed','default Cancel changed the recovered record'
   os.write(fd,b'\x1b[3~');wait_view(fd,t,lambda s:'Delete this session and its logs?' in s)
   os.write(fd,b'y');wait_view(fd,t,lambda s:'No stopped sessions.' in s)
   assert h.cli('show',dead,check=False).returncode!=0
   os.write(fd,b'q');assert h.join(pid)==0
   dead=h.create('dead-cli','/bin/sh','-c','read value');crash(h,dead)
   until(lambda:h.cli('stop',dead,check=False).returncode==0)
   h.cli('delete',dead)
   assert h.cli('show',dead,check=False).returncode!=0
   # A broken socket alone does not prove a dead broker. Its lock still wins.
   digest=hashlib.sha256(str(h.state.resolve()).encode()).hexdigest()[:12]
   socket=pathlib.Path('/tmp/tdock-'+str(os.getuid()))/digest/(live+'.sock')
   hidden=socket.with_suffix('.hidden')
   socket.rename(hidden)
   try:
    assert h.show(live)['health']=='unreachable'
    assert h.cli('stop',live,check=False).returncode!=0,'stopped a living unreachable broker'
    assert h.cli('delete',live,check=False).returncode!=0,'deleted a living unreachable broker'
    assert (h.state/'tasks'/live/'spec.json').exists()
   finally:hidden.rename(socket)
   assert h.show(live)['health']=='online' and h.show(live)['runtime']==original
   print('PASS background recovery with retention off, manual stop, retained logs in History, deletion confirmation, and live broker protection')
  finally:h.cleanup()
if __name__=='__main__':main()
