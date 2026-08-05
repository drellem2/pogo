#!/usr/bin/env bash
# Controls on scripts/check-staleness.sh — the FROM-SOURCE runner for the
# staleness witness (mg-dd49).
#
# WHAT IS UNDER TEST HERE IS NOT THE JUDGEMENT. internal/staleness owns that and
# has its own suite; duplicating it in shell would be a second copy of the
# decision, free to drift from the one that ships. What is under test is the
# property that makes the runner worth having at all:
#
#   IT MUST NEVER REACH FOR AN INSTALLED `pogo`.
#
# The witness detects that installed artifacts have fallen behind source. It is a
# subcommand of `pogo`, and `pogo` only becomes current when the nightly redeploy
# runs — the very mechanism whose failure it detects. A runner that quietly fell
# back to the binary on PATH would look like it worked and would be reporting
# whatever revision the last successful deploy left behind. On 2026-08-05 that
# revision was six days and 52 commits old and did not contain this subcommand at
# all.
#
# So section 2 puts a POISONED `pogo` first on PATH — one that records having
# been called and exits 66 — and asserts both that the marker is absent and that
# the exit status is the witness's own. An assertion that only checked the exit
# code would pass against a fallback that happened to agree.
#
# Sections 1 and 3 are the positive control carried end to end through the
# runner: RED on a genuinely missed run, quiet on a healthy one. The witness's
# own suite proves the judgement; this proves the runner does not lose it.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"

RESULTS_FILE=$(mktemp)
pogo_sandbox_create staleness
SANDBOX="$POGO_SANDBOX_DIR"

cleanup() {
    pogo_sandbox_down
    rm -f "$RESULTS_FILE"
}
trap cleanup EXIT

pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE" || { echo "LEDGER WRITE FAILED: $1"; exit 1; }; }

# Go's caches are pinned to the REAL ones before HOME moves. GOMODCACHE and
# GOCACHE default off $HOME, so a `go run` under the sandbox home would re-resolve
# and re-download the entire module graph into a directory this file deletes on
# exit. Pinning them explicitly beats the HOME-derived default, so the sandbox
# stays private for everything that matters — ~/.pogo, the config layer, the mg
# store — while the build caches stay shared. They are Go's, not pogo's, and no
# assertion here reads or writes them.
GOMODCACHE_REAL="$(cd "$REPO_ROOT" && go env GOMODCACHE 2>/dev/null)"
GOCACHE_REAL="$(cd "$REPO_ROOT" && go env GOCACHE 2>/dev/null)"
if [ -z "$GOMODCACHE_REAL" ] || [ -z "$GOCACHE_REAL" ]; then
    pogo_sandbox_fail "could not resolve GOMODCACHE/GOCACHE — the runner cannot be exercised without a build cache"
fi
export GOMODCACHE="$GOMODCACHE_REAL" GOCACHE="$GOCACHE_REAL"

# Warm the build under the real HOME, before isolation, so a cold first `go run`
# inside the assertions cannot be mistaken for a hang.
echo "Warming the build for scripts/check-staleness.sh..."
if ! (cd "$REPO_ROOT" && go build -o "$SANDBOX/pogo-warm" ./cmd/pogo); then
    pogo_sandbox_fail "could not build cmd/pogo — the from-source runner cannot be exercised"
fi

pogo_sandbox_isolate

RUNNER="$REPO_ROOT/scripts/check-staleness.sh"

# --- 1. RED — a genuinely missed run, through the runner ---------------------
# The 2026-07-31..08-04 shape: the last recorded attempt is 07-31 and the box was
# powered off through four 03:00 windows, so nothing was written for any of them.
STALE_STAMP="$SANDBOX/stale.stamp"
printf '2026-07-31 1 0\n' > "$STALE_STAMP"

red_out="$(bash "$RUNNER" --skip-prompts --stamp "$STALE_STAMP" --now 2026-08-04T12:00:00Z 2>&1)"
red_rc=$?

if [ "$red_rc" -eq 1 ]; then
    pass "runner exits 1 on a missed run"
else
    fail "runner exited $red_rc on a missed run, want 1 — output: $red_out"
fi
if printf '%s' "$red_out" | grep -q "MISSED: 4 night(s)"; then
    pass "runner reports all four missed nights"
else
    fail "runner did not report four missed nights — output: $red_out"
fi

# --- 2. It builds from source and never consults an installed `pogo` ---------
# A poisoned `pogo` first on PATH. If the runner ever falls back to the installed
# binary — now or after a well-meant edit — this is the assertion that says so.
POISON_DIR="$SANDBOX/poison"
mkdir -p "$POISON_DIR"
cat > "$POISON_DIR/pogo" <<EOF
#!/usr/bin/env bash
echo "the installed pogo was invoked" > "$SANDBOX/poison.marker"
exit 66
EOF
chmod +x "$POISON_DIR/pogo"
rm -f "$SANDBOX/poison.marker"

