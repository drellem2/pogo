#!/usr/bin/env bash
# Run the staleness witness FROM SOURCE, never from an installed binary (mg-dd49).
#
# THE RECURSION THIS EXISTS TO BREAK. `pogo check-staleness` detects that the
# box's installed artifacts have fallen behind what shipped. It is a subcommand
# of `pogo`, and `pogo` only becomes current when the nightly redeploy runs — the
# exact mechanism whose failure it detects. So a witness reachable only through
# the installed binary cannot be switched on until the fault it detects is
# already fixed, which is not yet a detector. On 2026-08-05 that was not
# hypothetical: the installed `pogo` was six days and 52 commits stale, so the
# subcommand did not exist on the box at all while all nine shipped prompts and
# the daemon were behind main.
#
# mg-2894's rule, applied literally: a tracked gate should self-activate FROM
# SOURCE and never call the installed binary, because tracked files go live at
# MERGE and compiled binaries only at DEPLOY. This file is tracked, so it is
# present in every checkout at the merge commit, and it compiles the code sitting
# beside it. A `git pull` arms the witness; no `go install`, no launchd, no
# redeploy. That is the whole point of the file — it is deliberately thin.
#
# It is NOT a second implementation. Every judgement lives in internal/staleness
# and cmd/pogo/checkstaleness.go; a shell reimplementation would be a second copy
# of the decision, free to drift from the one under test, and the failure it
# would hide is the one where the two disagree.
#
# Usage — all flags are passed through to `pogo check-staleness`:
#
#   scripts/check-staleness.sh
#   scripts/check-staleness.sh --skip-prompts --stamp /tmp/stale.stamp
#
# Exit status is the witness's own: 0 clean, 1 anything reported. A failure to
# RUN also exits non-zero, because a check that could not run has not found its
# subject healthy.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

# The module root is resolved from THIS FILE's location, not from $PWD and not
# from PATH. A checkout is what carries the code; the directory an operator or a
# cron happened to be standing in is not.
if [ ! -f "$REPO_ROOT/go.mod" ]; then
    echo "check-staleness: no go.mod at $REPO_ROOT — this script must stay inside the pogo checkout it builds from" >&2
    exit 2
fi

# `go` is a hard requirement and its absence is reported as such. Falling back to
# an installed `pogo` here would reintroduce the exact dependency the file exists
# to remove, and it would do it silently: the run would succeed, against
# whatever revision the last deploy happened to leave behind.
if ! command -v go >/dev/null 2>&1; then
    echo "check-staleness: 'go' is not on PATH, so the witness cannot be built from source." >&2
    echo "  This script will NOT fall back to an installed 'pogo': the installed binary is" >&2
    echo "  only as current as the last redeploy, and a redeploy that did not happen is" >&2
    echo "  precisely what this witness is for." >&2
    exit 2
fi

# `cd` into the checkout so `./cmd/pogo` resolves there, and pass everything
# through. The witness's own --repo default (the deploy checkout, else the
# working directory) is unaffected: what is built from source is the DETECTOR,
# not the reference it compares against.
cd "$REPO_ROOT" || exit 2
exec go run ./cmd/pogo check-staleness "$@"
