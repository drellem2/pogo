#!/bin/bash
# =============================================================================
# signal-sender.sh — front-end for scripts/signal-sender.c, the instrument that
# records WHICH PROCESS SENT the signal that ends a gate step (mg-cbc3).
# =============================================================================
#
#   scripts/signal-sender.sh <command> [args...]
#
# WHAT THIS ANSWERS THAT NOTHING ON THIS BOX ANSWERED
#
# Three merge gates have been ended by a SIGTERM the refinery did not send
# (2026-08-19 at 178s, 2026-09-07 at 85s and 264s — mg-3bd1). All three closed
# with the sender unidentified, and the reason recorded was that darwin does not
# expose a sending pid: true of a shell, true of Go's os/signal, and NOT true of
# a C program, which gets it from si_pid in an SA_SIGINFO handler with no
# privilege at all. Measured on this host with the sender known in advance, the
# reading was exact both times; the controls are Tests 1 and 2 of
# scripts/signal-sender_test.sh.
#
# WHY A COMPILED HELPER AND NOT MORE SHELL
#
# There is no shell-level reading of the sender. `trap` gives the signal and
# nothing else, `$?` gives 128+N which a chosen `exit 143` produces identically,
# and macOS ships no `sigwaitinfo` for python either (checked: python3's signal
# module has neither sigwaitinfo nor sigtimedwait on darwin). SA_SIGINFO is the
# only reading, and installing it requires a compiled program.
#
# HOW IT DEGRADES, AND WHY THAT SHAPE
#
# The gate must never go red because an instrument could not build. If cc is
# missing, or the compile fails, or the built binary does not pass its own
# one-line self-check, this file `exec "$@"` — which leaves the process chain
# byte-for-byte what it was before this instrument existed, because `exec`
# replaces this shell rather than adding to it. The degradation is ANNOUNCED on
# stderr: a witness that can be off quietly has been off for months by the time
# anybody asks (mg-3bd1's own reasoning for POGO_SIGNAL_WITNESS=0).
#
# The binary is cached under the pogo state root and keyed by a hash of the
# source, so it is compiled once per revision of signal-sender.c rather than
# once per gate run, and a stale binary from an older source can never be used.
#
# ENVIRONMENT
#   POGO_SIGNAL_SENDER=0      run the command unwrapped. Announced on stderr.
#   POGO_SIGNAL_WITNESS_DIR   where the record and the cached binary live.
#                             Default <pogo state root>/signal-witness, resolved
#                             by resolve_pogo_home below — NOT by
#                             "${POGO_HOME:-$HOME/.pogo}", which is wrong on this
#                             box (see that function, and mg-3bd1's Test 9).
#   CC                        compiler. Default cc.
#
# EXIT STATUS
#   the wrapped command's, unchanged, on every path.
# =============================================================================
set -u

if [ "$#" -eq 0 ]; then
    echo "usage: signal-sender.sh <command> [args...]" >&2
    exit 2
fi

if [ "${POGO_SIGNAL_SENDER:-1}" = "0" ]; then
    echo "signal-sender.sh: DISABLED by POGO_SIGNAL_SENDER=0 — if this run is signalled, the sending pid will not be recorded." >&2
    exec "$@"
fi

# resolve_pogo_home — the pogo state root, normalised the way config.PogoHome
# normalises it. The same spelling scripts/signal-witness.sh and
# scripts/fleet-liveness-probe.sh carry, which is what internal/config's
# tree-wide guard sanctions. "${POGO_HOME:-$HOME/.pogo}" is WRONG ON THIS BOX:
# ~/.zshrc still exports the legacy POGO_HOME=$HOME, so it names $HOME while
# config.PogoHome names $HOME/.pogo.
resolve_pogo_home() {
    local h="${POGO_HOME:-}"
    if [ -z "$h" ]; then echo "$HOME/.pogo"; return 0; fi
    local a="${h%/}" b="${HOME%/}"
    if [ "$a" = "$b" ]; then echo "$a/.pogo"; else echo "$h"; fi
}

WITNESS_DIR="${POGO_SIGNAL_WITNESS_DIR:-$(resolve_pogo_home)/signal-witness}"
export POGO_SIGNAL_WITNESS_DIR="$WITNESS_DIR"

HERE="$(cd "$(dirname "$0")" && pwd)"
SRC="$HERE/signal-sender.c"

# degrade_now — run the command as though this file were not in the chain.
# `exec`, so the process count and the parent of the wrapped command are exactly
# what they were before: the delivery shape this instrument measures must not be
# changed by the instrument failing to start.
#
# The reason travels in $degrade_msg rather than as $1 because this function is
# called as `degrade_now "$@"` — it has to forward the caller's argument vector,
# so its own parameters are already spoken for.
degrade_msg=""
degrade_now() {
    echo "signal-sender.sh: ${degrade_msg} — running UNWITNESSED; a signal on this step will not name its sender." >&2
    exec "$@"
}

[ -r "$SRC" ] || { degrade_msg="no signal-sender.c beside this script"; degrade_now "$@"; }

# Key the cache on the SOURCE, so an edit to the .c can never be served from a
# binary built before it. shasum is on stock macOS; cksum is the POSIX fallback.
if command -v shasum >/dev/null 2>&1; then
    SRC_KEY="$(shasum -a 256 "$SRC" 2>/dev/null | cut -c1-16)"
else
    SRC_KEY="$(cksum < "$SRC" 2>/dev/null | tr -d ' ' | cut -c1-16)"
fi
[ -n "${SRC_KEY:-}" ] || SRC_KEY="nokey"
ARCH="$(uname -m 2>/dev/null || echo unknown)"
BIN_DIR="$WITNESS_DIR/bin"
BIN="$BIN_DIR/signal-sender-$ARCH-$SRC_KEY"

if [ ! -x "$BIN" ]; then
    CC_BIN="${CC:-cc}"
    command -v "$CC_BIN" >/dev/null 2>&1 || {
        degrade_msg="no C compiler ($CC_BIN) on PATH"; degrade_now "$@"; }
    mkdir -p "$BIN_DIR" 2>/dev/null || {
        degrade_msg="cannot create $BIN_DIR"; degrade_now "$@"; }
    # Compile to a temporary name in the SAME directory and rename: two gates
    # can run this concurrently on one host, and a half-written binary that is
    # already named $BIN is a broken gate rather than a missing instrument.
    TMP_BIN="$(mktemp "$BIN_DIR/.signal-sender.XXXXXX" 2>/dev/null)" || {
        degrade_msg="cannot write to $BIN_DIR"; degrade_now "$@"; }
    if ! "$CC_BIN" -O1 -o "$TMP_BIN" "$SRC" >/dev/null 2>&1; then
        rm -f "$TMP_BIN" 2>/dev/null
        degrade_msg="$CC_BIN could not build signal-sender.c"; degrade_now "$@"
    fi
    chmod 0755 "$TMP_BIN" 2>/dev/null
    mv -f "$TMP_BIN" "$BIN" 2>/dev/null || {
        rm -f "$TMP_BIN" 2>/dev/null
        degrade_msg="could not install $BIN"; degrade_now "$@"; }
fi

# The self-check, and it is not ceremony: a cached binary can be truncated, can
# be for another architecture, or can have been built by a compiler that is now
# gone. `signal-sender` with no arguments prints its usage and exits 2, so this
# costs one fork and separates "runs" from "exists".
"$BIN" >/dev/null 2>&1
if [ "$?" -ne 2 ]; then
    degrade_msg="$BIN did not pass its self-check (no-arg run should exit 2)"
    degrade_now "$@"
fi

exec "$BIN" "$@"
