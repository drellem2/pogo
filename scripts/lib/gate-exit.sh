#!/bin/bash
# =============================================================================
# WHAT THE MERGE GATE SAYS ON ITS WAY OUT (mg-82a6)
# =============================================================================
#
# `test.sh` arms exactly one EXIT trap, and this file is what it arms it with.
# Two things have to be said at the end of a gate run and neither of them can
# be appended at the bottom of the file, because the run whose report matters
# most is the one that never reaches the bottom:
#
#   the profile   which step cost what (mg-eed9).
#   the coverage  how much of this gate GitHub CI also runs (mg-82a6) — and,
#                 when the gate FAILED, that a green CI run on the same commit
#                 is not evidence the failure is spurious.
#
# WHY THE COVERAGE LINE RIDES THE EXIT TRAP AND NOT A README:
#   The false inference this exists to stop is made at one specific moment: a
#   gate has just gone red, someone checks GitHub, sees green, and concludes the
#   two disagree. Nothing about that moment involves opening a document. The
#   fact has to be in the output the person is already looking at, next to the
#   failure. On 2026-08-14 it was not, and one unreproduced failure became a
#   MAIN IS RED alarm four minutes before a nightly deploy, on a tree that was
#   green throughout (mg-5fc8).
#
# WHY IT IS A LIBRARY AND NOT FOUR LINES INLINE IN test.sh:
#   So that it is testable. A trap body written inline in `test.sh` can only be
#   exercised by running the whole gate — ~12 minutes and several live daemons —
#   which in practice means it is never exercised at all, which is the shape of
#   defect this repository keeps finding. `scripts/ci-coverage_test.sh` sources
#   THIS file, arms the same trap, and drives a failing step in about a second.
#
# WHY IT DOES NOT LIVE IN gate-profile.sh:
#   gate-profile.sh is generic — `build.sh` sources it too, and its contract is
#   "measure steps". CI coverage is a property of `test.sh` specifically, and
#   `build.sh` invokes `test.sh`, so folding it in there would print the notice
#   twice per gate and would couple a measurement library to one repo's CI.
#
# USAGE (test.sh):
#   . "$TEST_SH_DIR/scripts/lib/gate-profile.sh"
#   . "$TEST_SH_DIR/scripts/lib/gate-exit.sh"
#   gate_profile_begin "test.sh"
#   trap 'gate_exit_report' EXIT
#
# ENVIRONMENT:
#   POGO_CI_COVERAGE_NOTICE=0   suppress the coverage block. The profile still
#                               prints. Exists for suites that source this file
#                               to test the profile half in isolation.
# =============================================================================

GATE_EXIT_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE_EXIT_SCRIPTS_DIR="$(cd "$GATE_EXIT_LIB_DIR/.." && pwd)"

# gate_exit_report — the EXIT trap body. MUST capture $? on its first line: it
# runs on both the passing and the failing exit, and the failing one is the
# whole reason the coverage block exists.
gate_exit_report() {
    local status=$?

    if command -v gate_profile_report >/dev/null 2>&1; then
        gate_profile_report || true
    fi

    if [ "${POGO_CI_COVERAGE_NOTICE:-1}" != "0" ]; then
        # Never allowed to change the gate's verdict. This block is a caption on
        # the result, and a caption that could turn a green gate red — or, worse,
        # a red gate green — would be a new instance of the class of defect it is
        # captioning.
        bash "$GATE_EXIT_SCRIPTS_DIR/ci-coverage.sh" --notice --gate-status "$status" || true
    fi

    return $status
}
