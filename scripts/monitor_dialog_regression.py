#!/usr/bin/env python3
"""Real PTY monitor, modal confirmation and numbered-name regression.

All test tasks live in temporary state; accepts an optional old broker binary.
"""
import fcntl
import json
import os
import pathlib
import re
import signal
import struct
import sys
import tempfile
import termios
import time

from integration import Harness, drain, frame, receive, until
from screen_regression import Terminal
from ui_regression import wait_view

FIXTURE=r'''
import os,select,signal,sys,time,tty
tty.setraw(0)
name=sys.argv[1]
def paint(*args):
    s=os.get_terminal_size(0)
    os.write(1,('\x1b[?1049h\x1b[H\x1b[2J\x1b[1;1HSCREEN-%s\x1b[3;1HSTATIC-%s\x1b[4;1HSIZE:%d,%d'%(name,name,s.lines,s.columns)).encode())
signal.signal(signal.SIGWINCH,paint)
paint()
count=0
while True:
    os.write(1,('\x1b[6;1Hcounter:%d\x1b[K'%count).encode());count+=1
    if select.select([0],[],[],.08)[0]:
        p=os.read(0,4096)
        if not p:break
        os.write(1,b'\x1b[8;1HINPUT:'+p+b'\x1b[K')
'''


def resize(pid,fd,terminal,rows,cols):
    terminal.columns,terminal.rows=cols,rows
    terminal.main.resize(lines=rows,columns=cols);terminal.alt.resize(lines=rows,columns=cols)
    fcntl.ioctl(fd,termios.TIOCSWINSZ,struct.pack('HHHH',rows,cols,0,0))
    os.kill(pid,signal.SIGWINCH)


def latest_size(h,ref):
    values=re.findall(rb'SIZE:(\d+),(\d+)',h.cli('logs',ref,'--raw').stdout)
    return tuple(map(int,values[-1])) if values else None


def records(h):return json.loads(h.cli('ls','--json').stdout)
def identity(h,ref):
    r=h.show(ref)['runtime'];return tuple(r[k] for k in ['instance_id','broker_pid','child_pid'])


def modal(terminal,title,expected=None,fd=None):
    def bounds():
        for top in range(terminal.rows):
            for left in range(terminal.columns):
                if terminal.current.buffer[top][left].data!='╭':continue
                right=next((x for x in range(left+1,terminal.columns) if terminal.current.buffer[top][x].data=='╮'),None)
                if right is None:continue
                for bottom in range(top+1,terminal.rows):
                    if terminal.current.buffer[bottom][left].data!='╰' or terminal.current.buffer[bottom][right].data!='╯':continue
                    if all(terminal.current.buffer[y][x].data=='│' for y in range(top+1,bottom) for x in (left,right)):
                        return top,bottom,left
        return None
    if fd is not None:
        wait_view(fd,terminal,lambda text:title in text and (not expected or expected in text) and bounds() is not None)
    text='\n'.join(terminal.display)
    assert title in text,text
    found=bounds()
    assert found is not None,'confirmation must be a complete bordered overlay:\n'+text
    top,bottom,left=found
    assert top>=2 and bottom>top,'confirmation must be centered:\n'+text
    assert left>0,'dialog must stand apart from the left margin'
    if expected:assert expected in text,text
    return text


