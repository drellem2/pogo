#!/bin/bash
# =============================================================================
# signal-witness.sh — run a command and, if a signal lands on the gate's test
# subtree, record WHO ELSE WAS ON THE BOX at that instant (mg-3bd1).
# =============================================================================
#
#   scripts/signal-witness.sh <command> [args...]
#
# WHAT THIS IS FOR, AND WHAT IT DELIBERATELY IS NOT
#
# Three merge gates on this host have been ended by a SIGTERM that the refinery
# did not send. Measured from ~/.pogo/events.log over 2026-08-16..2026-09-07:
#
#     2026-08-19T17:57Z  polecat-pfbaf   killed at 2m58s (178s)  class INDETERMINATE
#     2026-09-07T20:08Z  polecat-t2127   killed at 1m26s  (85s)  class DEFECT
#     2026-09-07T20:14Z  polecat-t2127   killed at 4m24s (264s)  class DEFECT
#
# Three different elapsed times, so no fixed watchdog. The same POSITION every
# time: `go test ./...` had printed through internal/ackwatch and was inside
# internal/agent — the next package in the print order and the slowest in the
# tree — so the elapsed time varies because internal/agent's runtime varies, not
# because the signal arrives at random. All three carry the same shape and none
# of them carries the one fact that would end the investigation: WHICH PROCESS
# SENT THE SIGNAL. Nothing on this box was recording it.
#
# This file does not name the sender: on darwin the sending pid is available
# only to a handler installed with SA_SIGINFO, which a SHELL cannot install and
# Go's os/signal does not expose. That was recorded here as "and it cannot",
# which was wrong — a compiled helper installs SA_SIGINFO in one call and needs
# no privilege, and scripts/signal-sender.sh (mg-cbc3) is that helper, wired
# INSIDE the tmpdir guard where the three recorded kills actually landed. This
# file is still the instrument for the reading that has to be taken from
# OUTSIDE the signalled region, which is the one below and which the sender
# recorder cannot take:
#
#   THE DELIVERY SHAPE   which of this process's ANCESTORS are still alive when
#                        the signal lands. A signal to the gate's process group
#                        takes the ancestors with it; a walk over a subtree
#                        leaves them running. Those two have different senders,
#                        and until now they were indistinguishable in the
#                        record. (For the 2026-09-07 pair this was reconstructed
#                        by hand from the ORDER of three lines of gate output;
#                        that reconstruction is what this file replaces.)
#   THE PROCESS TABLE    every process on the host at the instant of the kill,
#                        with pid/ppid/pgid/start/argv. The sender is somewhere
#                        in it. A snapshot taken after the gate has failed is
#                        taken minutes later and is worth nothing.
#
# WHY THE STATUS ALONE IS NOT ENOUGH, AND THE ONE THING THIS MUST NOT ASSERT
#
# A shell cannot tell a child KILLED BY signal N from a child that chose to
# `exit $((128+N))`. `$?` is 128+N for both, and there is no second reading in
# POSIX shell that separates them. So a wrapper that saw 143 and reported "the
# step was killed by SIGTERM" would be asserting a fact it does not have — the
# same error in the opposite direction to the one that made these runs read as
# branch defects. Everything below therefore distinguishes:
#
#   OBSERVED   this process caught the signal itself, in a trap. Not an
#              inference: the signal was delivered here.
#   AMBIGUOUS  the wrapped command came back 128+N and no signal was delivered
#              to this process. That is a killed child OR a child that exited
#              128+N on purpose, and this wrapper does not know which.
#
# WHY IT RE-RAISES
#
# On a caught signal it restores the default disposition and re-signals itself,
# so its own wait status is the signal rather than a chosen exit code. A
# wrapper that turned a kill into an ordinary exit would be destroying the
# evidence it exists to preserve.
#
# EXIT STATUS
#   the wrapped command's status, unchanged, on every path where this process
#   was not itself signalled. 2 for a usage error.
#
# ENVIRONMENT
#   POGO_SIGNAL_WITNESS=0     run the command with no traps and no snapshot.
#                             Announced on stderr — a witness that can be
#                             disabled quietly has been off for months by the
#                             time anybody asks.
#   POGO_SIGNAL_WITNESS_PS    the process-table command. Default
#                             `ps -axo pid=,ppid=,pgid=,lstart=,command=`.
#                             The suite overrides it to keep its controls cheap
#                             and deterministic.
#   POGO_SIGNAL_WITNESS_DIR   where the full process-table snapshot is written.
#                             Default <pogo state root>/signal-witness, resolved
#                             by resolve_pogo_home below — NOT by
#                             "${POGO_HOME:-$HOME/.pogo}", which is wrong on this
#                             box (see that function).
#
# WHY THE TABLE GOES TO A FILE AND NOT INTO THE GATE OUTPUT
#
# A remedy is an artifact of the same kind as the defect, so it is subject to
# that defect. The refinery caps the gate output it PERSISTS at 8 KB head+tail
# (internal/refinery/gateoutputcap.go) and the copy on the event line at 1 KB.
# `ps -ax` on this host is several hundred lines; pasted inline it would not
# merely be truncated, it would evict the step profile and the failure text
# around it — a report that destroys the record it was added to. So the block
# printed inline is small and fixed-size, and the table it summarises is written
# to a file that outlives the refinery worktree. If the file cannot be written
# the table IS printed inline, because a snapshot that exists nowhere is worse
# than one that crowds the log, and the report says which of the two happened.
#
# WHAT THIS STILL CANNOT SEE
#
#   SIGKILL         nothing catches it and no trap runs. A gate killed with -9
#                   leaves this file as silent as it was before.
#   PID REUSE       the ancestor list is captured before the run, so an ancestor
#                   that died and had its pid reused reads as ALIVE. That biases
#                   the reading TOWARD "not a group kill", which is the answer
#                   the three recorded occurrences already give — so a
#                   subtree verdict from a long run deserves the argv check the
#                   report prints alongside each pid.
#   ITS OWN LEAK    nothing reaps the snapshots. One file per SIGNALLED run and
#                   nothing else — three in the 22 days measured, ~60 KB each —
#                   so it is bounded by the rate of the fault it records rather
#                   than by how often the gate runs, which is the distinction
#                   that made the $TMPDIR leak (mg-60eb) unbounded and this one
#                   not. Said out loud rather than swept: a cleanup that ran on
#                   a schedule would be one more thing that can delete the
#                   evidence before anyone reads it.
# =============================================================================
set -u

