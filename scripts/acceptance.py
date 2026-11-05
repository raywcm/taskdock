#!/usr/bin/env python3
"""Run the isolated process/terminal acceptance suite and save an evidence report."""
import argparse
import datetime
import json
import pathlib
import platform
import subprocess
import sys
import time

SCRIPTS = pathlib.Path(__file__).resolve().parent
ROOT = SCRIPTS.parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='bin/tdock')
    parser.add_argument('--legacy-binary', help='optional mtask v0.5.2 binary for upgrade checks')
    parser.add_argument('--report', default='dist/acceptance/local.json')
    args = parser.parse_args()
    binary = pathlib.Path(args.binary).resolve()
    tests = [
        ('lifecycle', ['integration.py']),
        ('tmux-cli', ['tmux_cli_regression.py']),
        ('monitor-control', ['monitor_control_regression.py']),
        ('monitor-parser', ['monitor_panic_regression.py']),
        ('multi-terminal', ['multi_terminal_regression.py']),
        ('automatic-theme', ['theme_regression.py']),
        ('application-colors', ['color_regression.py']),
        ('stale-deletion', ['stale_delete_regression.py']),
        ('session-search', ['search_regression.py']),
        ('idle-cpu', ['idle_cpu_regression.py']),
        ('screen', ['screen_regression.py']),
        ('ui', ['ui_regression.py']),
        ('connections', ['connection_modes_regression.py']),
        ('monitor', ['monitor_dialog_regression.py']),
        ('history-view', ['history_view_regression.py']),
        ('history-retention', ['history_regression.py']),
        ('tui-exit', ['tui_exit_regression.py']),
        ('detach-keys', ['detach_keys_regression.py']),
        ('bounded-soak', ['stress.py', '--binary', str(binary), '--tasks', '16', '--seconds', '5']),
    ]
    if args.legacy_binary:
        tests.append(('rename-upgrade', ['rename_regression.py', str(binary), str(pathlib.Path(args.legacy_binary).resolve())]))
    report = {
        'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
        'os': platform.system(), 'release': platform.release(), 'arch': platform.machine(),
        'version': subprocess.check_output([str(binary), '--version'], text=True).strip(),
        'python': platform.python_version(), 'tests': [],
        'scope': 'Temporary state and real PTYs. Automated cell-model checks do not certify all GUI terminals or full AI workflows.',
    }
    destination = pathlib.Path(args.report)
    destination.parent.mkdir(parents=True, exist_ok=True)
    for name, command in tests:
        script, *extra = command
        argv = [sys.executable, str(SCRIPTS / script)]
        argv += extra if extra else [str(binary)]
        start = time.monotonic()
        print('RUN ' + name, flush=True)
        try:
            result = subprocess.run(argv, cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=240)
            output, code = result.stdout, result.returncode
        except subprocess.TimeoutExpired as err:
            output, code = str(err), 124
        print(output, end='' if output.endswith('\n') else '\n', flush=True)
        report['tests'].append({'name': name, 'exit_code': code, 'seconds': round(time.monotonic() - start, 2), 'output': output})
        destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        if code:
            raise SystemExit(code)
    print('PASS all acceptance suites; evidence saved to ' + str(destination), flush=True)


if __name__ == '__main__':
    main()
