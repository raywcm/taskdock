#!/usr/bin/env python3
"""Visible-screen regression for attach/detach, using pyte's VT cell model.

Usage: python scripts/screen_regression.py NEW_BINARY [OLD_BROKER_BINARY]
Install test-only dependencies from scripts/requirements-screen.txt.
"""
import pathlib
import os
import time
import shutil
import sys
import tempfile

import pyte
from integration import Harness,drain


class VTStream(pyte.ByteStream):
    # Bubble Tea can optimize dialog movement with CSI S/T. pyte 0.8.2
    # omits these commands; ignoring them creates false stale-cell failures.
    csi=dict(pyte.ByteStream.csi, S='scroll_up', T='scroll_down')
    events=pyte.ByteStream.events | {'scroll_up','scroll_down'}


class Terminal:
    """Route xterm alternate buffers; pyte supplies CSI/cursor/erase/cell handling."""
    def __init__(self,columns=80,rows=24):
        self.columns,self.rows=columns,rows
        self.main=pyte.Screen(columns,rows)
        self.alt=pyte.Screen(columns,rows)
        self.alternate=False
        self.current=self.main
        self.stream=VTStream(self)

    def __getattr__(self,name):
        value=getattr(self.current,name)
        if callable(value):return lambda *a,**kw:getattr(self.current,name)(*a,**kw)
        return value

    def set_mode(self,*modes,**kwargs):
        normal=[]
        for mode in modes:
            if kwargs.get('private') and mode in (47,1047,1049):
                if not self.alternate:
                    self.main.save_cursor()
                    self.alt=pyte.Screen(self.columns,self.rows)
                self.alternate=True;self.current=self.alt
            else:normal.append(mode)
        if normal:self.current.set_mode(*normal,**kwargs)

    def reset_mode(self,*modes,**kwargs):
        normal=[]
        for mode in modes:
            if kwargs.get('private') and mode in (47,1047,1049):
                if self.alternate:self.main.restore_cursor()
                self.alternate=False;self.current=self.main
            else:normal.append(mode)
        if normal:self.current.reset_mode(*normal,**kwargs)

    def feed(self,data):self.stream.feed(data)

    def select_graphic_rendition(self,*attrs,private=False):
        # CSI ? Ps m queries xterm keyboard mode; it is not an SGR color.
        if not private:self.current.select_graphic_rendition(*attrs)

    def scroll_up(self,count=1,**kwargs):
        screen=self.current
        top,bottom=screen.margins or (0,screen.lines-1)
        saved=screen.cursor.y;screen.cursor.y=bottom
        for _ in range(min(count or 1,bottom-top+1)):screen.index()
        screen.cursor.y=saved

    def scroll_down(self,count=1,**kwargs):
        screen=self.current
        top,bottom=screen.margins or (0,screen.lines-1)
        saved=screen.cursor.y;screen.cursor.y=top
        for _ in range(min(count or 1,bottom-top+1)):screen.reverse_index()
        screen.cursor.y=saved


def verify_scroll_regions():
    query=Terminal(10,6)
    query.feed(b'\x1b[?4m\x1b[31mX\x1b[0m')
    assert query.display[0].startswith('X') and query.current.buffer[0][0].fg=='red'
    terminal=Terminal(10,6)
    for y in range(6):terminal.feed(('\x1b[%d;1HROW-%d'%(y+1,y+1)).encode())
    terminal.feed(b'\x1b[2;5r\x1b[6;3H\x1b[2T')
    assert [row.rstrip() for row in terminal.display]==['ROW-1','','','ROW-2','ROW-3','ROW-6']
    assert (terminal.current.cursor.x,terminal.current.cursor.y)==(2,5)
    terminal.feed(b'\x1b[S')
    assert [row.rstrip() for row in terminal.display]==['ROW-1','','ROW-2','ROW-3','','ROW-6']
    assert (terminal.current.cursor.x,terminal.current.cursor.y)==(2,5)