if [ "$#" -eq 0 ]; then
    echo "usage: signal-witness.sh <command> [args...]" >&2
    exit 2
fi

if [ "${POGO_SIGNAL_WITNESS:-1}" = "0" ]; then
    echo "signal-witness.sh: DISABLED by POGO_SIGNAL_WITNESS=0 — no traps armed, nothing will be recorded if this run is signalled." >&2
    "$@"
    exit $?
fi

WITNESS_PS="${POGO_SIGNAL_WITNESS_PS:-ps -axo pid=,ppid=,pgid=,lstart=,command=}"
# resolve_pogo_home — the pogo state root, normalised the way config.PogoHome
# normalises it. Copied verbatim from scripts/fleet-liveness-probe.sh, which is
# the spelling internal/config's tree-wide guard sanctions, and pinned against
# `config.PogoHome` by the parity test in scripts/signal-witness_test.sh.
#
# The obvious "${POGO_HOME:-$HOME/.pogo}" is WRONG ON THIS BOX and was caught by
# that guard on the first gate run of this change: ~/.zshrc still exports the
# legacy POGO_HOME=$HOME, so that expression names $HOME while config.PogoHome
# names $HOME/.pogo. The snapshot would have been written next to a different
# tree from the one everything else reads — and this file's whole purpose is to
# leave a record somebody can find later.
resolve_pogo_home() {
    local h="${POGO_HOME:-}"
    if [ -z "$h" ]; then echo "$HOME/.pogo"; return 0; fi
    # Compare without trailing slashes rather than with realpath: this must work
    # on a box where the directory does not exist yet.
    local a="${h%/}" b="${HOME%/}"
    if [ "$a" = "$b" ]; then echo "$a/.pogo"; else echo "$h"; fi
}

