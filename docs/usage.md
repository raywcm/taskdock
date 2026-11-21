# Usage

## Create and reconnect

```sh
tdock new -s work -- bash
tdock new -ds build -- make test
tdock new -c /absolute/project -s editor -- vim README.md
tdock attach work
```

Interactive `new` enters the task immediately. `-d`, `--json` or non-terminal input
starts it in the background. Omit the command to use `$SHELL` or `/bin/sh`.
A single quoted command runs through `$SHELL -c`, for example
`tdock new -ds build 'make test && make build'`. Multiple arguments run directly
as argv. Use `--` before a command to keep even a single argument literal.
Place tdock flags before the command; remaining flags belong to the application.
Tasks accept names, full IDs or unique ID prefixes of at least four characters.

## Attached status row

Fullscreen sessions show their name, short ID and process-tree RSS in a fixed
bottom row. Memory and renames refresh every two seconds. The size owner gives
the application one fewer row. Followers crop the owner's screen and keep their
own status row. A follower shows the application's cursor when it falls inside
the visible part of the screen. The status row follows the chosen theme and
color mode. Terminal keeps the terminal’s default background.

With cell-based mouse tracking, the status row ignores clicks and wheel events.
Dragging from the application into the status row releases at the edge of the
application. With classic mouse reporting, a drag beyond row or column 223
releases at the last coordinate the application can receive. Pixel tracking
uses the visible bounds when the terminal reports its pixel dimensions. Without those dimensions, pixel reports pass through.

The status view renders a bounded VT screen in the alternate buffer. Use
`tdock --no-status` for native forwarding and host-terminal scrollback, or
`tdock attach --no-status work` for one connection. Views above 512 columns or
257 rows start in native mode.

Starting another manager, `new` or `attach` from inside a TaskDock session is
blocked. Use Ctrl+Q or F12 to return to the list. Management commands such as
`ls`, `logs`, `rename` and `stop` remain available inside a session.

## tmux session commands

| Command | Supported flags / aliases |
| --- | --- |
| `new-session` | `new`, `-s NAME`, `-c DIRECTORY`, `-d`, `-A` |
| `list-sessions` | `ls`, `--json`, `--history` |
| `attach-session` | `attach`, `a`, `-t SESSION`, `-r`, `--shared`, `-d` / `--takeover` |
| `rename-session` | `rename`, `-t SESSION NAME` |
| `has-session` | `has`, `-t SESSION`; exit 0 if running, 1 otherwise |
| `kill-session` | `-t SESSION`; stops the task and removes its history |

`new -As work` attaches to a running named session or creates it. Add `-d` to
leave it detached. An ended record keeps its name; inspect or delete it first.
Targets accept names, IDs or unique ID prefixes; positional targets still work.
Supply a session target explicitly. TaskDock does not interpret tmux window or
pane targets or tmux format strings. `attach -d -t NAME` takes control of the
session, detaches other controllers and keeps read-only viewers connected.
Listing includes retained stopped records.

Existing scripts should replace `list` with `ls`, `status` with `show`, `rm` with
`delete`, and `new -n` with `new -s`. `show` always prints JSON; its redundant
`--json` flag was removed. Use `stop` when you want to retain output history;
`delete` removes a stopped record.

## View existing tmux sessions

Press `t` in the TaskDock session list to browse the current tmux server. Select a
session with the arrow keys and press Enter to preview its active pane. Use ↑/↓
or PgUp/PgDn to scroll within the captured pane and ←/→ to read wide lines.
The wheel and trackpad also scroll when the captured pane exceeds the preview.
Home shows the top left. End follows the bottom as the preview refreshes once
a second. Esc returns to the list. Browsing requires the tmux
executable and access to the server as the current user.

```sh
tdock tmux ls --json
tdock tmux capture -t work
tdock tmux view -t work
tdock tmux -L build ls
tdock tmux -S /path/to/socket capture -t work
```

`view` opens tmux's own read-only client with its native pane layout. Use the
tmux detach binding to leave (by default Ctrl+B, then d). Inside tmux, use
TaskDock's preview or `capture` to inspect another session. Preview and capture
leave pane dimensions alone. On older versions such as tmux 3.0a, tmux can
resize a window to fit a native read-only client.

## Detach keys

- **Ctrl+Q** detaches. On Mac, use Control+Q.
- **F12** also detaches. Mac keyboards may require Fn+F12.

From the manager, detach returns to the list. A direct `tdock attach` returns to
the shell. The task keeps running. Ctrl+D and Ctrl+C retain the application's
behavior. Ctrl+B and Ctrl+] pass directly to the application.

`tdock attach --no-escape TASK` disables Ctrl+Q and F12 handling.
Close the terminal or end the frontend to disconnect in that mode. Bracketed
paste passes through without interpreting detach keys. A standalone Esc waits
up to 50 ms. Recognized mouse report prefixes allow up to 500 ms between input
fragments on a slow connection.

## Manage tasks

The title row shows the main shortcuts; F1/? opens the full list.

```sh
tdock ls --json
tdock show work
tdock rename work work-old
tdock stop work-old
tdock ls --history --json
tdock logs work-old
tdock logs --follow work-old
tdock logs --tail 4096 work-old
tdock delete work-old
```

`stop` sends TERM, then KILL after five seconds if necessary. Set
`TDOCK_STOP_GRACE` when creating a session to use a different duration, such as
`TDOCK_STOP_GRACE=30s tdock new -ds work -- command`. If the broker has
already died, `stop` moves the session to History and retains its output.
TaskDock also checks for lost brokers in the background, even when history
expiry is disabled. The broker lock protects sessions during temporary connection
failures. `show` reports exit code and signal.

