#!/usr/bin/env python3
"""Session CLI compatibility, quoting, targets and destructive-operation scope."""
import concurrent.futures,json,os,pathlib,sys,tempfile
from integration import Harness,until,wait_output
from connection_modes_regression import FIXTURE,connect,frame,output

def main():
 binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
 with tempfile.TemporaryDirectory(prefix='tdock-tmux-') as root:
  h=Harness(root,binary)
  try:
   h.cli('settings','set','language','en')
   record=json.loads(h.cli('new-session','-ds','work','-c',root,'--json','--','/bin/sh','-c','read value').stdout)
   ref=record['spec']['id'];before=h.show(ref)['runtime']
   assert record['spec']['dir']==root
   assert not h.cli('has-session','-t','work').stdout
   assert not h.cli('has',ref[:8]).stdout
   assert h.cli('has','-t','missing',check=False).returncode==1
   sentinel=pathlib.Path(root)/'must-not-run'
   reused=json.loads(h.cli('new','-Ad','-s','work','--json',f'touch {sentinel}').stdout)
   assert reused['spec']['id']==ref and reused['runtime']['child_pid']==before['child_pid'] and not sentinel.exists()
   for label in (ref,ref[:8]):
    named=h.create(label,'/bin/sh','-c','read value')
    result=h.cli('new','-Ads',label)
    assert result.stdout.strip().decode()==named,('reused wrong session',label,named,result.stdout)
   def reuse(_):return h.cli('new','-Ads','raced','--','/bin/sh','-c','read value').stdout.strip()
   with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:ids=list(pool.map(reuse,range(4)))
   assert len(set(ids))==1,ids
   shell=json.loads(h.cli('new-session','-d','-s','shell-command','--json',"printf 'SHELL:%s\\n' quoted; exit 9").stdout)
   assert h.finished(shell['spec']['id'])['runtime']['exit_code']==9
   assert b'SHELL:quoted' in h.cli('logs','shell-command').stdout
   argv=json.loads(h.cli('new','-ds','argv','--json','--',sys.executable,'-c','import sys,json;print(json.dumps(sys.argv[1:]))','space ; $HOME','--name=literal').stdout)
   h.finished(argv['spec']['id'])
   assert json.loads(h.cli('logs','argv').stdout)==['space ; $HOME','--name=literal']
   child=json.loads(h.cli('new','-ds','child-flags','--json','/bin/sh','-c','printf CHILD-FLAGS').stdout)
   h.finished(child['spec']['id']);assert b'CHILD-FLAGS' in h.cli('logs','child-flags').stdout
   window=json.loads(h.cli('new','-ds','named-session','-n','工作区','--json','--','/bin/sh','-c','exit 0').stdout)
   assert window['spec']['name']=='named-session' and window['spec']['window_name']=='工作区'
   h.cli('rename-session','-t','work','renamed');assert h.show(ref)['spec']['name']=='renamed'
   h.cli('rename',ref,'work');assert h.show(ref)['runtime']['child_pid']==before['child_pid']
   for command in [('rename','-t','work','work','bad'),('attach','-t','work','work'),('has','-t',''),('new','-A'),('list',),('status','work'),('rm','work'),('show','work','--json')]:
    assert h.cli(*command,check=False).returncode!=0,command
   assert h.show(ref)['spec']['name']=='work' and h.show(ref)['health']=='online'
   assert json.loads(h.cli('list-sessions','--json').stdout)==json.loads(h.cli('ls','--json').stdout)
   h.cli('stop','work');assert h.cli('has','-t','work',check=False).returncode==1
   assert any(r['spec']['id']==ref for r in json.loads(h.cli('ls','--json').stdout))
   reused=h.cli('new','-Ads','work').stdout.decode().strip()
   assert reused != ref
   h.cli('stop',reused);h.cli('delete',reused);h.cli('delete',ref)
   live=h.create('live',sys.executable,'-c',FIXTURE);until(lambda:b'MODES-READY' in h.cli('logs',live).stdout)
   pid,fd=h.spawn_tty('new-session','-A','-s','live');wait_output(fd,b'[live]');os.write(fd,b'reused-live\n')
   wait_output(fd,b'ECHO:reused-live');os.write(fd,b'\x1b[24~');assert h.join(pid)==0
   owner,_=connect(h,live,'exclusive')
   try:
    pid,fd=h.spawn_tty('attach-session','-r','-t','live');wait_output(fd,b'Read only')
    os.write(fd,b'FORBIDDEN\n');frame(owner,3,b'visible\n');wait_output(fd,b'ECHO:visible')
    os.write(fd,b'\x1b[24~');assert h.join(pid)==0
    assert b'FORBIDDEN' not in h.cli('logs',live).stdout
   finally:owner.close()
   owner,_=connect(h,live,'exclusive')
   try:
    pid,fd=h.spawn_tty('attach-session','-d','-t','live');wait_output(fd,b'[live]')
    os.write(fd,b'takeover-shortcut\n');wait_output(fd,b'ECHO:takeover-shortcut')
    os.write(fd,b'\x11');assert h.join(pid)==0
   finally:owner.close()
   original=h.show(live)['runtime']
   victim=h.create('victim','/bin/sh','-c','read value');h.cli('kill-session','-t','victim')
   assert h.cli('show',victim,check=False).returncode!=0
   assert h.show(live)['runtime']==original
   print('PASS tmux session aliases, -s/-c/-d/-t/-r/-A, concurrent reuse, shell/argv boundaries, explicit targets, has exit codes, scoped kill and removed redundant CLI entries')
  finally:h.cleanup()
if __name__=='__main__':main()