WITNESS_DIR="${POGO_SIGNAL_WITNESS_DIR:-$(resolve_pogo_home)/signal-witness}"

# The ancestor chain, captured BEFORE the command runs. It has to be taken
# early: by the time a signal lands, a dead ancestor is gone from `ps` and
# cannot be asked what it was. Walking up from $$ to pid 1.
WITNESS_ANCESTORS=""
witness_capture_ancestors() {
    local p="$$" n=0 pp cmd
    while [ "$n" -lt 24 ]; do
        pp="$(ps -o ppid= -p "$p" 2>/dev/null | tr -d ' ')"
        case "$pp" in '' | *[!0-9]*) break ;; esac
        [ "$pp" -le 1 ] && break
        cmd="$(ps -o command= -p "$pp" 2>/dev/null | cut -c1-100)"
        WITNESS_ANCESTORS="$WITNESS_ANCESTORS$pp	$cmd
"
        p="$pp"
        n=$((n + 1))
    done
}
witness_capture_ancestors

# witness_report SIGNAL_NAME CERTAINTY — everything this file exists to write.
#
# stderr, and unbuffered echoes rather than one heredoc, because the gate's
# stdout and stderr are one pipe and the ORDER of these lines against the
# surrounding gate output is itself evidence — that ordering is what identified
# the delivery shape of the 2026-09-07 pair.
witness_report() {
    local signame="$1" certainty="$2" alive=0 dead=0 line pid cmd
    local snapfile="" snapcount=0 stamp

    # FIRST, before any of the prose: the table is a measurement of a moment and
    # every line printed before it is a moment it is no longer measuring.
    stamp="$(date -u +%Y%m%dT%H%M%SZ 2>/dev/null || echo unknown)"
    # $WITNESS_PS is deliberately unquoted: it is a COMMAND WITH FLAGS, and the
    # split is what turns it back into one. Quoting it would look up a program
    # named "ps -axo pid=,...".
    if mkdir -p "$WITNESS_DIR" 2>/dev/null; then
        snapfile="$WITNESS_DIR/${stamp}.$$.ps.txt"
        if $WITNESS_PS > "$snapfile" 2>/dev/null; then
            snapcount="$(wc -l < "$snapfile" 2>/dev/null | tr -d ' ')"
            [ -n "$snapcount" ] || snapcount=0
        else
            rm -f "$snapfile" 2>/dev/null
            snapfile=""
        fi
    fi
    {
        echo ""
        echo "============================================================================="
        echo "SIGNAL WITNESS — ${certainty}: ${signame} on the gate's test subtree (mg-3bd1)"
        echo ""
        if [ "$certainty" = "OBSERVED" ]; then
            echo "  This process caught ${signame} in a trap, so the signal was DELIVERED HERE."
            echo "  That is a measurement, not an inference from an exit status."
        else
            echo "  The wrapped command came back 128+N and NO signal was delivered to this"
            echo "  process. A shell cannot separate a child killed by ${signame} from a child"
            echo "  that chose to exit with that status — \$? is identical for both. The"
            echo "  readings below are context for either, and must NOT be quoted as a kill."
        fi
        echo ""
        echo "  ANCESTORS AT THE MOMENT OF THE SIGNAL — the delivery shape."
        echo "  A signal to this gate's PROCESS GROUP takes these with it; a walk over a"
        echo "  subtree rooted at or below this process leaves them running. Those two have"
        echo "  different senders, and this is the reading that tells them apart."
        echo ""
        if [ -z "$WITNESS_ANCESTORS" ]; then
            echo "    (none captured — the pre-run walk found no parent above pid 1)"
        else
            printf '%s' "$WITNESS_ANCESTORS" | while IFS='	' read -r pid cmd; do
                [ -n "$pid" ] || continue
                if kill -0 "$pid" 2>/dev/null; then
                    echo "    ALIVE  $pid  $cmd"
                else
                    echo "    GONE   $pid  $cmd"
                fi
            done
        fi
        # Counted in this shell rather than in the loop above: the `while` runs
        # in a subshell on the far side of a pipe, so counters incremented there
        # are lost. That is the bug this comment exists to stop being reintroduced.
        for line in $(printf '%s' "$WITNESS_ANCESTORS" | awk -F'\t' 'NF { print $1 }'); do
            if kill -0 "$line" 2>/dev/null; then alive=$((alive + 1)); else dead=$((dead + 1)); fi
        done
        echo ""
        echo "    ${alive} alive, ${dead} gone."
        if [ "$dead" -eq 0 ] && [ "$alive" -gt 0 ]; then
            echo "    EVERY ancestor survived => the signal was NOT delivered to this gate's"
            echo "    process group. Something addressed a subtree, or these pids one at a time."
        elif [ "$alive" -eq 0 ] && [ "$dead" -gt 0 ]; then
            echo "    EVERY ancestor is gone => consistent with a group-wide or gate-wide kill."
        fi
        echo ""
        echo "    (An ancestor that died and had its pid REUSED reads as ALIVE here — the"
        echo "     list is captured before the run and cannot be re-derived afterwards. The"
        echo "     argv above is what distinguishes a survivor from a stranger.)"
        echo ""
        echo "  PROCESS TABLE at the instant of the signal — pid ppid pgid started command."
        echo "  The candidate set for the sender, worth having only because it was taken NOW"
        echo "  rather than after the gate failed. If scripts/signal-sender.sh was in the"
        echo "  chain and was itself signalled it will have NAMED the sender outright; this"
        echo "  table is what remains when the signal did not reach that far."
        echo ""
        if [ -n "$snapfile" ]; then
            echo "    ${snapcount} processes, written to:"
            echo "        ${snapfile}"
            echo "    It is OUTSIDE the refinery worktree on purpose — that directory is"
            echo "    removed when the merge request resolves, and this record has to"
            echo "    outlive it."
        else
            echo "    The snapshot could NOT be written to ${WITNESS_DIR}, so the table is"
            echo "    inline instead and may be truncated by the gate's own output cap:"
            echo ""
            $WITNESS_PS 2>/dev/null | sed 's/^/    /'
        fi
        echo ""
        echo "  Whatever you conclude from this, it is not a verdict on the branch: a run"
        echo "  that was signalled did not finish asserting anything."
        echo "============================================================================="
        echo ""
    } >&2
}

