#!/bin/bash
# Every step below must fail the whole script. Without set -e (and with the
# old `echo && cmd` chaining), the script's exit status was only the LAST
# command's — a failing `go test` was silently masked and the refinery gate
# merged a branch whose tests panicked (mg-59d5).
set -e

# Per-step timing for the merge gate (mg-eed9). `gate_step` echoes the same
# label the bare `echo` used to, runs the command in this shell, and returns its
# status unchanged — so `set -e` still aborts on a failing step. The report is
# armed on EXIT rather than appended at the bottom, because the run whose
# profile is most worth having is the one that FAILED, and that run never
# reaches the bottom of this file.
#
# The same EXIT trap also prints how many of the rows below GitHub CI actually
# runs (mg-82a6). That number belongs next to the RESULT, not in a document,
# because the inference it exists to stop is made in the seconds after a gate
# goes red: on 2026-08-14 a single unreproduced failure here, set beside a green
# CI run on the same commit, was reported as MAIN IS RED four minutes before a
# nightly deploy — on a tree that was green throughout. CI runs a MINORITY of
# the rows below, on a different operating system, so the two were never in a
# position to contradict each other (mg-5fc8).
TEST_SH_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$TEST_SH_DIR/scripts/lib/gate-profile.sh"
. "$TEST_SH_DIR/scripts/lib/gate-exit.sh"
gate_profile_begin "test.sh"
trap 'gate_exit_report' EXIT

echo "Step 2: Testing..."
echo "Making test directories"
mkdir -p _testdata/a-service/.git
mkdir -p _testdata/b-service/.git

# A bare `go test ./...` here inherited Go's default 10-minute per-package
# timeout (mg-a465, gh#107), and it reached the merge gate: build.sh runs this
# file, and the refinery gate defaults to ./build.sh. Two things were wrong with
# that, and only one of them is the number:
#
#   the budget      10m was never chosen, it was a default nobody set. On a
#                   host that has swung between load 1.5 and 174, the slowest
#                   package here measures 206-217s and the same package has
#                   gone 81.2s -> 155.1s on load alone.
#   the report      Go implements -timeout BY PANICKING, so raising the number
#                   alone would only move the panic. An overrun and a mass
#                   regression still looked identical, which is #107's actual
#                   complaint.
#
# go-test-budget.sh sets the budget AND classifies the overrun, so a package
# that ran long is reported as a package that ran long. See that file's header
# for why 20m and not the 40m the issue suggests.
# Wrapped in the $TMPDIR leak guard (mg-60eb). The wrapper pins $TMPDIR to a
# private directory for the duration, reports anything the run abandoned there
# by name, and removes it. Two things that were separate are now the same thing:
# the leak measurement no longer runs against a hand-picked slice of packages —
# it rides on the whole-tree run the gate already pays for, so a leak in a
# package nobody thought to list fails here on the day it is written.
#
# mg-de3c's slice was "every internal/testtmp caller", i.e. the packages its own
# fix had touched, so its coverage was a property of the fix rather than of the
# tree. Two TestMains outside it went on leaking one directory per run until
# $TMPDIR reached ~5,000 fixture directories, the volume hit 100%, and this file
# died with Errno 28 in the refinery gate — failing every merge on the host and
# presenting as a defect in whichever branch happened to be running.
gate_step "Testing Go packages" bash scripts/tmpdir-leak-guard.sh bash scripts/go-test-budget.sh ./...

# The $TMPDIR leak guard (mg-de3c). It belongs in the gate rather than on
# demand because the defect it measures is the one that BROKE the gate: at 255
# MiB free, ./build.sh failed here with "no space left on device" and the
# refinery reported a healthy branch as defective, naming specific tests
# (mg-b41f). The cause was our own suites — 37,083 abandoned fixture directories
# and 61 GB in one $TMPDIR, growing with every run.
#
# The load-bearing case is Test 2, the positive control: the check is a COUNT,
# and "the count did not grow" means nothing until the same counting code has
# been shown to report growth. Test 4 is then the ticket's acceptance criterion
# verbatim — count, run a suite that creates fixtures, count, unchanged.
#
# 18.7s measured (rank 7 of 25, 3.4% of the gate): it runs three `go test`
# invocations over a deliberately small slice that still touches every caller,
# rather than the ~300s internal/agent costs in full.
gate_step "Testing the \$TMPDIR leak guard" bash scripts/tmpdir-leak_test.sh

