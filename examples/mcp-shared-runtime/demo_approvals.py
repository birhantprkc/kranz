#!/usr/bin/env python3
"""Approve only the Kranz tools used by the recorded Codex walkthrough."""

import subprocess
import sys
import time


def capture(pane):
    return subprocess.run(
        ["tmux", "capture-pane", "-p", "-t", pane],
        check=False,
        capture_output=True,
        text=True,
    ).stdout


def main():
    pane = sys.argv[1]
    approved = set()
    allowed = ("plan", "restart", "wait")

    while subprocess.run(
        ["tmux", "has-session", "-t", pane.split(":", 1)[0]],
        check=False,
        capture_output=True,
    ).returncode == 0:
        screen = capture(pane)
        for tool in allowed:
            if tool in approved:
                continue
            if f'run tool "{tool}"?' not in screen:
                continue
            time.sleep(0.8)
            subprocess.run(
                ["tmux", "send-keys", "-t", pane, "Down", "Enter"],
                check=True,
            )
            approved.add(tool)
        time.sleep(0.2)


if __name__ == "__main__":
    main()