# Armed BEFORE the command starts, and every signal named. A bare `trap ... EXIT`
# does not fire on SIGTERM, which is the only signal this file has ever been
# needed for.
# The SIG- prefixed spelling, deliberately, and it is the same point mg-0502
# made about `signal: terminated`: the report is the thing that gets grepped and
# forwarded, so both arms must name the signal identically. A report saying
# "TERM" where the other says "SIGTERM" is one a search for past occurrences
# silently misses.
witness_trap() {
    local signame="$1"
    witness_report "$signame" "OBSERVED"
    # Die of the signal rather than of a chosen status, so this process's wait
    # status still says what happened to it.
    trap - "$signame"
    kill -"$signame" "$$"
}
trap 'witness_trap SIGTERM' SIGTERM
trap 'witness_trap SIGINT' SIGINT
trap 'witness_trap SIGHUP' SIGHUP
trap 'witness_trap SIGQUIT' SIGQUIT

"$@"
status=$?

# The AMBIGUOUS arm. Bash reports a signalled child as 128+N and there is no
# second reading that separates that from a chosen exit — so this snapshot is
# offered as context and labelled as ambiguous, never as a kill.
if [ "$status" -gt 128 ] && [ "$status" -lt 193 ]; then
    signum=$((status - 128))
    case "$signum" in
        1) signame="SIGHUP" ;;
        2) signame="SIGINT" ;;
        3) signame="SIGQUIT" ;;
        9) signame="SIGKILL" ;;
        15) signame="SIGTERM" ;;
        *) signame="signal ${signum}" ;;
    esac
    witness_report "$signame" "AMBIGUOUS (status ${status} from the wrapped command)"
fi

exit "$status"
