# Testing

Tests use temporary tasks and state directories. Existing tasks are left alone.
Requires Go 1.26+, Python 3 and pyte for screen checks.

```sh
make check
python3 -m venv .venv
.venv/bin/pip install -r scripts/requirements-screen.txt
make acceptance PYTHON=.venv/bin/python
```

The suite stops on failure and saves platform, version and per-test output to
`dist/acceptance/local.json`. Reports and binaries are excluded from Git.
For a race-enabled run, build to a fresh path and pass it to
`scripts/acceptance.py --binary /path/to/tdock`.

## Suites

| Script | Coverage |
| --- | --- |
| integration.py | Lifecycle, bytes, Unicode, signals, jobs, logs, upgrades and malformed clients |
| screen_regression.py | Reconnect redraw, terminal modes, htop and outer shell preservation |
| ui_regression.py | Five languages, tabs, settings, metrics and stop confirmation |
| connection_modes_regression.py | Read-only enforcement, takeover, shared input and size ownership |
| tmux_cli_regression.py | Session aliases/flags, concurrent reuse, quoting, exit codes and scoped kill |
| monitor_control_regression.py | Immediate input, typeahead, Unicode/paste, switch routing and Vim editing |
| search_regression.py | New-session monitor, long lists, search input isolation, history and ignored Ctrl+Enter |
| monitor_dialog_regression.py | Preview, resize, busy/retry, dialogs and name conflicts |
| history_view_regression.py | History scrolling, empty output and OSC filtering |
| history_regression.py | Expiry policies and active-task protection |
| tui_exit_regression.py | Ctrl+C after monitor detach; INT/TERM/HUP cleanup across five pages |
| detach_keys_regression.py | F12, both Ctrl+B forms, standalone Ctrl+D/Esc, paste and no-escape |
| stress.py | Concurrent health scans and broker RSS |
| idle_cpu_regression.py | Idle CPU guard for fullscreen, read-only, list and monitor |
| installer_test.py | Platform selection, checksums, pinned versions and failed-upgrade protection |

Run a focused suite with `.venv/bin/python scripts/NAME.py ./bin/tdock`.
The default soak uses 16 tasks for five seconds. For a longer run:

```sh
.venv/bin/python scripts/stress.py --tasks 32 --seconds 3600
```

## Optional checks

`scripts/app_smoke.py ./bin/tdock` starts installed Codex/OpenCode without model
requests and checks PIDs across attach rounds. It skips missing executables;
screen layout and interactive workflows need separate testing.

`screen_regression.py` and `monitor_dialog_regression.py` accept an old binary as
a second argument. The old-broker negative checks in
`connection_modes_regression.py` require a pre-v0.3.0 binary without mode support.

See [compatibility](compatibility.md) for tested platforms and
[GitHub Actions](https://github.com/raywcm/taskdock/actions) for hosted runs.