def test_monitor(h,frontend):
    alpha=h.create('alpha',sys.executable,'-c',FIXTURE,'alpha')
    beta=h.create('beta',sys.executable,'-c',FIXTURE,'beta')
    until(lambda:latest_size(h,alpha)==(24,80) and latest_size(h,beta)==(24,80))
    before={ref:identity(h,ref) for ref in [alpha,beta]}
    # An existing controller must not be displaced merely by selecting its task.
    owner,kind,_=h.socket(beta);assert kind==2
    h.binary=frontend
    pid,fd=h.spawn_tty();terminal=Terminal()
    try:
        wait_view(fd,terminal,lambda text:'alpha' in text and 'beta' in text and 'Memory' in text)
        os.write(fd,b'v')
        text=wait_view(fd,terminal,lambda text:'STATIC-alpha' in text and 'counter:' in text)
        screen_row=next(row for row in terminal.display if 'STATIC-alpha' in row)
        assert screen_row.index('STATIC-alpha')>screen_row.index('│'),text
        until(lambda:latest_size(h,alpha)==(17,54)) # 80x24: content 76, list 19, gap 3.
        os.write(fd,b'\x1b[D\x1b[Chl') # Horizontal keys must not leave/reopen monitor.
        first=re.search(r'counter:(\d+)',text).group(1)
        wait_view(fd,terminal,lambda text:bool((m:=re.search(r'counter:(\d+)',text)) and m.group(1)!=first))
        os.write(fd,b'z')
        time.sleep(.15)
        until(lambda:b'INPUT:z' in h.cli('logs',alpha,'--raw').stdout)
        resize(pid,fd,terminal,30,120)
        wait_view(fd,terminal,lambda text:'SIZE:23,85' in text)
        assert latest_size(h,alpha)==(23,85)
        os.write(fd,b'\x1b[B')
        wait_view(fd,terminal,lambda text:'STATIC-beta' in text and 'Control' in text)
        assert latest_size(h,beta)==(24,80), 'joining must not resize the existing controller'
        until(lambda:latest_size(h,alpha)==(23,85))
        frame(owner,9);kind,_=receive(owner)
        while kind!=10:kind,_=receive(owner)
        assert h.show(beta)['health']=='online'
        owner.close()
        until(lambda:latest_size(h,beta)==(23,85))
        os.write(fd,b'\x1b[15~')
        wait_view(fd,terminal,lambda text:'STATIC-beta' in text and 'Unable to monitor' not in text)
        os.write(fd,b'\x1b[24~')
        wait_view(fd,terminal,lambda text:'Memory' in text and 'STATIC-beta' not in text)
        until(lambda:latest_size(h,beta)==(23,85))
        os.write(fd,b'v')
        wait_view(fd,terminal,lambda text:'STATIC-beta' in text)
        # Return to the list before entering the ordinary terminal.
        os.write(fd,b'\x1b')
        wait_view(fd,terminal,lambda text:'Memory' in text and 'STATIC-beta' not in text)
        os.write(fd,b'\r')
        wait_view(fd,terminal,lambda text:'STATIC-beta' in text and 'alpha' not in text)
        os.write(fd,b'K')
        wait_view(fd,terminal,lambda text:'INPUT:K' in text)
        os.write(fd,b'\x11')
        wait_view(fd,terminal,lambda text:'Memory' in text and 'STATIC-beta' not in text)
        # Returning to monitor sets the pane size and leaves it in place on exit.
        os.write(fd,b'v');wait_view(fd,terminal,lambda text:'STATIC-beta' in text)
        until(lambda:latest_size(h,beta)==(23,85))
        os.write(fd,b'\x1b');wait_view(fd,terminal,lambda text:'Memory' in text)
        offset=len(h.cli('logs',beta,'--raw').stdout)
        for _ in range(3):
            os.write(fd,b'v');wait_view(fd,terminal,lambda text:'STATIC-beta' in text)
            resize(pid,fd,terminal,30,120) # Same window size must be a no-op.
            os.write(fd,b'\x1b[24~');wait_view(fd,terminal,lambda text:'Memory' in text and 'STATIC-beta' not in text)
        updates=re.findall(rb'SIZE:(\d+),(\d+)',h.cli('logs',beta,'--raw').stdout[offset:])
        assert all(tuple(map(int,size))==(23,85) for size in updates),updates
        os.write(fd,b'q');drain(fd);assert h.join(pid)==0
        for ref in [alpha,beta]:assert identity(h,ref)==before[ref]
        print('PASS v split monitor/static cells/live refresh, direct keyboard input, pane resize, switch/size retention, occupied-session shared join/promotion, list Enter attach/detach returns to list, stable task PIDs',flush=True)
    finally:owner.close()