gate_step "Testing neovim plugin" bash nvim/test_nvim.sh

gate_step "Testing bash shell integration" bash shell/bashrc_test.sh

gate_step "Testing pogo-self-deploy driver" bash scripts/pogo-self-deploy_test.sh

# The build stamp (mg-3141). Every locally-built binary reported commit:"" and
# branch:"" — present and empty — because the ldflags lived only in goreleaser,
# which builds none of the binaries this box runs. Four "is the fix live?"
# questions in one night were answered by file mtimes and by inference as a
# result.
#
# It is in the GATE and not on demand because the defect is a build-path defect:
# it cannot be caught by any test of internal/version, which passes whether or
# not a build path sets the flags. This suite builds via build.sh AND via
# pogo-self-deploy's version_ldflags, RUNS each binary, and asks it — a `-X`
# naming a wrong symbol path links cleanly and fails only at runtime. ~9s, two
# of which are its positive control: a build with the flags withheld, required
# to come back RED.
gate_step "Testing the build stamp (pogo/pogod can say what they contain)" bash scripts/stamp_test.sh

# The size-triggered module-cache reclaim (mg-b7c3). Runs here rather than only
# in the Go suite because the decision this job exists for — fire, refuse, or
# defer — lives in the shell script, not in the installer. Its stubs are on PATH,
# so the real script takes real branches and `go clean -modcache` is never
# reached with a real cache behind it.
gate_step "Testing the Go module cache reclaim" bash scripts/pogo-reclaim_test.sh

# The packaged test isolation itself (mg-78a5). FOUR tickets were filed for one
# defect — a test reading the developer's live ~/.pogo, live daemon or live fleet
# (mg-6092, mg-e8e7, mg-5336, mg-3412) — because every suite re-derived its
# isolation by hand. scripts/pogo-sandbox is that envelope packaged; this file
# breaks each of its guarantees on purpose and asserts the result comes back as a
# SETUP failure with no PASS:, no FAIL: and no PROVED: line. It runs BEFORE the
# live suite because the live suite now depends on it: an isolation that is
# broken should be reported here, by name, rather than as a live-control
# cascade.
gate_step "Testing the packaged test isolation (scripts/pogo-sandbox)" bash scripts/pogo-sandbox_test.sh

# The live control for the mg-de08 mail-check post-check (mg-c02d). Stands up a
# sandboxed pogod and drives the ASSEMBLED verify path — the unit test above
# only proves the pure classifier can fail. Costs ~40s (pogod holds its
# mail-check reap for 30s after boot); that is the price of the only assertion
# that shows the redeploy post-check can go RED at all, and mg-f206's unattended
# nightly redeploy rests on it.
gate_step "Testing pogo-self-deploy live mail-check control" bash scripts/pogo-self-deploy_live_test.sh

# The control on the control's SANDBOX (mg-3412). The live suite above is only
# worth its isolation, and under four concurrent polecats that isolation failed:
# it drove another run's daemon and reported 14 assertion failures — including
# verbatim fail-open findings — about a tree that was provably fine. This file
# breaks the sandbox on purpose and asserts the result comes back as a SETUP
# failure with no PASS:, no FAIL: and no PROVED: token, so a broken sandbox can
# never again be read as a regression (or hand do_prove a deploy gate). It runs
# AFTER the live suite deliberately: that run passing is the positive direction
# this file therefore does not have to pay 60s to restate.
gate_step "Testing the live control's sandbox isolation and setup-failure reporting" bash scripts/pogo-self-deploy_live_setup_test.sh

