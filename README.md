# TaskDock

<table>
  <tr>
    <td width="50%" align="center">
      <strong>TaskDock</strong><br>
      <img src="docs/assets/sessions.png" width="100%" alt="TaskDock session list with CPU and memory usage">
    </td>
    <td width="50%" align="center">
      <strong>Claude Code</strong><br>
      <img src="docs/assets/claude.png" width="100%" alt="Claude Code with TaskDock's session name and memory status row">
    </td>
  </tr>
  <tr>
    <td width="50%" align="center">
      <strong>OpenCode</strong><br>
      <img src="docs/assets/opencode.png" width="100%" alt="OpenCode in TaskDock's split monitor">
    </td>
    <td width="50%" align="center">
      <strong>htop</strong><br>
      <img src="docs/assets/htop.png" width="100%" alt="htop monitoring real processes in TaskDock">
    </td>
  </tr>
</table>

Keep terminal programs running after you close the terminal or lose SSH.
Reconnect from a task list, monitor output, or share control from another
terminal. Linux and macOS, one binary.

Use it for shells, editors, development servers and long-running commands.
Coding agents such as **Codex, Claude Code and OpenCode** work the same way:
they run as their native CLI programs, with their own interface. No plugins,
wrappers or tmux setup.

## Install

```sh
curl -fsSL https://github.com/raywcm/taskdock/releases/latest/download/install.sh | sh
```

Installs to `~/.local/bin` after SHA-256 verification. Supports Linux and macOS
on amd64/arm64. Add `~/.local/bin` to PATH. Existing tasks keep running on upgrade.
See [releases](https://github.com/raywcm/taskdock/releases) for archives and Deb/RPM
packages, or [installation options](docs/installation.md) for a specific version
or installation directory.

### From source

Requires Go 1.26+, Git and make. Go is not needed at runtime.

```sh
git clone https://github.com/raywcm/taskdock.git
cd taskdock
make build
./scripts/install.sh --prefix "$HOME/.local" ./bin/tdock
```

Add `~/.local/bin` to PATH, or run `./bin/tdock` directly.

## Use

For coding agents, use the tools you already have installed:

```sh
tdock new -ds codex -- codex
tdock new -ds claude -- claude
tdock new -ds opencode -- opencode
tdock
```

Select a session and press Enter to open it, or `v` to control it
beside the session list. Sessions keep running when you leave TaskDock.
See [compatibility](docs/compatibility.md) for tested versions and terminal limits.

Shells and other terminal programs work the same way:

```sh
tdock new -s work -- bash
# Press Ctrl+Q to detach. The task keeps running.
tdock
# Select work and press Enter to reconnect.
```

When using SSH, run tdock on the remote host. Reconnect to the same host and
account after a dropped connection. Tasks do not survive a host reboot.

| In the task list | Action |
| --- | --- |
| ↑/↓, Enter | Select and enter a task; stopped tasks open history |
| Tab / Shift+Tab | Switch Running, Stop and Settings tabs |
| n, F2, s, Delete | Create, rename, stop or delete; stop/delete require confirmation |
| v | Open monitor control |
| Shift+↑/↓ | Move the selected session; order is saved across terminals |
| Ctrl+F | Search all sessions by name, ID, directory or command |
| c | Choose takeover, read-only or shared control |
| F5, F1/? | Refresh or show help |
| q / Ctrl+C | Exit the manager without stopping tasks |

Fullscreen sessions have a bottom row with the session name and process-tree
memory use. Use `tdock --no-status` for native forwarding and terminal scrollback.
Starting a nested manager or session inside TaskDock is blocked; `ls`, `logs` and
other management commands remain available.

Inside a task, **Ctrl+Q** or **F12** returns to the list.
On Mac, use Control+Q. F12 may require Fn. Standalone **Ctrl+D** and **Ctrl+C**
go to the application. Use `tdock attach --no-escape TASK` to send all keys,
including Ctrl+Q and F12, to the application.

New sessions created from the TUI open selected in the monitor, with a narrow
session list and a wide control pane. Type
directly, including Enter; use **Ctrl+F** to search, **↑/↓** to switch tasks, and
**Ctrl+Q/F12** to return to the list. Esc also returns from the monitor.
Shift+↑/↓ sends ordinary arrow keys to the application. Enter opens a session
from the list, and `v` opens the monitor. The monitor has no fullscreen shortcut. Ctrl+Enter is ignored.
Default connections share control automatically. Use `c` to choose read-only,
shared control or a confirmed takeover. The first controller sets the size;
followers do not resize the application.

## CLI

```sh
tdock new -ds build -- make test
tdock ls --json
tdock logs --follow build
tdock stop build
tdock delete build
tdock attach -t work -r     # Or --shared / --takeover
tdock settings set history-retention off
```

Common tmux session commands and flags are supported: `new-session`/`new`,
`list-sessions`/`ls`, `attach-session`/`attach`/`a`, `rename-session`/`rename`,
`has-session`/`has`, and `kill-session`. See [usage](docs/usage.md) for the supported
subset and migration notes.

Stopped records and logs expire after **one hour** by default. Each task retains
two output files of about 8 MiB. All connections use the same Unix account.

The status view reconstructs a bounded screen from retained output. Native
`--no-status` reattach restores terminal modes and requests an application redraw.
Host-terminal scrollback belongs to that terminal. Monitoring
shares input across terminals. The oldest controller owns the PTY size; other
monitors fit that screen into their panes without resizing the application.

## Documentation

- [Usage](docs/usage.md) · [Compatibility](docs/compatibility.md)
- [Build and packaging](docs/installation.md) · [Testing](docs/stability.md)
- [Architecture](docs/architecture.md) · [Protocol](docs/protocol.md)
- [Language packs](docs/i18n.md) · [Roadmap](docs/roadmap.md)
- [GitHub Actions](https://github.com/raywcm/taskdock/actions)

[MIT License](LICENSE). Dependency licenses are included in
[third-party notices](THIRD_PARTY_NOTICES.md).
