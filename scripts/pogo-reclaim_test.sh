#!/usr/bin/env bash
# Tests for scripts/launchd/pogo-reclaim.sh — the size-triggered Go module
# cache reclaim (mg-b7c3).
#
# ---------------------------------------------------------------------------
# THE ASSERTIONS THIS SUITE EXISTS FOR
# ---------------------------------------------------------------------------
# The ticket's complaint is MISATTRIBUTION: a full disk that presents as a
# broken branch. A reclaim job is an artifact of the same kind as the defect it
# remedies, so the failure available to it is misattribution in the other
# direction — a job that ran, wrote a reassuring line, and left the disk full.
# Sections 2 and 6 are the two shapes of that, and everything else is scaffolding
# around them:
#
#   §2  Disk low, cache SMALL. The reclaim cannot help. It must NOT run
#       `go clean`, must NOT exit 0, and must say in words that the module cache
#       is not what is filling the volume. A free-space-only trigger passes this
#       section by deleting a 680M cache and reporting success, which is the
#       exact reading that costs a morning of wrong diagnosis.
#   §6  Disk low, cache LARGE, reclaim SUCCEEDS, disk STILL low. The job did
#       everything it can do and the condition persists. It must say so rather
#       than let a successful reclaim stand as "the disk is handled".
#
# Section 3 is the other half of the AND: a large cache on a healthy disk must
# not be deleted, and the `du` that would measure it must not even be paid.
#
# Nothing here stubs the script's logic. `df`, `du`, `go`, `pgrep`, `pogo` and
# `mg` are replaced by recording stubs on PATH — the real script runs, takes
# real branches, and every assertion is on what it did and what it said.
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/launchd/pogo-reclaim.sh"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create reclaim
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== pogo-reclaim.sh tests ==="

if [ ! -x "$SCRIPT" ]; then
    echo "  FAIL: $SCRIPT is missing or not executable" >&2
    echo "=== 0 passed, 1 failed ==="
    exit 1
fi

WORK=""
STUBS=""
OUT=""
RC=0

GB=1048576 # KB in a GiB

# fixture resets the canned world. Every knob is a file so a test can rewrite
# one line and re-run without rebuilding the stubs.
#
#   free/total/used   what `df -Pk` reports, in KB
#   cache             what `du -sk` reports, in KB ("" = du fails)
#   clean_rc          exit code of `go clean -modcache`
#   clean_frees       KB that free space gains when the clean succeeds
#   trim_frees        KB that free space gains once the build-cache trim has
#                     removed gocache/00/old1-a (the trim itself is REAL: a real
#                     `find -delete` against real files with real mtimes)
#   du.map            "<path>\t<kb>" rows for any path other than the module
#                     cache; kb "hang" makes that du sleep past any bound
fixture() {
    WORK="$(mktemp -d)"
    STUBS="$WORK/bin"
    mkdir -p "$STUBS" "$WORK/modcache" "$WORK/state"
    : > "$WORK/du.map"

    # A real Go build cache, laid out as Go lays it out. Ages in seconds.
    local gc="$WORK/gocache"
    mkdir -p "$gc/00" "$gc/ab" "$gc/fuzz"
    printf '%s\n' "This directory holds cached build artifacts from the Go build system." > "$gc/README"
    echo 1700000000 > "$gc/trim.txt"
    for f in 00/old1-a 00/old1-d ab/stale-d ab/new-a 00/notanentry fuzz/old-a; do echo x > "$gc/$f"; done
    age() { perl -e '$t = time - shift; utime $t, $t, @ARGV' "$@"; }
    age $((3 * 86400)) "$gc/00/old1-a" "$gc/00/old1-d" "$gc/00/notanentry" "$gc/fuzz/old-a" "$gc/README" "$gc/trim.txt"
    age $((25 * 3600)) "$gc/ab/stale-d"
    age $((60 * 60)) "$gc/ab/new-a" # used an hour ago: today's working set

    echo "0" > "$WORK/clean_rc"
    echo "0" > "$WORK/clean_frees"
    echo "[]" > "$WORK/refinery.json"
    : > "$WORK/calls.log"
    : > "$WORK/mail.log"

    cat > "$STUBS/df" <<'EOF'
#!/usr/bin/env bash
echo "df $*" >> "$WORK_DIR/calls.log"
[ -s "$WORK_DIR/df.broken" ] && { echo "df: nope" >&2; exit 1; }
free="$(cat "$WORK_DIR/free")"
total="$(cat "$WORK_DIR/total")"
if [ -s "$WORK_DIR/trim_frees" ] && [ ! -e "$WORK_DIR/gocache/00/old1-a" ]; then
    free=$((free + $(cat "$WORK_DIR/trim_frees")))
fi
if [ -s "$WORK_DIR/used" ]; then used="$(cat "$WORK_DIR/used")"; else used=$((total - free)); fi
echo "Filesystem 1024-blocks Used Available Capacity Mounted-on"
echo "/dev/fake $total $used $free 99% /Fake/Volume"
EOF

    cat > "$STUBS/du" <<'EOF'
#!/usr/bin/env bash
echo "du $*" >> "$WORK_DIR/calls.log"
for a in "$@"; do
    case "$a" in -*) continue ;; esac
    if [ "$a" = "$WORK_DIR/modcache" ]; then
        kb="$(cat "$WORK_DIR/cache" 2>/dev/null)"
        [ -z "$kb" ] && { echo "du: cannot read" >&2; exit 1; }
    else
        kb="$(awk -F '\t' -v p="$a" '$1 == p { print $2 }' "$WORK_DIR/du.map")"
        [ -z "$kb" ] && kb=0
    fi
    [ "$kb" = hang ] && exec sleep 30
    printf '%s\t%s\n' "$kb" "$a"
