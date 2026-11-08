#!/usr/bin/env python3
"""Local-only real PTY/socket/TUI coverage of takeover, read-only and shared control.

All tasks use temporary state. Dependencies: scripts/requirements-screen.txt.
"""
import json
import os
import pathlib
import socket
import sys
import tempfile
import time

from integration import Harness, drain, frame, receive, until, wait_output
from screen_regression import Terminal, CURSES_LIKE
from ui_regression import wait_view

CAP='attach-modes-v1'
FIXTURE=r'''
import os,tty
# A raw, line-delimited echo allows byte-exact forwarding and resize checks.
tty.setraw(0)
os.write(1,b'MODES-READY\n')
pending=b''
while True:
    part=os.read(0,65536)
    if not part:break
    pending+=part
    while b'\n' in pending:
        line,pending=pending.split(b'\n',1)
        if line==b'SIZE':
            s=os.get_terminal_size(0);os.write(1,('SIZE:%d,%d\n'%(s.lines,s.columns)).encode())
        else:os.write(1,b'ECHO:'+line+b'\n')
'''


def connect(h,ref,mode,rows=24,cols=80):
    record=h.show(ref)
    task=record['spec']['id']
    # Harness.socket computes the private runtime path and validates permissions.
    conn,kind,payload=h.socket(ref,mode='control')
    assert kind==2 and CAP in json.loads(payload)['capabilities']
    conn.close()
    import hashlib
    digest=hashlib.sha256(str(h.state.resolve()).encode()).hexdigest()[:12]
    conn=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);conn.settimeout(6)
    conn.connect(f'/tmp/tdock-{os.getuid()}/{digest}/{task}.sock')
    frame(conn,1,{'task_id':task,'instance_id':record['runtime']['instance_id'],'mode':'attach','size':{'rows':rows,'cols':cols},'attach_mode':mode,'capabilities':[CAP]})
    kind,payload=receive(conn)
    assert kind==2,(kind,payload)
    welcome=json.loads(payload);assert welcome['attach_mode']==mode
    return conn,welcome


def read_until(conn,kind,predicate=lambda value:True):
    deadline=time.monotonic()+6
    while time.monotonic()<deadline:
        current,payload=receive(conn)
        if current==kind and predicate(payload):return payload
        if current==8 and kind!=8:raise AssertionError(payload)
    raise AssertionError('expected frame did not arrive')


def output(conn,needle):
    accumulated=bytearray()
    def found(part):
        accumulated.extend(part)
        return needle in accumulated
    read_until(conn,4,found)
    return bytes(accumulated)