poison_out="$(PATH="$POISON_DIR:$PATH" bash "$RUNNER" --skip-prompts --stamp "$STALE_STAMP" --now 2026-08-04T12:00:00Z 2>&1)"
poison_rc=$?

if [ -f "$SANDBOX/poison.marker" ]; then
    fail "the runner invoked the installed pogo — the witness would report whatever the last deploy left behind"
else
    pass "the runner never invoked the installed pogo"
fi
if [ "$poison_rc" -eq 66 ]; then
    fail "the runner returned the poisoned binary's exit status ($poison_rc)"
elif [ "$poison_rc" -eq 1 ]; then
    pass "the runner returned the witness's own exit status with a poisoned pogo on PATH"
else
    fail "the runner exited $poison_rc with a poisoned pogo on PATH, want 1 — output: $poison_out"
fi

# --- 3. QUIET — the same runner, a healthy night -----------------------------
# Same instant as section 1, so this measures the record and not the clock.
FRESH_STAMP="$SANDBOX/fresh.stamp"
printf '2026-08-04 1 0\n' > "$FRESH_STAMP"

green_out="$(bash "$RUNNER" --skip-prompts --stamp "$FRESH_STAMP" --now 2026-08-04T12:00:00Z 2>&1)"
green_rc=$?

if [ "$green_rc" -eq 0 ]; then
    pass "runner exits 0 on a healthy night"
else
    fail "runner exited $green_rc on a healthy night, want 0 — output: $green_out"
fi

# --- 4. It resolves its checkout from its own path, not from $PWD ------------
# A schedule or a cron fires from whatever directory it likes. If the runner
# resolved ./cmd/pogo against $PWD it would work in every hand-run test and fail
# the moment something automated invoked it.
cwd_out="$(cd / && bash "$RUNNER" --skip-prompts --stamp "$FRESH_STAMP" --now 2026-08-04T12:00:00Z 2>&1)"
cwd_rc=$?
if [ "$cwd_rc" -eq 0 ]; then
    pass "runner works when invoked from an unrelated directory"
else
    fail "runner exited $cwd_rc when run from / — output: $cwd_out"
fi

# --- 5. No `go` is reported, not papered over --------------------------------
# The failure mode being excluded is a silent fallback: a box without a toolchain
# quietly running the stale installed binary and reporting a clean night.
#
# The PATH here is the system one MINUS the toolchain, not an empty directory.
# An empty PATH is not the scenario — it loses `bash` itself, so the shell fails
# at 127 before the runner is ever entered, and the section's other two
# assertions then pass over a script that did not run. `go` lives in
# /opt/homebrew/bin or ~/go/bin on this box and is absent from /usr/bin and /bin,
# so the system pair is exactly "a usable machine with no toolchain"; the guard
# below refuses to assert anything if that ever stops being true.
SYSTEM_PATH="/usr/bin:/bin"
if PATH="$SYSTEM_PATH" command -v go >/dev/null 2>&1; then
    fail "go is on $SYSTEM_PATH, so the no-toolchain control cannot be constructed here"
else
    rm -f "$SANDBOX/poison.marker"
    nogo_out="$(PATH="$POISON_DIR:$SYSTEM_PATH" bash "$RUNNER" --skip-prompts --stamp "$STALE_STAMP" 2>&1)"
    nogo_rc=$?

    if [ "$nogo_rc" -ne 0 ] && [ "$nogo_rc" -ne 1 ] && [ "$nogo_rc" -ne 127 ]; then
        pass "runner fails loudly (exit $nogo_rc) when go is absent"
    else
        fail "runner exited $nogo_rc with no go on PATH — a check that could not run must not read as a verdict, and 127 would mean the runner itself never started"
    fi
    if [ -f "$SANDBOX/poison.marker" ]; then
        fail "with no go on PATH the runner fell back to the installed pogo — exactly the silent fallback this file exists to exclude"
    else
        pass "with no go on PATH the runner still refused the installed pogo"
    fi
    if printf '%s' "$nogo_out" | grep -q "not on PATH"; then
        pass "runner says why it could not run"
    else
        fail "runner gave no reason for refusing — output: $nogo_out"
    fi
fi

# --- tally -------------------------------------------------------------------
echo
echo "=== scripts/check-staleness.sh controls ==="
cat "$RESULTS_FILE"
fails=$(grep -c '^FAIL:' "$RESULTS_FILE" || true)
passes=$(grep -c '^PASS:' "$RESULTS_FILE" || true)
echo "$passes passed, $fails failed"
[ "$fails" -eq 0 ] || exit 1
[ "$passes" -gt 0 ] || { echo "no assertions ran"; exit 1; }
