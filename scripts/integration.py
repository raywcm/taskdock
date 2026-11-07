#!/usr/bin/env python3
"""Black-box tests against a real tdock binary. Uses only the Python standard library.

Run: python3 scripts/integration.py [bin/tdock]
All tasks and sockets belong to temporary test state; no user's task is touched.
"""
import concurrent.futures
import errno
import fcntl
import hashlib
import json
import os
import pathlib
import pty
import select
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time

BINARY = pathlib.Path(sys.argv[1] if __name__ == '__main__' and len(sys.argv) > 1 else 'bin/tdock').resolve()
ENV = dict(os.environ)
ENV.pop('XDG_RUNTIME_DIR', None)
ENV['TERM'] = 'xterm-256color'
ENV['GORACE'] = 'atexit_sleep_ms=0 halt_on_error=1'


def until(check, seconds=5):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        value = check()
        if value:
            return value
        time.sleep(.02)
    raise AssertionError('condition did not become true within timeout')


def drain(fd, duration=.15):
    out = b''
    end = time.monotonic() + duration
    while time.monotonic() < end:
        ready, _, _ = select.select([fd], [], [], max(0, end-time.monotonic()))
        if not ready:
            break
        try:
            part = os.read(fd, 65536)
            if not part:
                break
            out += part
        except OSError as err:
            if err.errno == errno.EIO:
                break
            raise
    return out


def wait_output(fd, needle, timeout=6, normalize_whitespace=False):
    """Wait for actual output instead of assuming frontend startup latency."""
    output = b''
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        output += drain(fd, .05)
        value = b' '.join(output.split()) if normalize_whitespace else output
        if needle in value:
            return output
    raise AssertionError(output)


def frame(conn, kind, payload=b''):
    if isinstance(payload, dict):
        payload = json.dumps(payload).encode()
    conn.sendall(b'MT' + bytes([1, kind]) + struct.pack('>I', len(payload)) + payload)


def receive(conn):
    def exact(size):
        value = b''
        while len(value) < size:
            part = conn.recv(size-len(value))
            if not part:
                raise EOFError('socket ended before frame completed')
            value += part
        return value
    header = exact(8)
    assert header[:3] == b'MT\x01', header
    length = struct.unpack('>I', header[4:])[0]
    assert length <= 65536
    return header[3], exact(length)


class Harness:
    def __init__(self, root, binary=BINARY):
        self.root = pathlib.Path(root)
        self.state = self.root/'state'
        self.binary = binary
        self.children = {}

    @property
    def base(self):
        return [str(self.binary), '--state-dir', str(self.state)]

    def cli(self, *args, check=True, **kwargs):
        result = subprocess.run(self.base+list(args), capture_output=True, env=ENV,
                                timeout=10, **kwargs)
        if check and result.returncode:
            raise AssertionError((args, result.returncode, result.stdout, result.stderr))
        return result

    def create(self, name, *argv):
        record = json.loads(self.cli('new', '-d', '--json', '--name', name, '--', *argv).stdout)
        return record['spec']['id']

    def show(self, ref):
        return json.loads(self.cli('show', ref).stdout)

    def finished(self, ref):
        result = until(lambda: (r if (r := self.show(ref)).get('runtime', {}).get('state') in ('exited', 'failed') else None))
        self.cli('stop', ref)
        return result

    def socket(self, ref, mode='attach', identity=None):
        record = self.show(ref)
        task = record['spec']['id']
        digest = hashlib.sha256(str(self.state.resolve()).encode()).hexdigest()[:12]
        path = pathlib.Path(f'/tmp/tdock-{os.getuid()}')/digest/(task+'.sock')
        if not path.exists():
            path = pathlib.Path(f'/tmp/mtask-{os.getuid()}')/digest/(task+'.sock')
        assert path.stat().st_mode & 0o777 == 0o600
        assert path.parent.stat().st_mode & 0o777 == 0o700
        conn = socket.socket(socket.AF_UNIX)
        conn.settimeout(3)
        conn.connect(str(path))
        hello = {'task_id': task, 'instance_id': identity or record['runtime']['instance_id'], 'mode': mode}
        if mode == 'attach':
            hello['size'] = {'rows': 24, 'cols': 80}
        frame(conn, 1, hello)
        kind, payload = receive(conn)
        return conn, kind, payload

    def spawn_tty(self, *args):
        pid, fd = pty.fork()
        if pid == 0:
            os.execve(str(self.binary), self.base+list(args), ENV)
        self.children[pid] = fd
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
        return pid, fd

    def join(self, pid, timeout=5, on_output=None):
        end = time.monotonic()+timeout
        tail = b''
        while time.monotonic() < end:
            # Keep consuming the PTY while waiting. A late/final repaint can
            # otherwise fill its output buffer and block a healthy frontend.
            part = drain(self.children[pid], .02)
            tail = (tail + part)[-4096:]
            if part and on_output:
                on_output(part)
            got, status = os.waitpid(pid, os.WNOHANG)
            if got:
                part = drain(self.children[pid], .02)
                if part and on_output:
                    on_output(part)
                os.close(self.children.pop(pid))
                return os.waitstatus_to_exitcode(status)
        raise AssertionError('frontend failed to exit; final output: '+repr(tail))

    def cleanup(self):
        for pid, fd in list(self.children.items()):
            # Close the PTY master before reaping. On macOS an exiting terminal
            # client can otherwise wait for its slave while waitpid blocks us.
            os.close(fd)
            try: os.kill(pid, signal.SIGKILL)
            except ProcessLookupError: pass
            deadline=time.monotonic()+2
            while time.monotonic()<deadline:
                try: got,_=os.waitpid(pid,os.WNOHANG)
                except ChildProcessError: break
                if got: break
                time.sleep(.02)
        self.children.clear()
        if not self.state.exists(): return
        records = json.loads(self.cli('ls', '--json').stdout)
        for record in records:
            task = record['spec']['id']
            self.cli('stop', task, check=False)
            self.cli('recover', task, check=False)
            self.cli('delete', task, check=False)