def test_protocol(h):
    ref=h.create('mode-protocol',sys.executable,'-c',FIXTURE)
    until(lambda:b'MODES-READY' in h.cli('logs',ref,'--raw').stdout)
    before=h.show(ref)['runtime']
    connections=[]
    try:
        first,welcome=connect(h,ref,'exclusive');connections.append(first)
        assert welcome['size_owner']
        observer,welcome=connect(h,ref,'readonly',rows=10,cols=35);connections.append(observer)
        assert not welcome.get('size_owner',False) and welcome['size']=={'rows':24,'cols':80}
        # Broker-side enforcement, even when a malicious observer sends frames.
        for kind,payload in [(3,b'FORBIDDEN-INPUT\n'),(5,{'rows':1,'cols':1}),(11,b'')]:
            bad,_=connect(h,ref,'readonly');connections.append(bad)
            frame(bad,kind,payload)
            error=json.loads(read_until(bad,8))
            assert error['code']=='read_only',error
            assert h.show(ref)['health']=='online'
        shared,welcome=connect(h,ref,'shared',rows=10,cols=35);connections.append(shared)
        assert not welcome.get('size_owner',False)
        frame(first,3,b'owner:\x1b[31m'+'中文'.encode()+b'\x00\xff\n')
        expected=b'ECHO:owner:\x1b[31m'+'中文'.encode()+b'\x00\xff\n'
        for conn in [first,observer,shared]:assert expected in output(conn,expected)
        frame(shared,3,b'shared-input\n')
        for conn in [first,observer,shared]:output(conn,b'ECHO:shared-input\n')
        frame(first,5,{'rows':31,'cols':101})
        frame(first,9) # Wait until the owner resize is applied before another client queries.
        read_until(first,10)
        frame(shared,5,{'rows':1,'cols':1})
        frame(shared,3,b'SIZE\n')
        for conn in [first,observer,shared]:output(conn,b'SIZE:31,101\n')
        replacement,welcome=connect(h,ref,'takeover',rows=28,cols=100);connections.append(replacement)
        assert welcome['size_owner']
        for conn in [first,shared]:assert json.loads(read_until(conn,8))['code']=='taken_over'
        # Sending after the takeover must never enter the PTY, even if an old
        # socket has not yet observed its final error/EOF.
        for conn in [first,shared]:
            try:frame(conn,3,b'FORBIDDEN-STALE\n')
            except (BrokenPipeError,ConnectionResetError):pass
        frame(replacement,3,b'after-takeover\n')
        for conn in [observer,replacement]:output(conn,b'ECHO:after-takeover\n')
        promoted,welcome=connect(h,ref,'shared',rows=26,cols=90);connections.append(promoted)
        assert not welcome.get('size_owner',False)
        replacement.close()
        read_until(promoted,12,lambda p:json.loads(p).get('type')=='role' and json.loads(p).get('size_owner'))
        frame(promoted,5,{'rows':26,'cols':90});frame(promoted,3,b'SIZE\n')
        for conn in [observer,promoted]:output(conn,b'SIZE:26,90\n')
        after=h.show(ref)['runtime']
        for key in ['instance_id','broker_pid','child_pid']:assert after[key]==before[key]
        logs=h.cli('logs',ref,'--raw').stdout
        assert b'FORBIDDEN' not in logs
        print('PASS opaque fanout, multiple writers, enforced read-only input/resize/stop, takeover revokes all writers but preserves readers, stable task PIDs, owner promotion/size',flush=True)
    finally:
        for conn in connections:conn.close()
        h.cli('stop',ref)


