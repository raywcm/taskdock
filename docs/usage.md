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

## tmux session commands

| Command | Supported flags / aliases |
| --- | --- |
| `new-session` | `new`, `-s NAME`, `-c DIRECTORY`, `-d`, `-A` |
| `list-sessions` | `ls`, `--json`, `--history` |
| `attach-session` | `attach`, `a`, `-t SESSION`, `-r`, `--shared`, `--takeover` |
| `rename-session` | `rename`, `-t SESSION NAME` |
| `has-session` | `has`, `-t SESSION`; exit 0 if running, 1 otherwise |
| `kill-session` | `-t SESSION`; stops the task and removes its history |

`new -As work` attaches to a running named session or creates it. Add `-d` to
leave it detached. An ended record keeps its name; inspect or delete it first.
Targets accept names, IDs or unique ID prefixes; positional targets still work.
Targets are explicit: tmux window/pane syntax, formats, implicit current sessions
and `attach -d` are unsupported. Listing includes retained stopped records.

Existing scripts should replace `list` with `ls`, `status` with `show`, `rm` with
`delete`, and `new -n` with `new -s`. `show` always prints JSON; its redundant
`--json` flag was removed. Use `stop` when you want to retain output history;
`delete` removes a stopped record.

## Detach keys

- **F12**: detach; Mac keyboards may require Fn+F12.
- **Ctrl+B, d**: release Ctrl before pressing d.

Compatibility shortcuts are documented here rather than in the interface:
**Ctrl+B, Ctrl+D** (keep Ctrl held while pressing B then D) and **Ctrl+], d**.

From the manager, detach returns to the list. A direct `tdock attach` returns to
the shell. The task keeps running. Standalone Ctrl+D and Ctrl+C retain the
application's behavior. Press a prefix twice to send it literally.

`tdock attach --no-escape TASK` disables all detach shortcuts, including F12.
Close the terminal or end the frontend to disconnect in that mode. Bracketed
paste passes through without interpreting detach keys. Incomplete escape
sequences wait up to 50 ms before being forwarded.

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

`stop` sends TERM, then KILL after one second if necessary. Deletion requires no
active broker. Confirmed dead sessions can be deleted directly, including from
the Running tab, without running `recover` first. A timeout alone cannot bypass
the broker lock. `show` reports exit code and signal.
Detach returns 0; an application exit is returned by `attach`.

Logs retain two approximately 8 MiB files. Normal viewing strips control
sequences; `logs --raw` writes the original bytes. `--tail` counts bytes.
Stopped records and logs expire after one hour by default:

```sh
tdock settings set history-retention 24h
tdock settings set history-retention off
```

In the Stop tab, Enter opens retained output. Use arrows, PgUp/PgDn, Home/End to
navigate; Esc/q returns. Viewing history does not restart the task.

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
ordinary application arrow keys. ↑/↓ switches
sessions; Esc, F12 or Ctrl+B, d returns to the list. To enter a session, return
to the list first. Ctrl+Enter is ignored throughout the manager and monitor.

The monitor keeps a narrow session list beside a wide control pane. ↑/↓ and Esc
are manager keys and never reach the application; bracketed paste stays literal.
The monitor joins shared control, including when another terminal is attached.
The oldest controller sets the PTY size; other monitors crop the same screen to
their panes. When that controller leaves, the next one applies its own size.
Unavailable tasks display “Unable to monitor”; F5 retries and F12/Esc returns to
the list. Embedded
control supports keyboard input; use fullscreen attach for native terminal mouse
behavior and terminal extensions.

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

```sh
tdock settings --json
tdock settings set language en
tdock settings set theme dark
tdock settings set refresh 2
tdock settings set resources off
tdock settings set default-tab stop
tdock settings reset
```

Languages: auto, en, zh-CN, ja, es, fr. Themes: auto, dark, light. Refresh: 1, 2 or
5 seconds. CPU/RSS cover the task process tree; shared memory can be counted more
than once.

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

Automatic theme reads the terminal background and uses the same colors as the
matching Light/Dark setting. COLORFGBG is a fallback for terminals without a
background-color reply; explicit theme choices always take priority.
Themes style the manager UI. Session previews and native attachments keep the
application's colors and terminal defaults.