def test_lifecycle(h):
    task = h.create('中文任务', '/bin/sh', '-c', 'printf "ready\\n"; read value')
    assert h.show(task)['health'] == 'online'
    assert h.cli('delete', task, check=False).returncode != 0
    h.cli('rename', task, 'renamed')
    assert h.show('renamed')['spec']['id'] == task
    assert h.cli('new', '-d', '-s', 'renamed', '--', '/bin/sh', check=False).returncode != 0
    assert b'ready' in h.cli('logs', task).stdout
    h.cli('stop', task)
    assert h.show(task)['runtime']['exit_code'] == 143
    assert any(r['spec']['id'] == task for r in json.loads(h.cli('ls', '--history', '--json').stdout))
    h.cli('delete', task)
    assert h.cli('show', task, check=False).returncode != 0
    canonical=[str(h.binary),'--state-dir',str(h.state.resolve()),'ls','--json']
    assert subprocess.run(canonical,env=ENV,capture_output=True,timeout=5).returncode==0
    print('PASS lifecycle, names, active-delete rejection, history and JSON', flush=True)


def test_foreground_startup(h):
    for i in range(5):
        pid,fd=h.spawn_tty('new','--name','fast-'+str(i),'--','/bin/sh','-c','printf FAST-OUTPUT; exit 4')
        out=wait_output(fd,b'FAST-OUTPUT')
        assert b'FAST-OUTPUT' in out,out
        assert h.join(pid)==4
    # Startup prompts are delivered once even if emitted before the socket handshake.
    pid,fd=h.spawn_tty('new','--name','prompt','--','/bin/sh','-c','printf FIRST-PROMPT; read value')
    out=wait_output(fd,b'FIRST-PROMPT')
    assert out.count(b'FIRST-PROMPT')==1,out
    os.write(fd,b'\x1dd');drain(fd);assert h.join(pid)==0
    h.cli('stop','prompt')
    print('PASS fast foreground output/exit races and first prompt delivery',flush=True)


