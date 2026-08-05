#!/bin/bash
# Every step below must fail the whole script. Without set -e (and with the
# old `echo && cmd` chaining), the script's exit status was only the LAST
# command's — a failing `go test` was silently masked and the refinery gate
# merged a branch whose tests panicked (mg-59d5).
set -e

echo "Step 2: Testing..."
echo "Making test directories"
mkdir -p _testdata/a-service/.git
mkdir -p _testdata/b-service/.git

echo "Testing Go packages"
go test ./...

echo "Testing neovim plugin"
bash nvim/test_nvim.sh

echo "Testing bash shell integration"
bash shell/bashrc_test.sh

echo "Testing pogo-self-deploy driver"
bash scripts/pogo-self-deploy_test.sh

# The packaged test isolation itself (mg-78a5). FOUR tickets were filed for one
# defect — a test reading the developer's live ~/.pogo, live daemon or live fleet
# (mg-6092, mg-e8e7, mg-5336, mg-3412) — because every suite re-derived its
# isolation by hand. scripts/pogo-sandbox is that envelope packaged; this file
# breaks each of its guarantees on purpose and asserts the result comes back as a
# SETUP failure with no PASS:, no FAIL: and no PROVED: line. It runs BEFORE the
# live suite because the live suite now depends on it: an isolation that is
# broken should be reported here, by name, rather than as a live-control
# cascade.
echo "Testing the packaged test isolation (scripts/pogo-sandbox)"
bash scripts/pogo-sandbox_test.sh

# The live control for the mg-de08 mail-check post-check (mg-c02d). Stands up a
# sandboxed pogod and drives the ASSEMBLED verify path — the unit test above
# only proves the pure classifier can fail. Costs ~40s (pogod holds its
# mail-check reap for 30s after boot); that is the price of the only assertion
# that shows the redeploy post-check can go RED at all, and mg-f206's unattended
# nightly redeploy rests on it.
echo "Testing pogo-self-deploy live mail-check control"
bash scripts/pogo-self-deploy_live_test.sh

# The control on the control's SANDBOX (mg-3412). The live suite above is only
# worth its isolation, and under four concurrent polecats that isolation failed:
# it drove another run's daemon and reported 14 assertion failures — including
# verbatim fail-open findings — about a tree that was provably fine. This file
# breaks the sandbox on purpose and asserts the result comes back as a SETUP
# failure with no PASS:, no FAIL: and no PROVED: token, so a broken sandbox can
# never again be read as a regression (or hand do_prove a deploy gate). It runs
# AFTER the live suite deliberately: that run passing is the positive direction
# this file therefore does not have to pay 60s to restate.
echo "Testing the live control's sandbox isolation and setup-failure reporting"
bash scripts/pogo-self-deploy_live_setup_test.sh

# The pogod condition annunciator's live controls (mg-342d). The MERGE-TIME
# SUBSET — the negative control plus A2, the enumeration's own highest severity —
# because those are the two that decide whether any of the rest mean anything, and
# the full file is 15 daemon boots.
#
# NEG first, and it is not a formality: every positive control in that file also
# passes against an annunciator that mails unconditionally, so without a clean
# boot proving silence the other twenty assertions are uninterpretable. A2 then
# forces a corrupt schedules.json and asserts the notice ARRIVES in a real
# maildir, is SUPPRESSED on the next boot with the condition still live, is
# CLEARED when it resolves, mails again immediately on recurrence, and — with the
# scheduler confirmed down — that the coordinator is actively WOKEN.
#
# It is here rather than only on demand because mg-342d's whole subject is alarms
# nobody reads, and a control nobody runs is the same defect wearing a test's
# clothes. The remaining rows (A4/A7/A11, A5, A9, A10, A14) are run with
# `scripts/pogo-condition-controls.sh` — ~3 minutes, not every merge.
echo "Testing the pogod condition annunciator (live controls: negative + A2)"
bash scripts/pogo-condition-controls.sh NEG A2

# The deploy script's SIGINT interrupt-safety control (mg-e201). Relocated OUT of
# the live_test.sh artifact gate (do_prove's comsub) because it tests the DEPLOY
# SCRIPT's INT trap, not the pogod detector, and its own-process-group Ctrl-C model
# only holds in this DIRECT context, not inside do_prove's `out="$(bash ...)"`.
echo "Testing pogo-self-deploy SIGINT interrupt-safety control"
bash scripts/pogo-self-deploy_sigint_test.sh

# The nightly redeploy TRIGGER (mg-42ac). Pure-helper tests only — sourcing the
# runner cannot fire a deploy, and every case here is a refusal: the two skips
# (outside-window, no-drift) and the aborts (dirty tree, diverged tree, no
# token). All of them fail the same visible way — a nightly that deploys
# nothing — so the suite exists to tell them apart.
echo "Testing pogo-deploy nightly trigger"
bash scripts/pogo-deploy_test.sh

# The FROM-SOURCE runner for the staleness witness (mg-dd49). The judgement is
# tested in internal/staleness; what this suite holds is the property that makes
# the runner worth having — it must never fall back to an installed `pogo`. The
# witness detects that installed artifacts have fallen behind source, and `pogo`
# only becomes current when the redeploy runs, so a fallback would report
# whatever the last successful deploy left behind. The load-bearing case is
# section 2: a POISONED `pogo` first on PATH, asserted both by its marker file
# and by the exit status, because either alone passes against a fallback that
# happens to agree.
echo "Testing the from-source staleness runner"
bash scripts/check-staleness_test.sh

echo "Testing build.sh"
bash build_test.sh

echo "Testing changelog fragment assembler"
bash scripts/assemble-changelog_test.sh

# Changelog coverage (mg-7904). The assembler's LOUD-EMPTY guard checks a weaker
# property (non-empty) than CONTRIBUTING's rule (a fragment per change), so it
# passes while a release ships describing only part of itself. The load-bearing
# case here is the POSITIVE CONTROL: the check is shown to FAIL on a range with
# a known-missing fragment before any passing case is trusted.
echo "Testing changelog coverage check"
bash scripts/changelog-coverage_test.sh

# Release-roll + link references (mg-cef7). Two silent, recurring release-path
# defects: update_changelog() emitted the `## [X.Y.Z]` heading with NO
# `[X.Y.Z]:` compare link (three cuts in a row — Markdown renders the unlinked
# version as LITERAL TEXT, so it degrades a published artifact and reads as a
# typo), and its unanchored sed also injected a spurious heading into any entry
# whose prose mentions `## [Unreleased]` (the heading count rose by TWO per cut).
# The load-bearing case is Test 9: on the input that actually occurred, the
# set-based check must report DUPLICATE HEADINGS and must NOT report missing link
# references — the count check's misdiagnosis, whose obvious remedy would have
# entrenched the corruption by giving the spurious headings link targets.
echo "Testing changelog release-roll and link references"
bash scripts/roll-changelog_test.sh

# The work-item scope guard (mg-f1d5). Every case runs against a stub `mg` and a
# fixture worktree in a temp dir, so the suite never reads the developer's live
# ~/.macguffin. The load-bearing case is the opt-in one: a guard that blocked an
# agent nobody opted in for would be ripped out of every fleet within the hour.
echo "Testing work-item scope guard"
bash scripts/mg-scope-guard_test.sh
