#!/usr/bin/env python3
"""Optional local Codex/OpenCode startup and reconnect smoke; submits no model prompts.

This is a PTY lifecycle check, not graphical terminal or application interaction acceptance.
"""
import fcntl
import json
import os
import pathlib
import pty
import select
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time
from integration import BINARY,ENV,Harness,drain,until


def direct_startup(binary,cwd):
    pid,fd=pty.fork()
    if pid==0:
        os.chdir(cwd)
        fcntl.ioctl(0,termios.TIOCSWINSZ,struct.pack('HHHH',24,80,0,0))
        os.execve(binary,[binary],ENV)
    output=drain(fd,1.5)
    try:os.killpg(pid,signal.SIGTERM)
    except ProcessLookupError:pass
    deadline=time.monotonic()+3
    while time.monotonic()<deadline:
        got,_=os.waitpid(pid,os.WNOHANG)
        if got:break
        drain(fd,.1)
    else:
        os.killpg(pid,signal.SIGKILL);os.waitpid(pid,0)
    os.close(fd)
    return len(output)


def main():
    report=[]
    binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else BINARY).resolve()
    report.append({'tdock':subprocess.check_output([str(binary),'--version'],text=True).strip()})
    with tempfile.TemporaryDirectory(prefix='tdock-apps-') as root:
        h=Harness(root,binary)
        active=[]
        try:
            for name in ('codex','opencode'):
                path=shutil.which(name)
                if not path:
                    report.append({'app':name,'status':'skipped: executable unavailable'});continue
                version=subprocess.check_output([path,'--version'],env=ENV,timeout=15,text=True).strip()
                direct_bytes=direct_startup(path,root)
                entry={'app':name,'version':version,'direct_startup_bytes':direct_bytes,'tasks':[]}
                for index in range(2):
                    created=json.loads(h.cli('new','-d','--json','-s',name+'-'+str(index),'--cwd',root,'--',path).stdout)
                    task=created['spec']['id']
                    until(lambda:len(h.cli('logs',task,'--raw').stdout)>0)
                    before=h.show(task)
                    if before['health']=='online':
                        active.append((task,before['runtime']['child_pid']))
                        entry['tasks'].append({'id':task,'startup_log_bytes':len(h.cli('logs',task,'--raw').stdout)})
                    else:entry['tasks'].append({'id':task,'status':'startup ended before attach'})
                report.append(entry)
            for index in range(2):
                task=h.create('shell-'+str(index),'/bin/sh','-c','read value')
                active.append((task,h.show(task)['runtime']['child_pid']))
            for _ in range(2):
                for task,child_pid in active:
                    pid,fd=h.spawn_tty('attach',task)
                    drain(fd,.2);os.write(fd,b'\x11');drain(fd,.15)
                    assert h.join(pid)==0
                    after=h.show(task)
                    assert after['health']=='online' and after['runtime']['child_pid']==child_pid
            report.append({'mixed_tasks':len(active),'attachment_rounds':2,'status':'all original child PIDs stayed online; no model prompt sent'})
        finally:h.cleanup()
    print(json.dumps(report,ensure_ascii=False,indent=2))

if __name__=='__main__':main()