def test_attach(h):
    task = h.create('attach', '/bin/sh', '-c', 'while IFS= read -r line; do [ "$line" = quit ] && exit 7; printf "ECHO:%s\\n" "$line"; done')
    pid, fd = h.spawn_tty('attach', task)
    initial_state = termios.tcgetattr(fd)
    initial = drain(fd)
    label = ('TaskDock · attach ['+task[:8]+'] · Shared control').encode()
    assert b'\x1b[22;2t\x1b]2;'+label+b'\x07' in initial, initial
    assert b'attach [' in initial and b'RSS' in initial, initial
    os.write(fd, b'first\n')
    assert b'ECHO:first' in drain(fd)
    pid2, fd2 = h.spawn_tty('attach', task)
    wait_output(fd2, b'Shared control')
    os.write(fd2, b'from-second\n')
    wait_output(fd, b'ECHO:from-second')
    os.write(fd2, b'\x1b[24~'); drain(fd2)
    assert h.join(pid2) == 0
    os.write(fd, b'\x02d')
    end = wait_output(fd, b'\x1b[23;2t')
    assert b'\x1b[?1049l' in end and b'\x1b[?25h' in end
    assert end.rfind(b'\x1b[23;2t') > end.rfind(b'\x1b[?1049l'), end
    # Slave termios are visible from the master after attach restores them.
    restored = termios.tcgetattr(fd)
    assert restored[3] & termios.ICANON and restored[3] & termios.ECHO, (initial_state, restored)
    assert h.join(pid) == 0
    assert h.show(task)['health'] == 'online'
    h.cli('rename', task, '代码会话')
    pid, fd = h.spawn_tty('attach', task)
    renamed = drain(fd)
    label = ('TaskDock · 代码会话 ['+task[:8]+'] · Shared control').encode()
    assert b'\x1b]2;'+label+b'\x07' in renamed and '代码会话 ['.encode() in renamed and b'RSS' in renamed, renamed
    os.write(fd, '中文\n'.encode())
    assert 'ECHO:中文'.encode() in drain(fd)
    # Killing the terminal frontend simulates a lost local/SSH connection.
    os.kill(pid, signal.SIGKILL)
    assert h.join(pid) == -signal.SIGKILL
    until(lambda: h.show(task)['health'] == 'online')
    pid, fd = h.spawn_tty('attach', task)
    drain(fd)
    os.write(fd, b'quit\n')
    drain(fd, .3)
    assert h.join(pid) == 7
    assert h.finished(task)['runtime']['exit_code'] == 7
    print('PASS real tty session name/short ID/title cleanup/rename, attach/detach/kill/reattach, default shared control, Unicode, termios and exit 7', flush=True)


def test_protocol_and_transparency(h):
    script = 'import os,tty;tty.setraw(0);os.write(1,b"READY");\nwhile True:\n p=os.read(0,65536)\n if not p:break\n os.write(1,p)'
    task = h.create('bytes', sys.executable, '-c', script)
    until(lambda: b'READY' in h.cli('logs', task, '--raw').stdout)
    conn, kind, payload = h.socket(task)
    assert kind == 2, payload
    data = b'\x00\xff\x03\x1b[31m\x1b[?1049h\x1b[<0;10;12M\x1b[200~'+ '中文'.encode()+b'\x1b[201~'
    frame(conn, 3, data)
    result = b''
    while len(result) < len(data):
        kind, part = receive(conn)
        assert kind == 4, (kind, part)
        result += part
    assert result == data, (result, data)
    frame(conn, 6)
    conn.close()
    # Wrong instance cannot connect. A control client cannot inject terminal input.
    conn, kind, _ = h.socket(task, 'control', '0'*32)
    assert kind == 8
    conn.close()
    conn, kind, _ = h.socket(task, 'control')
    assert kind == 2
    frame(conn, 3, b'invalid')
    assert receive(conn)[0] == 8
    conn.close()
    h.cli('stop', task)
    print('PASS byte-for-byte ANSI/mouse/paste/binary/Unicode, peer paths and protocol roles', flush=True)


def test_resize_interrupt_and_noescape(h):
    script = 'import os,signal,struct,fcntl,termios,time\ndef show(*a):\n r,c,_,_=struct.unpack("HHHH",fcntl.ioctl(0,termios.TIOCGWINSZ,b"\\0"*8));os.write(1,("SIZE:%d:%d\\n"%(r,c)).encode())\nsignal.signal(signal.SIGWINCH,show);print("READY",flush=True)\nwhile True:time.sleep(1)'
    task = h.create('resize', sys.executable, '-c', script)
    until(lambda: b'READY' in h.cli('logs', task).stdout)
    pid, fd = h.spawn_tty('attach', task)
    wait_output(fd, b'resize [')
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', 37, 103, 0, 0))
    os.kill(pid, signal.SIGWINCH)
    wait_output(fd, b'SIZE:36:103')
    os.write(fd, b'\x1dd');drain(fd);assert h.join(pid) == 0
    h.cli('stop', task)
    task = h.create('interrupt', '/bin/sh', '-c', 'exec sleep 30')
    pid, fd = h.spawn_tty('attach', task)
    wait_output(fd, b'interrupt [');os.write(fd, b'\x03');drain(fd)
    assert h.join(pid) == 130
    task = h.create('noescape', '/bin/sh', '-c', 'stty raw -echo; printf READY; dd bs=1 count=4 2>/dev/null | od -An -tx1')
    until(lambda: b'READY' in h.cli('logs', task).stdout)
    pid, fd = h.spawn_tty('attach', '--no-escape', task)
    wait_output(fd, b'noescape [');os.write(fd, b'\x02d\x1dd');out=wait_output(fd, b'02 64 1d 64', normalize_whitespace=True)
    assert b'02 64 1d 64' in b' '.join(out.split()), out
    assert h.join(pid) == 0
    print('PASS resize/SIGWINCH, Ctrl+C and disabled detach escape', flush=True)