def test_dialogs_and_names(h):
    work=h.create('work',sys.executable,'-c',FIXTURE,'work')
    old=h.create('work-2','/bin/sh','-c','exit 0');h.finished(old)
    source=h.create('rename-source',sys.executable,'-c',FIXTURE,'source')
    original={ref:identity(h,ref) for ref in [work,source]}
    pid,fd=h.spawn_tty();terminal=Terminal()
    wait_view(fd,terminal,lambda text:'rename-source' in text)
    os.write(fd,b'\x1b[B\x1bOQ')
    wait_view(fd,terminal,lambda text:'New session name' in text)
    os.write(fd,b'\x01\x0bwork\r')
    wait_view(fd,terminal,lambda text:'Name already exists' in text and 'work-3' in text)
    modal(terminal,'Name already exists','work-3',fd=fd)
    assert h.show(source)['spec']['name']=='rename-source'
    os.write(fd,b'\r') # Cancel is selected by default.
    wait_view(fd,terminal,lambda text:'Name already exists' not in text and 'New session name' in text)
    assert h.show(source)['spec']['name']=='rename-source'
    os.write(fd,b'\r')
    wait_view(fd,terminal,lambda text:'Name already exists' in text)
    renamed_claim=h.create('work-3','/bin/sh','-c','exit 0');h.finished(renamed_claim)
    os.write(fd,b'y')
    wait_view(fd,terminal,lambda text:'Name already exists' in text and 'work-3-2' in text)
    modal(terminal,'Name already exists','work-3-2',fd=fd)
    assert h.show(source)['spec']['name']=='rename-source'
    os.write(fd,b'y')
    until(lambda:h.show(source)['spec']['name']=='work-3-2')
    wait_view(fd,terminal,lambda text:'Name already exists' not in text and 'work-3-2' in text)
    # New collisions count all names, including stopped history.
    count=len(records(h))
    os.write(fd,b'n');wait_view(fd,terminal,lambda text:'New shell name' in text)
    os.write(fd,b'work\r')
    wait_view(fd,terminal,lambda text:'Name already exists' in text and 'work-4' in text)
    modal(terminal,'Name already exists','work-4',fd=fd);assert len(records(h))==count
    os.write(fd,b'\x1b');wait_view(fd,terminal,lambda text:'Name already exists' not in text)
    assert len(records(h))==count
    os.write(fd,b'\r');wait_view(fd,terminal,lambda text:'Name already exists' in text)
    # Race: another process claims the reviewed suggestion before confirmation.
    claimed=h.create('work-4','/bin/sh','-c','exit 0');h.finished(claimed)
    os.write(fd,b'y')
    wait_view(fd,terminal,lambda text:'Name already exists' in text and 'work-4-2' in text)
    modal(terminal,'Name already exists','work-4-2',fd=fd)
    assert len(records(h))==count+1,'race silently created an unreviewed alternative'
    os.write(fd,b'y')
    wait_view(fd,terminal,lambda text:'Control' in text and 'work-4-2' in text and 'Name already exists' not in text)
    created=next(r for r in records(h) if r['spec']['name']=='work-4-2')
    os.write(fd,b'\x11');wait_view(fd,terminal,lambda text:terminal.alternate and 'Memory' in text)
    # Stop and delete are actual centered modals, preserving the list behind.
    os.write(fd,b's');wait_view(fd,terminal,lambda text:'Confirm stop' in text)
    modal(terminal,'Confirm stop','work-4-2',fd=fd)
    os.write(fd,b'\r');wait_view(fd,terminal,lambda text:'Confirm stop' not in text)
    assert h.show(created['spec']['id'])['health']=='online'
    os.write(fd,b's');wait_view(fd,terminal,lambda text:'Confirm stop' in text)
    os.write(fd,b'\x1b[C\r');h.finished(created['spec']['id'])
    wait_view(fd,terminal,lambda text:'Confirm stop' not in text)
    os.write(fd,b'\x1b[C');wait_view(fd,terminal,lambda text:'work-2' in text and 'Stop' in text)
    os.write(fd,b'\x1b[3~');wait_view(fd,terminal,lambda text:'Confirm deletion' in text)
    modal(terminal,'Confirm deletion',fd=fd)
    os.write(fd,b'\r');wait_view(fd,terminal,lambda text:'Confirm deletion' not in text)
    assert any(r['spec']['id']==old for r in records(h))
    os.write(fd,b'\x1b[3~');wait_view(fd,terminal,lambda text:'Confirm deletion' in text)
    os.write(fd,b'y');until(lambda:all(r['spec']['id']!=old for r in records(h)))
    os.write(fd,b'q');drain(fd);assert h.join(pid)==0
    for ref in [work,source]:assert identity(h,ref)==original[ref]
    print('PASS centered stop/delete/duplicate-name dialogs, default Cancel/selected action, rename/new numbered suggestions include history, race prompts again, no silent changes to existing tasks',flush=True)