# The merged-not-closed ALERT and the merged-work GATE, against a pogod that is
# NOT a test binary (mg-161a). This is the one control in the tree that cmd/pogod
# structurally cannot host: defaultMergedOpenAlertMail refuses to send under
# testing.Testing(), by construction and correctly — without that guard a `go
# test ./cmd/pogod/...` run manufactures a real fleet alarm in the coordinator's
# real inbox. So every unit test of that path asserts against a STUB installed in
# its place, a green cmd/pogod suite is evidence about the stub, and mg-9d4e's
# alert went ten days merged and unexercised on the strength of it.
#
# It costs ~15s: two trivial merges through a real refinery in a private sandbox.
# It asserts BOTH arms of the delivery (no coordinator mailbox -> the undelivered
# event; registered -> the mail on disk and in `mg mail list`), and it separates
# this alert from the pre-existing filernotify one that fires on the same merges.
gate_step "Testing the merged-not-closed alert and merged-work gate at runtime" bash scripts/mergedopen-runtime_test.sh

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
gate_step "Testing the pogod condition annunciator (live controls: negative + A2)" bash scripts/pogo-condition-controls.sh NEG A2

# The deploy script's SIGINT interrupt-safety control (mg-e201). Relocated OUT of
# the live_test.sh artifact gate (do_prove's comsub) because it tests the DEPLOY
# SCRIPT's INT trap, not the pogod detector, and its own-process-group Ctrl-C model
# only holds in this DIRECT context, not inside do_prove's `out="$(bash ...)"`.
gate_step "Testing pogo-self-deploy SIGINT interrupt-safety control" bash scripts/pogo-self-deploy_sigint_test.sh

# The nightly redeploy TRIGGER (mg-42ac). Pure-helper tests only — sourcing the
# runner cannot fire a deploy, and every case here is a refusal: the two skips
# (outside-window, no-drift) and the aborts (dirty tree, diverged tree, no
# token). All of them fail the same visible way — a nightly that deploys
# nothing — so the suite exists to tell them apart.
gate_step "Testing pogo-deploy nightly trigger" bash scripts/pogo-deploy_test.sh

# The runner-side network positive control (mg-db96). Separate suite because the
# control is a separate artifact — scripts/lib/net-control.sh is a library any
# runner can source, not a piece of the deploy path — and because this suite
# does something none of the others do: it takes this box OFF THE NETWORK
# (sandbox-exec on Darwin, `unshare -rn` on Linux) and requires the control to
# report RED. A positive control that has only ever been seen going green is not
# known to work, so the suite FAILS rather than skipping when it cannot arrange
# an absence of network to test against.
gate_step "Testing the network positive control" bash scripts/net-control_test.sh

# The FROM-SOURCE runner for the staleness witness (mg-dd49). The judgement is
# tested in internal/staleness; what this suite holds is the property that makes
# the runner worth having — it must never fall back to an installed `pogo`. The
# witness detects that installed artifacts have fallen behind source, and `pogo`
# only becomes current when the redeploy runs, so a fallback would report
# whatever the last successful deploy left behind. The load-bearing case is
# section 2: a POISONED `pogo` first on PATH, asserted both by its marker file
# and by the exit status, because either alone passes against a fallback that
# happens to agree.
gate_step "Testing the from-source staleness runner" bash scripts/check-staleness_test.sh

# The per-package test budget and its overrun report (mg-a465). The
# load-bearing case is the POSITIVE CONTROL: a package is made to exceed the
# budget on purpose and the output must NAME it and the budget rather than
# ending in an unlabelled goroutine dump. `go test -timeout` alone does not
# give that — it panics — so without this file the fix looks applied and isn't.
# Costs 12.1s measured (5s of it deliberate sleeping); the budget is overridable
# so that control runs in seconds rather than 20 minutes.
gate_step "Testing the Go per-package test budget and overrun report" bash scripts/go-test-budget_test.sh