done
EOF

    cat > "$STUBS/sysctl" <<'EOF'
#!/usr/bin/env bash
[ "$*" = "-n vm.swapusage" ] && echo "total = 5120.00M  used = 3737.56M  free = 1382.44M  (encrypted)"
EOF

    cat > "$STUBS/go" <<'EOF'
#!/usr/bin/env bash
echo "go $*" >> "$WORK_DIR/calls.log"
case "$1 ${2:-}" in
    "env GOMODCACHE") echo "$WORK_DIR/modcache" ;;
    "env GOCACHE") echo "$WORK_DIR/gocache" ;;
    "clean -modcache")
        rc="$(cat "$WORK_DIR/clean_rc")"
        if [ "$rc" = "0" ]; then
            free="$(cat "$WORK_DIR/free")"
            echo $((free + $(cat "$WORK_DIR/clean_frees"))) > "$WORK_DIR/free"
            echo 0 > "$WORK_DIR/cache"
        else
            echo "go: clean failed" >&2
        fi
        exit "$rc" ;;
    *) exit 0 ;;
esac
EOF

    cat > "$STUBS/pgrep" <<'EOF'
#!/usr/bin/env bash
echo "pgrep $*" >> "$WORK_DIR/calls.log"
name="${!#}"
if [ -s "$WORK_DIR/pgrep.$name" ]; then cat "$WORK_DIR/pgrep.$name"; exit 0; fi
exit 1
EOF

    cat > "$STUBS/pogo" <<'EOF'
#!/usr/bin/env bash
echo "pogo $*" >> "$WORK_DIR/calls.log"
if [ "$1 $2" = "refinery queue" ]; then
    # The CLI writes advisories to stderr; emit one unconditionally so a reader
    # that folds stderr into the JSON fails this suite rather than the box.
    echo "advisory: this is stderr and must not reach the parser" >&2
    cat "$WORK_DIR/refinery.json" 2>/dev/null
fi
exit 0
EOF

    cat > "$STUBS/mg" <<'EOF'