def test_jobs_crash_and_concurrency(h):
    task = h.create('jobs', '/bin/sh', '-c', 'sleep 30 & echo JOB:$!; wait')
    until(lambda: b'JOB:' in h.cli('logs', task).stdout)
    job = int(h.cli('logs', task).stdout.split(b'JOB:')[1].split()[0])
    h.cli('stop', task)
    def gone():
        try: os.kill(job, 0);return False
        except ProcessLookupError:return True
    until(gone)
    forced = h.create('ignore-term', sys.executable, '-c', 'import signal,time;signal.signal(signal.SIGTERM,signal.SIG_IGN);print("READY",flush=True);time.sleep(30)')
    until(lambda: b'READY' in h.cli('logs', forced).stdout)
    h.cli('stop', forced)
    assert h.show(forced)['runtime']['exit_code'] == 137
    task = h.create('crash', '/bin/sh', '-c', 'read value')
    record = h.show(task)
    os.kill(record['runtime']['broker_pid'], signal.SIGKILL)
    until(lambda: h.show(task)['health'] == 'unreachable')
    def crashed_child_gone():
        try:os.kill(record['runtime']['child_pid'],0);return False
        except ProcessLookupError:return True
    until(crashed_child_gone)
    h.cli('recover', task)
    assert h.show(task)['runtime']['state'] == 'failed'
    h.cli('delete', task)
    def create(i):
        return h.create('parallel-'+str(i), '/bin/sh', '-c', 'sleep 20')
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
        tasks = list(pool.map(create, range(16)))
        list(pool.map(lambda task: h.cli('stop', task), tasks))
    assert all(h.show(task)['health'] == 'finished' for task in tasks)
    # Only one concurrent creator may reserve the same user-visible name.
    def same(_):return h.cli('new','-d','-s','same-name','--','/bin/sh','-c','sleep 20',check=False).returncode
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        codes=list(pool.map(same,range(4)))
    assert codes.count(0)==1,codes
    h.cli('stop','same-name')
    print('PASS session job cleanup, broker SIGKILL/recover, 16 tasks and concurrent name reservation', flush=True)


def test_logs_and_upgrade(h):
    script = 'import os;os.write(1,b"X"*(18*1024*1024));os.write(1,b"\\x1b]52;c;SECRET\\x07\\x1b[31mEND\\x1b[0m\\n")'
    task = h.create('large-output', sys.executable, '-c', script)
    h.finished(task)
    files=list((h.state/'tasks'/task).glob('output.log*'))
    assert sum(p.stat().st_size for p in files)<=16*1024*1024
    raw=h.cli('logs',task,'--raw','--tail','100').stdout
    safe=h.cli('logs',task,'--tail','100').stdout
    assert b'\x1b' in raw and b'\x1b' not in safe and b'SECRET' not in safe and b'END' in safe
    task = h.create('follow',sys.executable,'-c','import time;print("ONE",flush=True);time.sleep(.3);print("TWO",flush=True)')
    followed=h.cli('logs','--follow','--tail','0',task).stdout
    assert followed.count(b'ONE')==1 and followed.count(b'TWO')==1,followed
    # Replacing the binary path must not change any running broker identity.
    install=h.root/'install';install.mkdir()
    target=install/'tdock';shutil.copy2(BINARY,target)
    original=h.binary;h.binary=target
    task=h.create('upgrade','/bin/sh','-c','read value')
    broker=h.show(task)['runtime']['broker_pid']
    stage=install/'new';shutil.copy2(BINARY,stage);os.replace(stage,target)
    assert h.show(task)['runtime']['broker_pid']==broker and h.show(task)['health']=='online'
    h.cli('stop',task);h.binary=original
    print('PASS high output, bounded rotation, safe/raw logs and atomic binary upgrade',flush=True)


def test_slow_and_malformed_clients(h):
    task=h.create('slow-client',sys.executable,'-c','import os,time;input();os.write(1,b"x"*(12*1024*1024));print("DONE",flush=True);time.sleep(30)')
    conn,kind,_=h.socket(task)
    assert kind==2
    frame(conn,3,b'go\n')
    until(lambda:b'DONE' in h.cli('logs',task,'--tail','100').stdout)
    assert h.show(task)['health']=='online'
    # The broker disconnects a stalled reader rather than blocking task output.
    ended=False
    try:
        while True:
            kind,_=receive(conn)
            if kind==8:ended=True;break
    except (EOFError,ConnectionResetError):ended=True
    finally:conn.close()
    assert ended
    conn,kind,_=h.socket(task,'control')
    assert kind==2
    conn.sendall(b'MT\x01\x03\xff\xff\xff\xff')
    try:
        assert conn.recv(1)==b''
    except ConnectionResetError:pass
    conn.close()
    assert h.show(task)['health']=='online'
    h.cli('stop',task)
    print('PASS slow consumer eviction and oversized untrusted frame rejection',flush=True)