def test_languages(h):
    h.create('work',sys.executable,'-c',FIXTURE,'work')
    source=h.create('source',sys.executable,'-c',FIXTURE,'source')
    before=identity(h,source)
    for code,control,stop,conflict in [
        ('en','Control','Confirm stop','Name already exists'),
        ('zh-CN','可控制','确认停止','会话名称已存在'),
        ('ja','操作中','停止の確認','名前が既に存在します'),
        ('es','Control','Confirmar detención','El nombre ya existe'),
        ('fr','Contrôle','Confirmer l’arrêt','Ce nom existe déjà'),
    ]:
        h.cli('settings','set','language',code)
        pid,fd=h.spawn_tty();terminal=Terminal()
        wait_view(fd,terminal,lambda text:'source' in text and 'work' in text)
        os.write(fd,b'\x1b[Bv')
        wait_view(fd,terminal,lambda text:'STATIC-source' in text and control in text)
        os.write(fd,b'\x1b[24~')
        wait_view(fd,terminal,lambda text:'STATIC-source' not in text)
        os.write(fd,b's');wait_view(fd,terminal,lambda text:stop in text)
        modal(terminal,stop,'source',fd=fd);os.write(fd,b'\x1b')
        wait_view(fd,terminal,lambda text:stop not in text and 'STATIC-source' not in text)
        os.write(fd,b'\x1bOQ');drain(fd,.1)
        os.write(fd,b'\x01\x0bwork\r')
        wait_view(fd,terminal,lambda text:conflict in text and 'work-2' in text)
        modal(terminal,conflict,'work-2',fd=fd)
        os.write(fd,b'\x1b');drain(fd,.1)
        os.write(fd,b'\x1b');wait_view(fd,terminal,lambda text:conflict not in text and 'STATIC-source' not in text)
        os.write(fd,b'q');drain(fd);assert h.join(pid)==0
        assert identity(h,source)==before and h.show(source)['spec']['name']=='source'
    print('PASS monitor label, stop and numbered-name modals in five languages, cancellation preserves original task',flush=True)


def test_missing_output(h):
    ref=h.create('missing-output',sys.executable,'-c',FIXTURE,'missing')
    until(lambda:latest_size(h,ref)==(24,80));before=identity(h,ref)
    (h.state/'tasks'/ref/'output.log').unlink()
    pid,fd=h.spawn_tty();terminal=Terminal()
    wait_view(fd,terminal,lambda text:'missing-output' in text)
    os.write(fd,b'v')
    wait_view(fd,terminal,lambda text:'Unable to monitor' in text and 'output is unavailable' in text)
    def released():
        conn,kind,_=h.socket(ref);conn.close();return kind==2
    until(released)
    assert identity(h,ref)==before and h.show(ref)['health']=='online'
    os.write(fd,b'q');drain(fd);assert h.join(pid)==0
    print('PASS missing output shows unable-to-monitor and releases its lease without stopping the task',flush=True)


def main():
    frontend=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
    old=pathlib.Path(sys.argv[2]).resolve() if len(sys.argv)>2 else frontend
    for test in [test_monitor,test_dialogs_and_names,test_languages,test_missing_output]:
        with tempfile.TemporaryDirectory(prefix='tdock-monitor-ui-') as root:
            h=Harness(root,old if test==test_monitor else frontend)
            try:
                h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
                if test==test_monitor:test(h,frontend)
                else:test(h)
            finally:h.cleanup()

if __name__=='__main__':main()