#!/usr/bin/env bash
{ echo "mg $*"; echo "--- body ---"; cat; } >> "$WORK_DIR/mail.log"
exit "$(cat "$WORK_DIR/mg_rc" 2>/dev/null || echo 0)"
EOF

    chmod +x "$STUBS"/*
    echo 0 > "$WORK/mg_rc"
}

# run invokes the real script against the canned world.
run() {
    OUT="$(
        WORK_DIR="$WORK" \
        PATH="$STUBS:$PATH" \
        POGO_RECLAIM_STATE_DIR="$WORK/state" \
        POGO_RECLAIM_FREE_FLOOR_GB="${FREE_FLOOR:-20}" \
        POGO_RECLAIM_CACHE_FLOOR_GB="${CACHE_FLOOR:-5}" \
        POGO_RECLAIM_CRITICAL_FREE_GB="${CRITICAL_FREE:-2}" \
        POGO_HOME="$WORK/pogo" \
        POGO_RECLAIM_TMPDIR="$WORK/tmp" \
        POGO_RECLAIM_CLAUDE_TMP="$WORK/claude" \
        env "$@" bash "$SCRIPT" 2>&1
    )"
    RC=$?
}

cleanup() { [ -n "$WORK" ] && rm -rf "$WORK"; }

called() { grep -q "^$1" "$WORK/calls.log"; }
gc_has() { [ -e "$WORK/gocache/$1" ]; }
du_map() { printf '%s\t%s\n' "$1" "$2" >> "$WORK/du.map"; }

# ---------------------------------------------------------------------------
# 1. HEALTHY DISK — the steady state, and the case that must cost nothing
# ---------------------------------------------------------------------------
# The interval fires 48 times a day. If a fire on a healthy box paid for a `du`
# of a multi-gigabyte tree, the sampling rate would be the cost. So this asserts
# the ORDER of the measurements, not just the verdict: `du` must never run.
echo "-- 1. healthy disk: above the free floor --"
fixture
echo $((200 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache" # large cache — and irrelevant, because ANDed
run
[ "$RC" -eq 0 ] && pass "exits 0" || fail "expected 0, got $RC"
called "du -sk $WORK/modcache" && fail "measured the module cache on a healthy disk (that du must be paid only when the disk is low)" || pass "never paid for the module-cache du"
called "du -sk $WORK/gocache" && pass "paid exactly the build-cache cap du (the cap arm is on by default)" || fail "the cap arm did not measure the build cache"
gc_has 00/old1-a && pass "under the cap: trimmed nothing on a healthy disk" || fail "trimmed the build cache on a healthy disk with the cache under its cap"
cleanup

echo "-- 1b. healthy disk with the cap arm off: one df, and no du at all --"
fixture
echo $((200 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
run POGO_RECLAIM_BUILDCACHE_CAP_GB=0
[ "$RC" -eq 0 ] && pass "exits 0" || fail "expected 0, got $RC"
called du && fail "paid for a du on a healthy disk with the cap disabled" || pass "never paid for any du"
called "go clean" && fail "reclaimed a 7G cache on a box with 200G free" || pass "did not reclaim"
case "$OUT" in *"above the floor"*) pass "says why it did nothing" ;; *) fail "no explanation: $OUT" ;; esac
cleanup

# ---------------------------------------------------------------------------
# 2. THE MISATTRIBUTION CASE — disk low, cache small
# ---------------------------------------------------------------------------
# This is this box on 2026-08-12 after a manual clean: 99% full, 680M cache.
# A free-space-only trigger fires here, deletes 680M, exits 0 and logs a success.
# Every full-disk build failure after that gets attributed to the branch.
echo "-- 2. CANNOT HELP: disk low, cache small --"
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache" # 680M, the measured plateau
run
[ "$RC" -eq 4 ] && pass "exits 4 (CANNOT HELP), not 0" || fail "expected 4, got $RC — a reassuring exit 0 here is the defect"
called "go clean" && fail "ran go clean for 680M against a 453G fill" || pass "refused to fire"
case "$OUT" in *"is NOT what is filling this volume"*) pass "names the cache as not the cause" ;; *) fail "did not say the cache is not the cause: $OUT" ;; esac
case "$OUT" in *"needs a human, not a cron"*) pass "says what it cannot do" ;; *) fail "no cannot-fix statement" ;; esac
case "$OUT" in *"680M"*) pass "quotes the measured cache size" ;; *) fail "no cache size in output" ;; esac
grep -q "mg mail send human" "$WORK/mail.log" && pass "alerted a human" || fail "nobody was told outside the log"
grep -q "NOT an account of the volume" "$WORK/mail.log" && pass "the mail carries the scope caveat too" || fail "the caveat did not travel into the mail"
cleanup

# ---------------------------------------------------------------------------
# 3. THE RECLAIM ITSELF — disk low, cache large
# ---------------------------------------------------------------------------
# The condition that produced the ticket: 571 MiB free, 7.3G cache.
echo "-- 3. reclaim: disk low, cache large --"
fixture
echo $((10 * GB)) > "$WORK/free" # below the 20G floor, above the 2G critical
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB + 300 * 1024)) > "$WORK/cache"
echo $((7 * GB)) > "$WORK/clean_frees"
run
[ "$RC" -eq 0 ] && pass "exits 0" || fail "expected 0, got $RC: $OUT"
called "go clean -modcache" && pass "ran go clean -modcache" || fail "did not reclaim"
case "$OUT" in *"reclaimed 7.0G"*) pass "reports the reclaimed amount from a re-measurement, not from the du" ;; *) fail "no reclaimed figure: $OUT" ;; esac
case "$OUT" in *"WHAT THIS DOES NOT FIX"*) pass "prints the scope note on a SUCCESSFUL run" ;; *) fail "the caveat is missing from the success path — which is the path that gets read" ;; esac
case "$OUT" in *"HEADROOM, NOT A FIX"*) pass "says headroom, not a fix" ;; *) fail "no headroom/fix distinction" ;; esac
cleanup

# ---------------------------------------------------------------------------
# 4. THE SCOPE NOTE IS COMPUTED, NOT A FIXED SENTENCE
# ---------------------------------------------------------------------------
# A hardcoded "the volume is still 99% full" would be right today and wrong
# forever after, and a caveat that has gone stale is worse than none: it is a
# false statement in the place a reader looks for the true one. So the same code
# path must produce different numbers for different fills.
echo "-- 4. the scope note reports THIS run's numbers --"
fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo $((7 * GB)) > "$WORK/clean_frees"
run
NOTE_A="$(printf '%s\n' "$OUT" | grep '% capacity:' | head -1)"
cleanup

fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo $((300 * GB)) > "$WORK/clean_frees" # a fill that mostly went away
run
NOTE_B="$(printf '%s\n' "$OUT" | grep '% capacity:' | head -1)"
cleanup

if [ -n "$NOTE_A" ] && [ -n "$NOTE_B" ] && [ "$NOTE_A" != "$NOTE_B" ]; then
    pass "two different fills produce two different statements"
    echo "      A: $NOTE_A"
    echo "      B: $NOTE_B"
else
    fail "the scope note did not vary with the fill (A='$NOTE_A' B='$NOTE_B')"
fi

# ---------------------------------------------------------------------------
# 5. IN-FLIGHT BUILDS
# ---------------------------------------------------------------------------
# `go clean -modcache` deletes trees a running build is READING. A build that
# loses them fails with a missing-file error — which reads like a broken branch,
# i.e. this ticket's own complaint, caused by this ticket's own fix.
echo "-- 5. deferral while something is building --"
fixture
echo $((10 * GB)) > "$WORK/free" # low, but well above the 2G critical floor
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo "4242" > "$WORK/pgrep.go"
run
[ "$RC" -eq 5 ] && pass "exits 5 (DEFERRED)" || fail "expected 5, got $RC"
called "go clean" && fail "deleted the module cache out from under a running build" || pass "did not touch the cache"
case "$OUT" in *"DEFERRED"*) pass "says it deferred and why" ;; *) fail "no deferral line: $OUT" ;; esac
cleanup

echo "-- 5a. the in-flight probe cannot be blind to its own ancestors (mg-19e4) --"
# `pgrep` excludes the calling process AND ALL OF ITS ANCESTORS unless passed -a
# (man pgrep). This count is a DEFER-GUARD, so the direction of that error is
# the dangerous one: a reclaim running underneath a build is told nothing is
# building and deletes the module cache the compile is reading — this suite's
# §5 complaint, produced by §5's own remedy.
#
# THE CONTROL IS LIVE, not stubbed, because the property under test belongs to
# the real pgrep and nothing about a stub could establish it. A symlink to
# /bin/sh gives an ancestor with a unique exact process name, so the two
# readings differ only in the flag.
ANC_DIR="$(mktemp -d)"
ln -sf /bin/sh "$ANC_DIR/pogoreclaimanc"
ANC_OUT="$("$ANC_DIR/pogoreclaimanc" -c 'echo "plain=$(pgrep -x pogoreclaimanc | wc -l | tr -d " ") ancestors=$(pgrep -ax pogoreclaimanc | wc -l | tr -d " ")"' 2>/dev/null)"
case "$ANC_OUT" in
    "plain=0 ancestors="[1-9]*)
        pass "premise: pgrep -x reports 0 for a process that IS the caller's ancestor, while pgrep -ax finds it ($ANC_OUT)" ;;
    *)
        fail "the ancestor-exclusion control did not reproduce ($ANC_OUT) — the -a below may be load-bearing for a reason this box no longer exhibits, or may have stopped being needed" ;;
esac
rm -rf "$ANC_DIR"

# And the script must actually pass the flag. The stub records its whole argv,
# so this reads the invocation rather than the source: a rewrite that keeps the
# comment and drops the flag fails here.
fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
run
called "pgrep -ax go" \
    && pass "the in-flight process check asks pgrep to include ANCESTORS — without -a a reclaim under a build reads as a quiet box" \
    || fail "the in-flight check ran pgrep without -a: $(grep '^pgrep' "$WORK/calls.log" | tr '\n' ' ')"
cleanup

echo "-- 5b. deferral on an in-flight refinery merge --"
fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
cat > "$WORK/refinery.json" <<'EOF'
[{"id":"mr-1","status":"processing"},{"id":"mr-2","status":"queued"}]
EOF
run
[ "$RC" -eq 5 ] && pass "exits 5 on a processing MR" || fail "expected 5, got $RC: $OUT"
called "go clean" && fail "ran a modcache clean during a merge gate" || pass "did not touch the cache"
case "$OUT" in *"refinery merge"*) pass "names the refinery as the reason" ;; *) fail "did not name the merge: $OUT" ;; esac
cleanup

echo "-- 5c. the critical floor overrides the deferral --"
fixture
echo $((1 * GB / 2)) > "$WORK/free" # ~500M — the observed 571 MiB
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo $((7 * GB)) > "$WORK/clean_frees"
echo "4242" > "$WORK/pgrep.go"
run
[ "$RC" -eq 0 ] && pass "reclaims anyway below the critical floor" || fail "expected 0, got $RC: $OUT"
called "go clean -modcache" && pass "ran the clean" || fail "deferred at 500M free — the in-flight build fails either way"
case "$OUT" in *"NOT deferring"*) pass "states the override" ;; *) fail "override not stated: $OUT" ;; esac
cleanup

echo "-- 5d. an unanswerable in-flight check proceeds and SAYS SO --"
# Deferring forever on a question that cannot be answered is how the disk fills:
# a full volume breaks EVERY build, an ill-timed clean breaks one. So the tie is
# broken toward proceeding — but the log must not then read as a clean check.
fixture
echo $((10 * GB)) > "$WORK/free" # above the critical floor, so the check is REACHED
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo $((7 * GB)) > "$WORK/clean_frees"
rm -f "$WORK/refinery.json" # daemon down: the query returns nothing
run
[ "$RC" -eq 0 ] && pass "daemon down: proceeds rather than blocking" || fail "expected 0, got $RC: $OUT"
case "$OUT" in *"in-flight check PARTIAL"*) pass "reports the check as PARTIAL rather than passing silently" ;; *) fail "silently treated an unmade check as a pass: $OUT" ;; esac
cleanup

fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo $((7 * GB)) > "$WORK/clean_frees"
rm -f "$STUBS/pogo" # the CLI is not installed at all
run PATH="$STUBS:/usr/bin:/bin"
[ "$RC" -eq 0 ] && pass "no \`pogo\` on PATH: proceeds" || fail "expected 0, got $RC: $OUT"
case "$OUT" in *"\`pogo\` is not on PATH"*) pass "names the missing CLI" ;; *) fail "did not name what was missing: $OUT" ;; esac
cleanup

echo "-- 5e. the queue parse reads STDOUT ONLY --"
# The CLI writes advisories to stderr (the stub emits one on every call). A
# reader that folds them into the JSON gets an unparseable blob, which renders
# as "no MRs" — a deferral check that silently stops deferring.
fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
cat > "$WORK/refinery.json" <<'JSON'
[{"id":"mr-1","status":"processing"}]
JSON
run
[ "$RC" -eq 5 ] && pass "still sees the processing MR through the stderr advisory" || fail "expected 5, got $RC: $OUT"
cleanup

# ---------------------------------------------------------------------------
# 6. THE RECLAIM WORKED AND THE DISK IS STILL FULL
# ---------------------------------------------------------------------------
# The second shape of "but we installed the cron". Everything succeeded and the
# condition persists.
echo "-- 6. still below the floor after a successful reclaim --"
fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo $((7 * GB)) > "$WORK/clean_frees" # 8G free afterwards: still under the 20G floor
run
[ "$RC" -eq 0 ] && pass "exits 0 — the reclaim genuinely succeeded" || fail "expected 0, got $RC"
case "$OUT" in *"STILL BELOW THE FLOOR"*) pass "says the reclaim was not enough" ;; *) fail "a successful reclaim that fixed nothing reported only success: $OUT" ;; esac
grep -q "mg mail send human" "$WORK/mail.log" && pass "alerted a human" || fail "nobody was told the reclaim was not enough"
cleanup

# ---------------------------------------------------------------------------
# 7. THINGS THAT CANNOT BE MEASURED MUST NOT RENDER AS A PASS
# ---------------------------------------------------------------------------
echo "-- 7. unknowns --"
fixture
echo $((1 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
rm -f "$STUBS/go"
run PATH="$STUBS:/usr/bin:/bin"
[ "$RC" -eq 3 ] && pass "no \`go\`: exits 3 UNKNOWN" || fail "expected 3, got $RC: $OUT"
case "$OUT" in *UNKNOWN*) pass "says UNKNOWN" ;; *) fail "did not say UNKNOWN" ;; esac
cleanup

fixture
echo $((1 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo "x" > "$WORK/df.broken"
run
[ "$RC" -eq 3 ] && pass "unreadable df: exits 3 UNKNOWN" || fail "expected 3, got $RC: $OUT"
cleanup

fixture
echo $((1 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
: > "$WORK/cache" # du fails
run
[ "$RC" -eq 3 ] && pass "unreadable du: exits 3, not 0 and not 4" || fail "expected 3, got $RC: $OUT"
case "$OUT" in *"did not establish whether the cache is why"*) pass "distinguishes 'could not measure' from 'the cache is innocent'" ;; *) fail "an unmeasured cache read as a verdict: $OUT" ;; esac
cleanup

fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
echo "1" > "$WORK/clean_rc"
run
[ "$RC" -eq 1 ] && pass "a failed go clean exits 1" || fail "expected 1, got $RC: $OUT"
case "$OUT" in *"was NOT reclaimed"*) pass "does not claim a reclaim that did not happen" ;; *) fail "reported a reclaim after a failed clean: $OUT" ;; esac
cleanup

# ---------------------------------------------------------------------------
# 8. THE ALERT IS RATE-LIMITED
# ---------------------------------------------------------------------------
# A disk that stays full is the normal case for the cannot-help verdict, and a
# mail every 30 minutes is how a true signal gets filtered into a mail rule.
echo "-- 8. alert cooldown --"
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
run
FIRST="$(grep -c 'mg mail send' "$WORK/mail.log")"
run
SECOND="$(grep -c 'mg mail send' "$WORK/mail.log")"
[ "$FIRST" -eq 1 ] && [ "$SECOND" -eq 1 ] && pass "the second fire within the cooldown sends nothing" || fail "sent $FIRST then $SECOND mails"
case "$OUT" in *"alert suppressed"*) pass "logs the suppression rather than going quiet" ;; *) fail "suppressed silently" ;; esac
run POGO_RECLAIM_ALERT_COOLDOWN=0
THIRD="$(grep -c 'mg mail send' "$WORK/mail.log")"
[ "$THIRD" -eq 2 ] && pass "sends again once the cooldown elapses" || fail "expected 2 mails, got $THIRD"
cleanup

echo "-- 8b. a mail that could not be sent is not recorded as sent --"
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
echo 1 > "$WORK/mg_rc"
run
case "$OUT" in *"alert NOT SENT"*) pass "says nobody was told" ;; *) fail "a failed send read as a send: $OUT" ;; esac
echo 0 > "$WORK/mg_rc"
run
grep -q "mg mail send" "$WORK/mail.log" && pass "retries on the next fire (the cooldown stamp was not written)" || fail "a failed send burned the cooldown"
cleanup

# ---------------------------------------------------------------------------
# 9. CONCURRENCY AND DRY RUN
# ---------------------------------------------------------------------------
echo "-- 9. lock and dry-run --"
fixture
echo $((1 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
mkdir -p "$WORK/state/reclaim.lock.d" # a fire already in progress
run
[ "$RC" -eq 0 ] && pass "a held lock exits 0" || fail "expected 0, got $RC"
called "go clean" && fail "two concurrent go clean runs against one cache" || pass "did not run a second clean"
cleanup

fixture
echo $((1 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
run POGO_RECLAIM_DRY_RUN=1
[ "$RC" -eq 0 ] && pass "dry run exits 0" || fail "expected 0, got $RC"
called "go clean" && fail "a dry run deleted the cache" || pass "dry run removed nothing"
case "$OUT" in *"DRY RUN"*) pass "labels itself a dry run" ;; *) fail "unlabelled dry run" ;; esac
cleanup

# ---------------------------------------------------------------------------
# 10. THE STATIC-COPY TRAP
# ---------------------------------------------------------------------------
# launchd runs ~/.pogo/bin/pogo-reclaim.sh, which a merge does not refresh. The
# only way a log can answer "which version ran" is if the run says so itself.
echo "-- 10. every fire identifies the copy that ran --"
fixture
echo $((200 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
run
case "$OUT" in *"runner: $SCRIPT"*) pass "logs its own path" ;; *) fail "no runner line: $OUT" ;; esac
case "$OUT" in *"install-reclaim"*) pass "names the command that refreshes the static copy" ;; *) fail "the static-copy remedy is not in the log" ;; esac
cleanup

# ---------------------------------------------------------------------------
# 11. THE PERCENTAGE MUST BE THE ONE `df` PRINTS
# ---------------------------------------------------------------------------
# Found by running the real script against the real box rather than by reading
# it. On APFS the volume reserves space, so total != used + available: this host
# measured 415.1G used of a 460.4G volume with 7.0G free. used/total is 90%;
# `df -h` prints 99%, because df's Capacity column is used/(used+available) and
# rounds UP.
#
# A log line reading "90% used" while every operator's `df -h` says 99% is an
# instrument arguing with the tool it exists to agree with, on the single number
# the whole alarm turns on — and 90% reads as comfortable.
echo "-- 11. capacity matches df, not used/total --"
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((415 * GB)) > "$WORK/used" # APFS: 415 + 7 != 460
echo $((680 * 1024)) > "$WORK/cache"
run
case "$OUT" in
    *"99% capacity"*) pass "reports 99% — df's denominator, rounded up as df does" ;;
    *"90%"*) fail "reported 90% (used/total): comfortable, and nine points from what df prints" ;;
    *) fail "no capacity figure: $OUT" ;;
esac
case "$OUT" in *"% used"*) fail "still calls it '% used' — the number is a capacity, and the label is what makes it comparable" ;; *) pass "labels it capacity" ;; esac
cleanup

# ---------------------------------------------------------------------------
# 12. THE BUILD-CACHE ARM (mg-925f7): an age trim, never `go clean -cache`
# ---------------------------------------------------------------------------
# The trim runs a REAL `find -delete` against a fixture laid out as Go lays out
# its cache, with real mtimes. What it must delete: *-a/*-d entries in the
# two-hex shards older than 24h. What it must not: an entry a build used an
# hour ago, Go's own README/trim.txt, a non-entry file, the fuzz cache.
echo "-- 12. low disk: the build-cache trim runs first, and can be enough --"
fixture
echo $((10 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
echo $((15 * GB)) > "$WORK/trim_frees" # 25G free afterwards: above the 20G floor
run
[ "$RC" -eq 0 ] && pass "exits 0 once the trim brings free space above the floor" || fail "expected 0, got $RC: $OUT"
gc_has 00/old1-a || gc_has 00/old1-d || gc_has ab/stale-d && fail "left an entry older than 24h in place" || pass "removed every entry older than 24h (3d and 25h)"
gc_has ab/new-a && pass "kept the entry a build used an hour ago" || fail "deleted today's working set"
gc_has README && gc_has trim.txt && pass "kept Go's README and trim.txt" || fail "deleted Go's own bookkeeping"
gc_has 00/notanentry && gc_has fuzz/old-a && pass "kept the non-entry file and the fuzz cache" || fail "deleted outside the *-a/*-d shard entries"
called "go clean -cache" && fail "ran go clean -cache" || pass "never ran go clean -cache"
called "du -sk $WORK/modcache" && fail "measured the module cache after the trim had already fixed the disk" || pass "did not pay for the module-cache du"
case "$OUT" in *"removed 3 entries unused for >24h"*) pass "says how many entries it removed and the age" ;; *) fail "no trim count: $OUT" ;; esac
case "$OUT" in *"brought free space above the floor"*) pass "says the trim was enough" ;; *) fail "did not say why it stopped: $OUT" ;; esac
cleanup

echo "-- 12a. low disk: the trim is not enough, the module cache is small -> CANNOT HELP --"
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
echo $((2 * GB)) > "$WORK/trim_frees"
run
[ "$RC" -eq 4 ] && pass "exits 4 only after the trim has done what it can" || fail "expected 4, got $RC: $OUT"
gc_has 00/old1-a && fail "reached CANNOT HELP without trimming" || pass "trimmed before the verdict"
grep -q "Build-cache trim (mg-925f7) on this fire: removed 3 entries" "$WORK/mail.log" && pass "the alert says what the trim did" || fail "the alert does not say what the build-cache trim did"
grep -q "7.0G -> 9.0G" "$WORK/mail.log" && pass "the alert quotes the trim's measured free-space change" || fail "no before/after free figure in the alert"
cleanup

echo "-- 12b. a build in flight defers the trim, and the alert still goes out --"
# Exiting 5 at the first sign of a build would silence the alert on a box that is
# always building — which this one is. So the trim defers, the module-cache
# verdict is still reached, and a human is still told.
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
echo "4242" > "$WORK/pgrep.go"
run
[ "$RC" -eq 5 ] && pass "exits 5 (DEFERRED): the job may yet help on the next fire" || fail "expected 5, got $RC: $OUT"
gc_has 00/old1-a && pass "deleted nothing while a build was running" || fail "trimmed the build cache under a running build"
grep -q "Build-cache trim (mg-925f7) on this fire: DEFERRED" "$WORK/mail.log" && pass "the alert says the trim was deferred, and why" || fail "the deferral did not reach the alert"
cleanup

echo "-- 12c. the critical floor overrides the trim's deferral --"
fixture
echo $((1 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
echo "4242" > "$WORK/pgrep.go"
run
gc_has 00/old1-a && fail "deferred the trim at 1G free" || pass "trims anyway below the critical floor"
[ "$(printf '%s\n' "$OUT" | grep -c 'NOT deferring')" -eq 1 ] && pass "states the override once" || fail "the override was stated $(printf '%s\n' "$OUT" | grep -c 'NOT deferring') times"
cleanup

echo "-- 12d. a GOCACHE without Go's README is refused, not trimmed --"
fixture
rm -f "$WORK/gocache/README"
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
run
gc_has 00/old1-a && pass "deleted nothing from an untrusted directory" || fail "ran find -delete in a directory that is not provably a Go build cache"
case "$OUT" in *"REFUSED"*"no Go build-cache README"*) pass "says why it refused" ;; *) fail "silent refusal: $OUT" ;; esac
cleanup

echo "-- 12e. the cap arm: an over-cap cache is trimmed on a healthy disk --"
fixture
echo $((200 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
du_map "$WORK/gocache" $((45 * GB))
run
[ "$RC" -eq 0 ] && pass "exits 0" || fail "expected 0, got $RC: $OUT"
gc_has 00/old1-a && fail "an over-cap cache was not trimmed" || pass "trimmed an over-cap cache"
gc_has ab/new-a && pass "the cap arm kept today's working set too" || fail "the cap arm deleted today's working set"
case "$OUT" in *"over its 40.0G cap"*) pass "names the cap" ;; *) fail "did not name the cap: $OUT" ;; esac
called "du -sk $WORK/modcache" && fail "a healthy-disk fire measured the module cache" || pass "did not touch the module-cache arm"
cleanup

fixture
echo $((200 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
du_map "$WORK/gocache" $((45 * GB))
run POGO_RECLAIM_BUILDCACHE_CAP_GB=50
gc_has 00/old1-a && pass "the cap is configurable (45G under a 50G cap: untouched)" || fail "ignored POGO_RECLAIM_BUILDCACHE_CAP_GB"
cleanup

echo "-- 12f. the free floor is configurable for the trim too --"
fixture
echo $((30 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
FREE_FLOOR=40 run POGO_RECLAIM_ALERT_COOLDOWN=999999
gc_has 00/old1-a && fail "30G free under a 40G floor did not trim" || pass "POGO_RECLAIM_FREE_FLOOR_GB moves the trim's trigger"
cleanup

echo "-- 12g. dry run and the age knob --"
fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((7 * GB)) > "$WORK/cache"
run POGO_RECLAIM_DRY_RUN=1
gc_has 00/old1-a && gc_has ab/stale-d && pass "a dry run deleted nothing from the build cache" || fail "a dry run trimmed the build cache"
case "$OUT" in *"DRY RUN: would remove 3 build-cache entries"*) pass "a dry run counts what it would remove" ;; *) fail "no dry-run count: $OUT" ;; esac
cleanup

fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
run POGO_RECLAIM_BUILDCACHE_AGE_HOURS=48
gc_has ab/stale-d && ! gc_has 00/old1-a && pass "AGE_HOURS=48 keeps a 25h entry and removes a 3d one" || fail "the age knob was not honoured"
cleanup

fixture
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
run POGO_RECLAIM_BUILDCACHE_AGE_HOURS=0
gc_has ab/new-a && pass "AGE_HOURS=0 is clamped: an entry used an hour ago survives" || fail "an age under Go's 1h mtime refresh deleted a live entry"
cleanup

# ---------------------------------------------------------------------------
# 13. THE CONSUMER REPORT IS MEASURED ON THE FIRE, NOT QUOTED FROM AUGUST
# ---------------------------------------------------------------------------
# The alert used to quote "Library 73G, tools 15G, ..." measured 2026-08-12, and
# was still mailing it in November. It must now be a du taken on the fire, of
# the paths pogo and its agents write to — and it must not pretend the list
# accounts for the volume (swap in /System/Volumes/VM, pogo#258, is invisible
# to any du of the Data volume).
echo "-- 13. live per-path du in the alert --"
fixture
mkdir -p "$WORK/pogo/polecats" "$WORK/pogo/refinery" "$WORK/claude" "$WORK/tmp/TestFoo123" "$WORK/tmp/go-build42" "$WORK/tmp/tmp.abc"
du_map "$WORK/gocache" $((12 * GB))
du_map "$WORK/pogo/polecats" $((15 * GB))
du_map "$WORK/pogo/refinery" $((3 * GB))
du_map "$WORK/claude" $((20 * GB))
du_map "$WORK/tmp" $((9 * GB))
du_map "$WORK/tmp/TestFoo123" $((1 * GB))
du_map "$WORK/tmp/go-build42" $((1 * GB))
du_map "$WORK/tmp/tmp.abc" $((6 * GB))
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
run
# row LABEL VALUE: some ONE line of the output names LABEL and carries VALUE.
row() { printf '%s\n' "$OUT" | grep -F -- "$1" | grep -qF -- "$2"; }
for spec in "Go build cache  $WORK/gocache|12.0G" "polecat worktrees  $WORK/pogo/polecats|15.0G" \
    "refinery  $WORK/pogo/refinery|3.0G" "Claude scratch root  $WORK/claude|20.0G" \
    "\$TMPDIR, all of it  $WORK/tmp|9.0G" "of which Go test fixtures (Test*, go-build*)|2.0G" \
    "of which mktemp -d dirs (tmp.*)|6.0G" "Go module cache  $WORK/modcache|680M" "~/.cache  |absent"; do
    row "${spec%|*}" "${spec##*|}" && pass "row: ${spec%%  *} = ${spec##*|}" || fail "no row '${spec%|*}' carrying '${spec##*|}': $OUT"
done
grep -qF "20.0G  Claude scratch root  $WORK/claude" "$WORK/mail.log" && grep -q "polecat worktrees" "$WORK/mail.log" && pass "the rows travel into the mail" || fail "the mail does not carry the live rows"
# 12 + 15 + 3 + 20 + 9 + 0.66 = 59.66G; the breakdown rows are not double-counted.
grep -q "These rows sum to 59.7G of the 453.0G used; the other 393.3G is NOT attributed here" "$WORK/mail.log" \
    && pass "states the sum, the used figure and the unattributed rest (breakdown rows not double-counted)" \
    || fail "no sum/unattributed line: $(grep 'rows sum' "$WORK/mail.log")"
grep -q "NOT an account of the volume" "$WORK/mail.log" && pass "says the list is not an account of the volume" || fail "the list reads as a full account"
grep -q "/System/Volumes/VM" "$WORK/mail.log" && pass "names swap on the VM volume as invisible to this du" || fail "does not mention the VM volume"
grep -q "Swap now (sysctl vm.swapusage): total = 5120.00M" "$WORK/mail.log" && pass "quotes sysctl vm.swapusage" || fail "no swap reading"
grep -q "2026-08-12\|Library 73G" "$WORK/mail.log" && fail "still mails the hardcoded August list" || pass "the August list is gone from the mail"
case "$OUT" in *"Library 73G"*) fail "still logs the hardcoded August list" ;; *) pass "the August list is gone from the log" ;; esac
[ "$(grep -c "^du -sk $WORK/claude" "$WORK/calls.log")" -eq 1 ] && pass "each path is measured once per fire, though the log and the mail both print it" || fail "measured the same path $(grep -c "^du -sk $WORK/claude" "$WORK/calls.log") times"
cleanup

echo "-- 13a. a du that hangs is bounded and reported, never a size --"
fixture
mkdir -p "$WORK/claude"
du_map "$WORK/claude" hang
echo $((7 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
echo $((680 * 1024)) > "$WORK/cache"
T0="$(date +%s)"
run POGO_RECLAIM_DU_TIMEOUT_SEC=1
T1="$(date +%s)"
[ "$RC" -eq 4 ] && pass "the fire completes with its verdict" || fail "expected 4, got $RC: $OUT"
[ $((T1 - T0)) -lt 15 ] && pass "bounded: $((T1 - T0))s, not the stub's 30s" || fail "took $((T1 - T0))s — the du was not bounded"
row "Claude scratch root  $WORK/claude" "TIMED OUT after 1s" && pass "reports the timed-out row as TIMED OUT" || fail "the hung row was not reported: $OUT"
grep -q "sum to at least" "$WORK/mail.log" && pass "the sum is a lower bound when a row is unmeasured" || fail "the sum reads as exact despite a missing row"
cleanup

# ---------------------------------------------------------------------------
# 14. "MERGED IS NOT LIVE" CAN BE CHECKED BY A HASH
# ---------------------------------------------------------------------------
echo "-- 14. the runner line carries the copy's sha256 --"
fixture
echo $((200 * GB)) > "$WORK/free"
echo $((460 * GB)) > "$WORK/total"
run
SHA="$(shasum -a 256 "$SCRIPT" 2>/dev/null || sha256sum "$SCRIPT")"
SHA="${SHA%% *}"
case "$OUT" in *"runner: $SCRIPT"*"sha256 $SHA"*) pass "logs sha256 $SHA" ;; *) fail "no sha256 on the runner line: $OUT" ;; esac
cleanup

echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