If `tdock ls` reports an unreadable task record, use the full ID in the warning
to remove it with `tdock delete ID` once its broker has exited.
Detach returns 0; an application exit is returned by `attach`.

Logs retain two approximately 8 MiB files. Normal viewing strips control
sequences; `logs --raw` writes the original bytes. `--tail` counts bytes.
Stopped records and logs expire after one hour by default:

```sh
tdock settings set history-retention 24h
tdock settings set history-retention off
```

In the History tab, Enter opens retained output. Use the wheel, arrow keys,
PgUp/PgDn or Home/End to navigate. Esc or q returns to the list. Viewing
history does not restart the task.

## Multiple clients

```sh
tdock attach work                # Automatically join shared control
tdock attach work --read-only    # No application input or resize
tdock attach work --shared       # All controllers may type
tdock attach work --takeover     # Disconnect existing controllers; keep readers
```

The flags are mutually exclusive and require v0.3.0+ brokers. The TUI's `c` menu
provides the same modes, with shared control selected by default. Takeover requires
confirmation in the TUI; the CLI flag acts immediately.

The oldest connected controller owns the PTY size; ownership transfers on
leaving. Shared inputs can interfere. Read-only restricts that connection, not
the account's ability to open another management client. All peers use the same
Unix UID. Older brokers retain ordinary exclusive attach.

## Monitor and settings

In the TUI, new sessions open selected in the monitor. In the task list, Enter
opens a session fullscreen; `v` opens the monitor. Ordinary input, Enter, ←/→,
Ctrl+C/D and pasted text in the monitor go to the application. Shift+↑/↓ sends
ordinary application arrow keys. ↑/↓ switches sessions.
Ctrl+Q, F12 or Esc returns to the list. To enter a session, return
to the list first. Ctrl+Enter is ignored throughout the manager and monitor.

The monitor keeps a narrow session list beside a wide control pane. ↑/↓ and Esc
are manager keys and never reach the application; bracketed paste stays literal.
The monitor joins shared control, including when another terminal is attached.
The oldest controller sets the PTY size; other monitors crop the same screen to
their panes. When that controller leaves, the next one applies its own size.
Unavailable tasks display “Unable to monitor”. F5 retries.
Ctrl+Q/F12 or Esc returns to the list. Applications with cell-based mouse
tracking receive wheel, click and drag events relative to their pane. Releasing
a drag outside the pane releases it at the nearest edge. For pixel-based mouse reporting
or native terminal extensions, use `attach --no-status`.

For applications that use the terminal's normal screen without mouse tracking,
use the wheel or trackpad in the session or monitor pane to review up to 1,000
earlier screen lines. Scroll down or type in a session you control to return to live output.

Press **Ctrl+F** in the list or monitor to search running and stopped sessions.
Words match name, ID, directory and command without case sensitivity. ↑/↓ and
PgUp/PgDn navigate results; Enter confirms and Esc cancels. From the list, a
running result opens fullscreen; from the monitor, it changes the monitored
session. Stopped results open retained output. Search input stays out of the application.
Cancelling resumes the same monitor without resizing. The list scrolls with the
selection and supports PgUp/PgDn and Home/End. In the list, Shift+↑/↓ moves the
selected session within its tab. Order is saved atomically across terminals;
renames keep position and newly created sessions append.

Settings shows the installed version. Open the editor with Enter or `,`.
↑/↓ selects a field, ←/→ changes it, Enter saves, Esc cancels. Tab switches tabs
and discards unsaved changes.

Theme controls selection and backgrounds. Classic highlights the selected row.
Terminal keeps your terminal’s background and marks the selection with a `›`,
bright cyan in Dark mode and blue in Light mode. The arrow also works in monochrome.

Color mode applies to either theme. Automatic detects the terminal background
and falls back to `COLORFGBG` when available. Select Dark or Light to override
detection. Session previews and attached applications keep their own colors.

Two terminals can save different settings concurrently. Saving the editor applies
the fields you changed and displays the merged preferences. When both terminals
edit the same field, the last save takes effect.

```sh
tdock settings --json
tdock settings set language en
tdock settings set theme terminal
tdock settings set color-mode light
tdock settings set refresh 2
tdock settings set resources off
tdock settings set default-tab stop
tdock settings reset
```

Themes are `classic` (default) and `terminal`. Color modes are `auto` (default),
`dark`, and `light`. An existing Dark or Light preference opens in Classic with
that color mode. An existing Terminal preference opens in Terminal with automatic
colors. Older commands such as `tdock settings set theme dark` still set the color mode.

Languages include auto, en, zh-CN, ja, es, and fr. Refresh intervals are 1, 2,
or 5 seconds. CPU/RSS cover the task process tree and can count shared memory
more than once.

## Storage and recovery

State directory precedence: `--state-dir`, `TDOCK_STATE_DIR`, then
`$XDG_STATE_HOME/taskdock` or `~/.local/state/taskdock`. Existing installations
reuse their `mtask` directory in place and accept `MTASK_STATE_DIR` as a fallback;
live brokers and history remain accessible without moving files. Directories are 0700; metadata
and logs are 0600. Socket peers verify UID. Arguments and output may contain
sensitive data.

After SSH loss, reconnect to the same host/account and run tdock again. Reboot,
OOM or host login-session cleanup can end tasks. A crashed broker's PTY cannot
be recovered; `tdock recover TASK` reconciles stale records without signalling
old PIDs. Installation replaces the executable atomically and leaves existing
brokers running. See [compatibility](compatibility.md) for terminal limitations.

If TaskDock cannot read the history retention setting, it keeps existing history
and continues recovering lost sessions. Session controls remain available.
Repair `settings.json` or run `tdock settings reset` to restore the defaults.