# The TREE-WIDE sweep for unbounded `go test` (mg-37d4). The budget check above
# is per-site and mg-a465's wiring assertions name TWO files; this walks every
# tracked file, so a new script or CI job is covered the day it is added rather
# than the day somebody remembers to extend a list. That distinction is not
# theoretical: three unbounded sites have been found here one at a time by three
# different people, and the third (scripts/upgrade-smoke.sh) was already in the
# tree, already unbounded, on the day the two-filename check was written green.
#
# Runs the checker itself first — a walk of ~1000 tracked files, well under a
# second — then the suite whose load-bearing case is the positive control: an
# unbounded invocation is PLANTED in a fixture checkout and the checker must
# fire on it, naming file and line. That control is what caught the checker's
# own version of this bug, an anti-rot guard so broad it exited 2 against any
# tree but this one.
#
# The two step labels below deliberately avoid spelling the lowercase two-word
# command. mg-a465's wiring assertion greps this file for that string and
# subtracts the lines carrying -timeout, which is a PRESENCE test: it cannot
# tell a command from prose, so a label naming the thing being checked reads to
# it as an unbounded invocation and fails the gate. That false positive is the
# same one that made a presence rule unusable for the tree-wide sweep — it
# flagged 9 prose lines against 1 real invocation — and it is why the checker
# added here matches on command POSITION instead. The older assertion is left
# alone rather than rewritten: it pins a stronger per-file property (that these
# two files route through go-test-budget.sh at all), and widening another
# ticket's test is not this one's business.
gate_step "Checking every Go test invocation in the tree is bounded" bash scripts/unbounded-go-test.sh
gate_step "Testing the unbounded-invocation sweep" bash scripts/unbounded-go-test_test.sh

# The EXTERNAL redeploy witness (mg-ce10), implementing the rule that a detector
# for "X did not happen" must not be ACTIVATED BY X. driftwatch reports the
# running daemon's revision age correctly and ships entirely inside pogod, which
# the redeploy installs — so on a night the redeploy fails, the alarm for the
# failed redeploy is dark. The probe is a tracked file and goes live at MERGE.
#
# The load-bearing case is section 5: `go`, `pogo`, `pogod` and `jq` POISONED
# first on PATH, asserted both by their marker files and by the exit status,
# because the probe is only independent for as long as it touches nothing the
# deploy provides. Section 6 is the second-order version — a stale
# remote-tracking ref in a checkout the deploy is what fetches — and asserts the
# local-ref reading reports health on a state the default reading alerts on.
gate_step "Testing the external redeploy revision probe" bash scripts/revision-probe_test.sh

# The ARMING of that probe (mg-a03d). mg-ce10 landed the witness and wired it to
# nothing — 501 lines, zero schedules, zero plists, zero callers — which is the
# limiting case of the rule it implements: a detector activated by NOTHING is
# present by existence and absent by effect.
#
# The load-bearing case is section 4, and it is deliberately not "the plist
# parses". A plist can parse, install and appear in `launchctl list` while
# naming a command line nobody has ever run. Section 4 reads ProgramArguments
# back out of the plist the installer wrote and EXECUTES that exact vector,
# green (stub daemon answering) and red (daemon gone), asserting a distinct
# ledger line from each. Section 5 poisons go/pogo/pogod: an arming step that
# needs a current `pogo` cannot arm the box whose `pogo` is ten days stale,
# which is the box that needs arming. Section 2b is that same unknown-polarity
# argument aimed at the installer's own render guards: both had only ever been
# shown to ACCEPT, and one of them was the `| grep -q` spelling that admits a
# live placeholder at exit 0 once the render outgrows a pipe buffer (mg-712e).
gate_step "Testing the revision probe's launchd arming" bash scripts/install-revision-probe_test.sh

# The EXTERNAL witness that the FLEET is still completing turns (mg-f867). Same
# rule as the two steps above, one level out: a detector hosted INSIDE the
# population it watches cannot report that population failing. deploy-verify §0
# was built for exactly the 2026-08-14 outage, asks the right question, and never
# ran — it is architect's own schedule and architect was one of the seven agents
# that stopped.
#
# Two load-bearing cases. Section 6 is the THIRD CELL: four unmeasurable states,
# none of which may land in FLEET-STOP or in OK, including a FUTURE mtime — the
# one way this predicate could read green over a broken measurement. Section 8 is
# the delivery half, and it is written the way a RACE has to be: a deliberately
# chatty stub mg, with the FIRST assertion being that the old `pipefail + grep -q`
# idiom really does get 141 against that fixture, so the section cannot pass by
# certifying timing luck (mg-7ce7).
gate_step "Testing the external fleet-liveness probe" bash scripts/fleet-liveness-probe_test.sh