def test_tui(h):
    with tempfile.TemporaryDirectory(prefix='tdock-tui-') as root:
        ui=Harness(root,h.binary)
        try:
            pid,fd=ui.spawn_tty()
            wait_output(fd,b'TaskDock')
            os.write(fd,b'n');drain(fd)
            os.write(fd,b'tui-created\r');wait_output(fd,b'tui-created [')
            until(lambda:ui.show('tui-created')['health']=='online')
            created=ui.show('tui-created')['spec']['id']
            os.write(fd,b'printf "TUI-OK\\n"\r')
            assert b'TUI-OK' in drain(fd,.3)
            os.write(fd,b'\x02d');wait_output(fd,b'TaskDock')
            os.write(fd,b'\x1bOQ');drain(fd)
            os.write(fd,b'\x01\x0btui-renamed\r');drain(fd,.3)
            until(lambda:ui.show(created)['spec']['name']=='tui-renamed')
            os.write(fd,b's');drain(fd,.2)
            assert ui.show('tui-renamed')['health']=='online'
            os.write(fd,b'\x1b');drain(fd,.15)
            assert ui.show('tui-renamed')['health']=='online'
            os.write(fd,b's');drain(fd,.15)
            os.write(fd,b'y');drain(fd,.2)
            until(lambda:ui.show('tui-renamed')['health']=='finished')
            ui.cli('stop','tui-renamed');drain(fd,.3)
            os.write(fd,b'\x1b[C');drain(fd,.2)
            os.write(fd,b'\x1b[3~');drain(fd)
            os.write(fd,b'y');drain(fd,.3)
            until(lambda:json.loads(ui.cli('ls','--json').stdout)==[])
            os.write(fd,b'\x1b');drain(fd);assert ui.join(pid)==0
        finally:ui.cleanup()
    print('PASS TUI new/enter/detach, rename, stop confirmation/cancellation, delete confirmation and quit',flush=True)


def test_bash_and_vim(h):
    bash=shutil.which('bash')
    if bash:
        task=h.create('bash-compat',bash,'--noprofile','--norc')
        pid,fd=h.spawn_tty('attach',task);drain(fd)
        os.write(fd,b'printf "BASH-OK\\n"\r')
        assert b'BASH-OK' in drain(fd,.2)
        os.write(fd,b'exit\r');drain(fd);assert h.join(pid)==0
    vim=shutil.which('vim')
    if vim:
        target=h.root/'vim-中文.txt'
        task=h.create('vim-compat',vim,'-Nu','NONE','-n','-i','NONE','-N','-c','set encoding=utf-8',str(target))
        pid,fd=h.spawn_tty('attach',task);drain(fd,.3)
        os.write(fd,'i第一行\n第二行'.encode()+b'\x1b')
        drain(fd)
        os.write(fd,b'\x1dd');drain(fd);assert h.join(pid)==0
        pid,fd=h.spawn_tty('attach',task);drain(fd,.3)
        os.write(fd,'A 尾巴'.encode()+b'\x1b:wq\r');drain(fd,.3)
        assert h.join(pid)==0
        assert target.read_text()=='第一行\n第二行 尾巴\n',target.read_text()
    print('PASS available Bash interaction and Vim Unicode edit across detach/reattach (visual acceptance separate)',flush=True)


def main():
    assert BINARY.is_file(), 'build bin/tdock first'
    with tempfile.TemporaryDirectory(prefix='tdock-integration-') as root:
        harness=Harness(root)
        try:
            for test in (test_lifecycle,test_foreground_startup,test_attach,test_protocol_and_transparency,
                         test_resize_interrupt_and_noescape,test_jobs_crash_and_concurrency,
                         test_logs_and_upgrade,test_slow_and_malformed_clients,test_tui,test_bash_and_vim):
                test(harness)
            for log in harness.state.glob('tasks/*/broker.log'):
                assert b'DATA RACE' not in log.read_bytes(),log
        finally:
            harness.cleanup()
    print('All integration checks passed. Real SSH, other terminal emulators and Linux installation are separate acceptance checks.',flush=True)

if __name__=='__main__':main()
