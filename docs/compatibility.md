# Compatibility

Updated 2026-11-08

## Platforms

| Platform | Status | Notes |
| --- | --- | --- |
| macOS arm64 | Tested locally | Core terminal and process regression suites passed. |
| Ubuntu 24.04.3 LTS arm64 | Tested on a server | Core terminal and process regression suites passed. SSH disconnect/reconnect was also tested. |
| Linux amd64 | Tested in CI and on servers | Full regression suites run in CI. Status-row and nested-session checks passed on servers. |
| macOS amd64 | Tested on an Intel Mac | Status-row, nested-session and task lifecycle checks passed. |
| Debian / RPM-based distributions | Package contents checked | Native package-manager install, upgrade and removal tests are pending. |

Builds disable CGO and require Go 1.26+. Go is not required at runtime.

## Applications and terminals

| Application / environment | Tests run | Coverage still needed |
| --- | --- | --- |
| POSIX shell / Bash | Input/output, exit codes, Ctrl+C, job cleanup and reconnect | Shell customizations and plugins |
| Vim | Unicode editing, detach, reconnect and saving | Plugins and additional terminal extensions |
| htop | Static screen content after repeated reconnects | Other ncurses applications |
| Incremental full-screen test program | Static cells, terminal modes and reconnect redraw | Additional VT extensions |
| Codex 0.164.0-alpha.11.1 / OpenCode 1.18.30 | Startup, mixed-session reconnect and process identity | Interactive workflows, mouse and keyboard behavior |
| xterm-256color with a real PTY and pyte decoder | Main/alternate buffers, cursor, Unicode and redraw | GUI terminal behavior |
| SSH to Ubuntu ARM64 | Lost client connection, reconnect and process identity | Host-specific login-session cleanup policies |
| Tabby and other GUI terminals | Manual observations and bug reports | Systematic scrollback, selection, clipboard and mouse tests |

The mixed-task check used six tasks and two attach rounds without submitting
model requests. Separate captures verified the startup interfaces shown in the
README.

## Known limitations

- **Host shutdown ends tasks.** Brokers survive terminal closure and SSH loss
  when the host permits background user processes. Reboot, OOM and login-session
  cleanup can terminate them.
- **The status view uses a bounded terminal renderer.** It reserves a bottom row
  and uses an alternate buffer. Unsupported terminal extensions and native
  host scrollback require `tdock --no-status`. Native reattach restores terminal
  modes and requests a redraw. Retained logs can begin partway through output,
  so reconstructed screens may be incomplete.
- **Monitoring changes the PTY size.** The preview uses its pane size and retains
  that size when leaving. Full attach uses the terminal size minus its status
  row. Unreachable tasks display “Unable to monitor.” The preview accepts input
  immediately. ↑/↓ selects a task. Ctrl+Q/F12 or Esc returns to the list.
  Shift+↑/↓ sends ordinary arrow keys to the application.
- **Connection roles are scoped to one Unix account.** Read-only limits that
  connection. The same account can open another management client. Shared inputs
  can interfere. The oldest connected controller determines the PTY size.
- **History has limits.** Stopped records and logs expire after one hour by
  default. Use `tdock settings set history-retention off` to retain them.
  Each task keeps two output files of approximately 8 MiB each.
- **A lost broker cannot recover its PTY.** `recover` reconciles stale metadata
  without signalling old PIDs. Descendants that detached from the managed
  session may need manual cleanup after a broker crash.
- **Existing brokers keep their code after an upgrade.** New tasks use the new
  executable. Ordinary attach works with protocol-v1 brokers. Takeover,
  read-only and shared modes require v0.3.0 or later.
- **SIGKILL prevents frontend cleanup.** Normal Ctrl+C/INT/TERM/HUP exits restore
  terminal state. A killed frontend may leave modes that need a terminal reset.

See [testing](stability.md) for reproduction commands and [usage](usage.md) for
configuration and recovery procedures.

## Coding agents

Codex, Claude Code and OpenCode launch as ordinary terminal commands. No
agent-specific integration is required. The default fullscreen view and monitor
render bounded terminal screens.
`attach --no-status` forwards native output and input.

The README images show the session list with sampled CPU and memory usage,
Claude Code 2.1.284 on Linux, and OpenCode 1.18.30 and htop on macOS. The agent
views show startup and unsent drafts. OpenCode and htop use the split monitor.
Claude Code uses the attached view with the session name and RSS row.

A shared native terminal has one PTY size. Use monitor panes for differently
sized windows. Followers crop the owner’s screen without resizing the program.
The OS or terminal must pass Shift+Up/Down through for those monitor bindings.