CURSES_LIKE=r'''
import os,signal,time,tty
# Repaint only changed cells; a same-size SIGWINCH deliberately does not clear
# the app's cached screen. This is the reconnect case missed by byte-only tests.
tty.setraw(0)
size=os.get_terminal_size(0)
def paint():
    os.write(1,b'\x1b[0m\x1b[H\x1b[2J\x1b[1;1HSCREEN-TITLE\x1b[3;1HSTATIC-LABEL')
def resized(*args):
    global size
    latest=os.get_terminal_size(0)
    if latest!=size:size=latest;paint()
signal.signal(signal.SIGWINCH,resized)
os.write(1,b'\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?25l')
paint()
count=0
while True:
    os.write(1,('\x1b[5;1Hcounter:%d\x1b[K'%count).encode());count+=1;time.sleep(.05)
'''


def reenter(h,new_binary,name,argv,expected):
    terminal=Terminal()
    terminal.feed(b'OUTER-SHELL-KEEP\r\nOUTER-PROMPT> ')
    pid,fd=h.spawn_tty('new','--name',name,'--',*argv)
    deadline=time.monotonic()+6
    while time.monotonic()<deadline:
        terminal.feed(drain(fd,.05))
        if terminal.alternate and all(any(label in row for row in terminal.display) for label in expected):break
    assert terminal.alternate,'fixture did not enter its alternate screen'
    assert all(any(label in row for row in terminal.display) for label in expected),terminal.display
    h.binary=new_binary
    for cycle in range(3):
        os.write(fd,b'\x11' if cycle%2==1 else b'\x1b[24~');terminal.feed(drain(fd,.2));assert h.join(pid,on_output=terminal.feed)==0
        assert not terminal.alternate,'detach left alternate screen enabled'
        assert any('OUTER-SHELL-KEEP' in row for row in terminal.main.display)
        before=list(terminal.main.display)
        pid,fd=h.spawn_tty('attach',name)
        deadline=time.monotonic()+6
        while time.monotonic()<deadline:
            terminal.feed(drain(fd,.05))
            if terminal.alternate and all(any(label in row for row in terminal.display) for label in expected):break
        assert terminal.alternate, 'reattach painted app output onto the outer shell'
        assert terminal.main.display==before,'reattach overwrote the outer shell buffer'
        for label in expected:
            assert any(label in row for row in terminal.display),('static cells were not repainted',label,terminal.display)
        if name=='curses-fixture':
            assert terminal.current.cursor.hidden,'hidden cursor was not restored'
            assert 1000<<5 in terminal.current.mode,'mouse tracking was not restored'
            assert 2004<<5 in terminal.current.mode,'paste mode was not restored'
    os.write(fd,b'\x11');terminal.feed(drain(fd,.2));assert h.join(pid,on_output=terminal.feed)==0
    h.cli('stop',name)
    print('PASS',name,'three reentries restore full visible labels/modes and preserve outer shell',flush=True)


def plain_shell(h):
    terminal=Terminal();terminal.feed(b'SHELL-SCROLLBACK-KEEP\r\n')
    pid,fd=h.spawn_tty('--no-status','new','--name','plain','--','/bin/sh','-c','printf PLAIN-PROMPT; read value')
    terminal.feed(drain(fd,.3))
    assert not terminal.alternate,'plain shell was unnecessarily forced into alternate screen'
    assert any('SHELL-SCROLLBACK-KEEP' in line for line in terminal.display)
    os.write(fd,b'\x11');terminal.feed(drain(fd,.2));assert h.join(pid,on_output=terminal.feed)==0
    h.cli('stop','plain')
    print('PASS plain shell retains normal screen and scrollback behavior',flush=True)


def main():
    verify_scroll_regions()
    new_binary=pathlib.Path(sys.argv[1] if len(sys.argv)>1 else 'bin/tdock').resolve()
    old_binary=pathlib.Path(sys.argv[2]).resolve() if len(sys.argv)>2 else new_binary
    with tempfile.TemporaryDirectory(prefix='tdock-screen-') as root:
        h=Harness(root,old_binary)
        try:
            reenter(h,new_binary,'curses-fixture',[sys.executable,'-c',CURSES_LIKE],['SCREEN-TITLE','STATIC-LABEL'])
            htop=shutil.which('htop')
            if htop:
                h.binary=old_binary
                reenter(h,new_binary,'htop-screen',[htop],['Help','Setup','Search'])
            else:print('SKIP real htop: executable unavailable; deterministic screen fixture still tested',flush=True)
            h.binary=new_binary;plain_shell(h)
        finally:h.cleanup()

if __name__=='__main__':main()