def test_cli_and_tui(h):
    ref=h.create('mode-ui',sys.executable,'-c',FIXTURE)
    until(lambda:b'MODES-READY' in h.cli('logs',ref,'--raw').stdout)
    owner,_=connect(h,ref,'exclusive')
    try:
        pid,fd=h.spawn_tty('attach',ref,'--read-only');term=Terminal()
        wait_view(fd,term,lambda text:'Read only' in text)
        os.write(fd,b'FORBIDDEN-KEYBOARD\n\x03')
        frame(owner,3,b'visible-to-observer\n')
        wait_view(fd,term,lambda text:'ECHO:visible-to-observer' in text)
        assert b'FORBIDDEN' not in h.cli('logs',ref,'--raw').stdout
        os.write(fd,b'\x11');drain(fd);assert h.join(pid)==0
        # Ordinary Enter joins shared; c exposes explicit read-only/shared/takeover.
        pid,fd=h.spawn_tty();term=Terminal()
        wait_view(fd,term,lambda text:'mode-ui' in text)
        os.write(fd,b'c')
        wait_view(fd,term,lambda text:'Connection modes' in text)
        os.write(fd,b'\x1b[A\r');wait_view(fd,term,lambda text:'Read only' in text and 'Connection modes' not in text)
        os.write(fd,b'\x11');term.feed(wait_output(fd,b'TaskDock'))
        wait_view(fd,term,lambda text:term.alternate and 'mode-ui' in text and 'Memory' in text)
        # Choose shared control and send input from both ends.
        os.write(fd,b'c');wait_view(fd,term,lambda text:'Connection modes' in text)
        os.write(fd,b'\r');wait_view(fd,term,lambda text:'Shared control' in text and 'Connection modes' not in text)
        os.write(fd,b'from-ui-shared\n');output(owner,b'ECHO:from-ui-shared\n')
        frame(owner,3,b'from-other-controller\n')
        wait_view(fd,term,lambda text:'ECHO:from-other-controller' in text)
        os.write(fd,b'\x11');term.feed(wait_output(fd,b'TaskDock'))
        wait_view(fd,term,lambda text:term.alternate and 'mode-ui' in text and 'Memory' in text)
        # Takeover requires a second, explicit confirmation.
        os.write(fd,b'c');wait_view(fd,term,lambda text:'Connection modes' in text)
        os.write(fd,b'\x1b[B\r');wait_view(fd,term,lambda text:'y confirm' in text)
        os.write(fd,b'\x1b');wait_view(fd,term,lambda text:'Memory' in text and 'y confirm' not in text)
        frame(owner,3,b'cancel-preserves-owner\n');output(owner,b'ECHO:cancel-preserves-owner\n')
        os.write(fd,b'c');wait_view(fd,term,lambda text:'Connection modes' in text)
        os.write(fd,b'\x1b[B\r');wait_view(fd,term,lambda text:'y confirm' in text)
        os.write(fd,b'y');wait_view(fd,term,lambda text:'Take over' in text and 'Connection modes' not in text)
        assert json.loads(read_until(owner,8))['code']=='taken_over'
        os.write(fd,b'from-new-owner\n');wait_view(fd,term,lambda text:'ECHO:from-new-owner' in text)
        # A CLI takeover sends the previous TUI controller back to its list.
        replacement,other=h.spawn_tty('attach',ref,'--takeover');other_term=Terminal()
        wait_view(other,other_term,lambda text:'Take over' in text)
        wait_view(fd,term,lambda text:'taken over in another terminal' in text and 'Memory' in text)
        os.write(other,b'\x11');drain(other);assert h.join(replacement)==0
        os.write(fd,b'q');drain(fd);assert h.join(pid)==0
        print('PASS CLI read-only filters real keyboard, TUI explicit choices/default shared, shared input, confirmed/cancelled takeover, displaced TUI returns to list',flush=True)
    finally:owner.close();h.cli('stop',ref)


def test_fullscreen(h):
    pid,fd=h.spawn_tty('new','--name','mode-screen','--',sys.executable,'-c',CURSES_LIKE)
    owner_term=Terminal()
    wait_view(fd,owner_term,lambda text:'SCREEN-TITLE' in text and 'STATIC-LABEL' in text)
    for flag,label in [('--read-only','Read only'),('--shared','Shared control')]:
        observer,other=h.spawn_tty('attach','mode-screen',flag);term=Terminal()
        text=wait_view(other,term,lambda text:'SCREEN-TITLE' in text and 'STATIC-LABEL' in text)
        assert term.alternate,'viewer did not restore alternate screen'
        os.write(other,b'\x11');drain(other);assert h.join(observer)==0
        owner_term.feed(drain(fd,.15))
        assert 'STATIC-LABEL' in '\n'.join(owner_term.display)
    os.write(fd,b'\x11');drain(fd);assert h.join(pid)==0
    h.cli('stop','mode-screen')
    print('PASS fullscreen read-only/shared join requests owner repaint and restores static cells/modes',flush=True)



def test_languages(h):
    ref=h.create('mode-languages',sys.executable,'-c',FIXTURE)
    for code,title,readonly,shared,takeover,hint in [
        ('en','Connection modes','Read only','Shared control','Take over','y confirm'),
        ('zh-CN','连接方式','只读','共享控制','接管','y 确认'),
        ('ja','接続方法','閲覧のみ','共同操作','引き継ぐ','y 確定'),
        ('es','Modo de conexión','Solo lectura','Control compartido','Tomar control','y confirmar'),
        ('fr','Mode de connexion','Lecture seule','Contrôle partagé','Prendre le contrôle','y confirmer'),
    ]:
        h.cli('settings','set','language',code)
        pid,fd=h.spawn_tty();term=Terminal()
        wait_view(fd,term,lambda text:'mode-languages' in text)
        os.write(fd,b'c')
        wait_view(fd,term,lambda text:all(label in text for label in [title,readonly,shared,takeover]))
        row=next(i for i,line in enumerate(term.display) if shared in line)
        column=term.display[row].index(shared)
        assert term.current.buffer[row][column].reverse,'shared control must be the default'
        os.write(fd,b'\x1b[B\r');wait_view(fd,term,lambda text:hint in text)
        os.write(fd,b'\x1b');wait_view(fd,term,lambda text:title not in text and hint not in text and 'mode-languages' in text)
        os.write(fd,b'q');drain(fd);assert h.join(pid)==0
    h.cli('settings','set','language','en');h.cli('stop',ref)
    print('PASS localized connection menus, shared default and takeover confirmation in five languages',flush=True)