# The ARMING of that probe. The load-bearing cases are section 4 — the argument
# vector read back out of the rendered plist and EXECUTED, because a plist can
# parse, install and appear in `launchctl list` while naming a command line
# nobody has ever run — and section 5, where a failing delivery positive control
# REFUSES the install. 5e is this ticket's rule turned on its own remedy: the
# installer's placeholder guard is asserted against a 200KB render that defeats
# the `| grep -q` spelling it deliberately does not use.
gate_step "Testing the fleet-liveness probe's launchd arming" bash scripts/install-fleet-liveness-probe_test.sh

# The gate's own per-step profile (mg-eed9). The load-bearing case is Test 4,
# and it is a positive control rather than a smoke test: the profile's `cores`
# column is the only thing separating a step that is COMPUTING from a step that
# is WAITING, and it fails silently — `t=$(times)` forks, and a forked child's
# children-times start at zero, so every step reads 0.00s of CPU and the gate
# looks like a thing in which nothing computes. Reintroducing that one
# substitution turns the CPU-burning step's reading from 0.26s to 0.00s and
# takes three assertions RED with it (measured).
gate_step "Testing the gate's per-step profile" bash scripts/gate-profile_test.sh

gate_step "Testing build.sh" bash build_test.sh

gate_step "Testing changelog fragment assembler" bash scripts/assemble-changelog_test.sh

# Changelog coverage (mg-7904). The assembler's LOUD-EMPTY guard checks a weaker
# property (non-empty) than CONTRIBUTING's rule (a fragment per change), so it
# passes while a release ships describing only part of itself. The load-bearing
# case here is the POSITIVE CONTROL: the check is shown to FAIL on a range with
# a known-missing fragment before any passing case is trusted.
gate_step "Testing changelog coverage check" bash scripts/changelog-coverage_test.sh

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
gate_step "Testing changelog release-roll and link references" bash scripts/roll-changelog_test.sh

# The work-item scope guard (mg-f1d5). Every case runs against a stub `mg` and a
# fixture worktree in a temp dir, so the suite never reads the developer's live
# ~/.macguffin. The load-bearing case is the opt-in one: a guard that blocked an
# agent nobody opted in for would be ripped out of every fleet within the hour.
gate_step "Testing work-item scope guard" bash scripts/mg-scope-guard_test.sh

# The expired-premise instrument (mg-027b). It reads an mg store and prices the
# candidate triggers architect proposed; the suite builds its own fixture stores
# in a temp dir and passes --root explicitly, so it never reads the developer's
# live ~/.macguffin. The load-bearing case is the phase split: the mg-0466 /
# mg-24dc timeline is replayed to the second and must classify as PRE-DISPATCH,
# because the finding is that architect's trigger as stated would have missed the
# case that motivated it. A genuinely-simultaneous pair sits beside it as the
# positive control, so the negative cannot pass by the population being empty.
gate_step "Testing the expired-premise rate instrument" bash scripts/premise-expiry-rate_test.sh

# The CI-vs-gate coverage instrument and the EXIT trap that prints it (mg-82a6).
# Runs against fixture gate/workflow files in a temp dir, so the assertions do
# not move when a row is added to this file — the count is measured, never
# pinned. The load-bearing cases are the two positive controls: a script path
# that appears ONLY in a YAML comment must NOT count as covered, and a parse
# that finds nothing must exit 2 and SAY it could not measure, because "0 rows
# shared" is the most alarming reading this instrument can emit and must never
# be producible by the parser having rotted.
gate_step "Testing the CI-vs-gate coverage instrument" bash scripts/ci-coverage_test.sh