def test_old_broker(binary,old_binary):
    with tempfile.TemporaryDirectory(prefix='tdock-modes-old-') as root:
        h=Harness(root,old_binary)
        try:
            h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
            ref=h.create('old-broker',sys.executable,'-c',FIXTURE)
            before=h.show(ref)['runtime']
            h.binary=binary
            for flag in ['--read-only','--shared','--takeover']:
                pid,fd=h.spawn_tty('attach',ref,flag)
                data=b'';deadline=time.monotonic()+6
                while b'unsupported_attach_mode' not in data and time.monotonic()<deadline:data+=drain(fd,.05)
                assert b'unsupported_attach_mode' in data,data
                assert h.join(pid)==1
            after=h.show(ref)['runtime']
            for key in ['instance_id','broker_pid','child_pid']:assert before[key]==after[key]
            pid,fd=h.spawn_tty('attach',ref);term=Terminal()
            wait_view(fd,term,lambda text:'TaskDock' in text)
            os.write(fd,b'old-broker-still-usable\n');wait_view(fd,term,lambda text:'ECHO:old-broker-still-usable' in text)
            os.write(fd,b'\x11');drain(fd);assert h.join(pid)==0
            print('PASS old broker rejects all new modes without acquiring control; ordinary attach and original task PIDs preserved',flush=True)
        finally:h.cleanup()
    with tempfile.TemporaryDirectory(prefix='tdock-modes-legacy-client-') as root:
        h=Harness(root,binary)
        connections=[]
        try:
            h.cli('settings','set','language','en');h.cli('settings','set','history-retention','off')
            ref=h.create('legacy-client',sys.executable,'-c',FIXTURE)
            h.binary=old_binary
            pid,fd=h.spawn_tty('attach',ref);term=Terminal()
            h.binary=binary
            wait_view(fd,term,lambda text:'TaskDock' in text)
            shared,_=connect(h,ref,'shared');connections.append(shared)
            frame(shared,3,b'new-writer-old-frontend\n')
            wait_view(fd,term,lambda text:'ECHO:new-writer-old-frontend' in text)
            os.write(fd,b'old-writer-new-broker\n');output(shared,b'ECHO:old-writer-new-broker\n')
            replacement,_=connect(h,ref,'takeover');connections.append(replacement)
            drain(fd);assert h.join(pid)==1
            assert h.show(ref)['health']=='online'
            print('PASS old frontend works with new broker, shared input/output and takeover error; no unnegotiated Notice frames',flush=True)
        finally:
            for conn in connections:conn.close()
            h.cleanup()


def main():
    binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
    with tempfile.TemporaryDirectory(prefix='tdock-modes-') as root:
        h=Harness(root,binary)
        try:
            h.cli('settings','set','language','en')
            h.cli('settings','set','history-retention','off')
            test_protocol(h)
            # Remove only this suite's completed records to keep UI selection deterministic.
            for record in json.loads(h.cli('ls','--json').stdout):h.cli('delete',record['spec']['id'])
            test_cli_and_tui(h)
            test_fullscreen(h)
            test_languages(h)
        finally:h.cleanup()
    if len(sys.argv)>2:test_old_broker(binary,pathlib.Path(sys.argv[2]).resolve())

if __name__=='__main__':main()
