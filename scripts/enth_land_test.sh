#!/usr/bin/env bash
# =============================================================================
# enth_land_test.sh — enth-vcs slice 7 (mg-1fe2b), end to end: a REAL pogod
# lands enth work through a REAL enthd (docs/design/enth-vcs.md §6.1–§6.4,
# §16 row 7).
#
#   1  a sandboxed enthd serving alice/proj (enth-written), a read checkout of
#      it registered with a sandboxed pogod and selected as enth; a gate that
#      records the Change it gated
#   2  CLEAN LAND: `pogo refinery submit --item` → merged; the MR's enth
#      record names the trunk's new tip, merged_sha is empty, and the gate ran
#      on exactly that tip (a gated tree equal to the landed tree)
#   3  CONFLICT: work that conflicts with the trunk fails class=defect at the
#      dry run, naming the path; the trunk does not move and the gate never runs
#   4  STALE TIP: another land wins the race while the gate runs; the land is
#      refused stale_tip, the MR re-runs from the dry run and lands
#   5  RESULT MISMATCH: a land whose --expect-new is not the dry run's result
#      is refused result_mismatch; the retry lands with the gate held
#   6  KILL POGOD MID-LAND: enth carries the land out and pogod is SIGKILLed
#      before it sees the answer; a restarted pogod replays the land under its
#      request id, records the MR merged, and lands nothing twice
#   7  THE GIT CONTROL: the same daemon merges a git branch through a bare
#      origin as it always did — a merged_sha, no enth record
#
# The kill and the mismatch are made by a wrapper `enth` first on pogod's
# PATH, which passes every call through to the real enth unless a control
# file in $S/ctl asks it to interfere with the next real land.
#
# WHICH enth. $POGO_ENTH_BIN_DIR, else the first enthd on PATH. An enth
# without `land --expect-new` (enth #9 item D) SKIPS with exit 0;
# POGO_ENTH_REQUIRE=1 turns the skip into a failure.
#
# Exit: 0 all observations made (or skipped); 1 an observation failed; 99 the
# sandbox could not be established.
# =============================================================================

set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/pogo-sandbox"

RUN=0
PASSED=0
step() { echo; echo "===== $* ====="; }
note() { echo "  - $*"; }
ok()   { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo "  PASS  $*"; }
bad()  { RUN=$((RUN+1)); echo "  FAIL  $*" >&2; }
die()  { echo "FATAL: $*" >&2; exit 99; }
skip() {
    if [ "${POGO_ENTH_REQUIRE:-}" = 1 ]; then
        echo "enth_land_test.sh: FAIL (POGO_ENTH_REQUIRE=1): $1" >&2
        exit 1
    fi
    echo "enth_land_test.sh: SKIP — $1"
    exit 0
}

EBIN="${POGO_ENTH_BIN_DIR:-}"
if [ -z "$EBIN" ]; then
    EBIN="$(command -v enthd 2>/dev/null)" && EBIN="$(dirname "$EBIN")" || skip "no enthd on PATH and POGO_ENTH_BIN_DIR unset"
fi
for b in enth enthd enth-store enth-change enth-land; do
    [ -x "$EBIN/$b" ] || skip "$EBIN has no $b"
done
"$EBIN/enth" land --help 2>&1 | grep -q -- '--expect-new' || skip "$EBIN/enth land has no --expect-new (enth #9 item D)"

step "1. build pogo + pogod, isolate"
pogo_sandbox_create enthland
cleanup() {
    [ -n "${ENTHD_PID:-}" ] && kill "$ENTHD_PID" 2>/dev/null
    if [ -z "${ENTHLAND_KEEP:-}" ]; then
        pogo_sandbox_down
    else
        pogo_sandbox_daemon_kill
        sandbox_port_release
        echo "KEPT: $POGO_SANDBOX_DIR"
    fi
}
trap cleanup EXIT INT TERM HUP

BIN_DIR="$POGO_SANDBOX_DIR/bin"
mkdir -p "$BIN_DIR"
( cd "$REPO_ROOT" && GOBIN="$BIN_DIR" go install -p "${POGO_WORKER_CORES:-2}" ./cmd/pogo ./cmd/pogod ) || die "go install failed"
command -v mg >/dev/null || die "mg is not on PATH"
pogo_sandbox_isolate
export TMPDIR="$POGO_SANDBOX_DIR/tmp"
mkdir -p "$TMPDIR"
S="$POGO_SANDBOX_DIR"

# The wrapper enth, ahead of the real one on pogod's PATH.
WBIN="$S/wrap"; CTL="$S/ctl"
mkdir -p "$WBIN" "$CTL"
cat > "$WBIN/enth" <<EOF
#!/bin/sh
REAL="$EBIN/enth"; CTL="$CTL"
EOF
cat >> "$WBIN/enth" <<'EOF'
real=0
case " $* " in *" land "*) case " $* " in *" --dry-run "*) ;; *) case " $* " in *" --request-id "*) real=1 ;; esac ;; esac ;; esac
if [ $real = 1 ] && [ -f "$CTL/mismatch-once" ]; then
  rm -f "$CTL/mismatch-once"
  n=$#; prev=""
  for a in "$@"; do b="$a"; [ "$prev" = "--expect-new" ] && b="$(cat "$CTL/bogus")"; set -- "$@" "$b"; prev="$a"; done
  shift $n
fi
if [ $real = 1 ] && [ -f "$CTL/kill-after-land" ]; then
  rm -f "$CTL/kill-after-land"
  "$REAL" "$@" > "$CTL/landed-before-kill" 2>&1
  echo "$PPID" > "$CTL/killed-pid"
  kill -9 "$PPID"
  exit 4
fi
exec "$REAL" "$@"
EOF
chmod +x "$WBIN/enth"
export PATH="$BIN_DIR:$WBIN:$EBIN:$PATH"
note "sandbox: $S"
note "enth: $EBIN (wrapped by $WBIN/enth)"

step "2. enthd serving an enth-written repo"
R="$S/enthd"
UOUT="$("$EBIN/enthd" --root "$R" user add alice --email alice@example.com)" || die "enthd user add failed"
ENTH_TOKEN="$(printf '%s' "$UOUT" | sed -n 's/.*"token":"\(enth_[0-9a-f]*\)".*/\1/p')"
[ -n "$ENTH_TOKEN" ] || die "enthd user add printed no token"
export ENTH_TOKEN
export ENTH_AUTHOR_NAME=worker ENTH_AUTHOR_EMAIL=worker@example.com ENTH_COMMITTER_NAME=worker ENTH_COMMITTER_EMAIL=worker@example.com
"$EBIN/enthd" --root "$R" repo create alice/proj --owner alice >/dev/null || die "enthd repo create failed"
P="$R/repos/alice~proj"
mkdir -p "$S/w"
printf 'a1\na2\n' > "$S/w/a"; printf 'b1\n' > "$S/w/b"
hash_of() { sed -n 's/.*"hash":"\([0-9a-f]*\)".*/\1/p'; }
HA="$("$EBIN/enth-store" --store "$P" put "$S/w/a" | hash_of)"
HB="$("$EBIN/enth-store" --store "$P" put "$S/w/b" | hash_of)"
printf '{"parents":[],"author":{"name":"ann","email":"ann@example.com","time":1700000000,"tz":"+0000"},"committer":{"name":"ann","email":"ann@example.com","time":1700000000,"tz":"+0000"},"message":"root\\n","ops":[{"path":"a.txt","op":"put","blob":"%s","mode":"file"},{"path":"src/b.txt","op":"put","blob":"%s","mode":"file"}]}' "$HA" "$HB" > "$S/w/spec"
"$EBIN/enth-change" build --record "$S/w/rec" "$S/w/spec" >/dev/null || die "enth-change build failed"
ROOT_ID="$("$EBIN/enth-store" --store "$P" put "$S/w/rec" | hash_of)"
"$EBIN/enth-land" --store "$P" --user operator "$ROOT_ID" >/dev/null || die "operator land of the root failed"
"$EBIN/enthd" --root "$R" serve --listen 127.0.0.1:0 --follow-interval 0 > "$S/enthd.out" 2> "$S/enthd.err" &
ENTHD_PID=$!
for _ in $(seq 1 100); do grep -q '"type":"listening"' "$S/enthd.out" 2>/dev/null && break; sleep 0.1; done
URL="http://$(sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' "$S/enthd.out")"
[ "$URL" != "http://" ] || die "enthd did not start: $(cat "$S/enthd.err")"
E() { "$EBIN/enth" --remote "$URL" --repo alice/proj "$@"; }
tip() { E query show refs/heads/main | sed -n 's/.*"change":"\([0-9a-f]*\)".*/\1/p'; }
note "enthd $URL, trunk at ${ROOT_ID:0:12}"

mkdir -p "$S/repos"
E checkout main "$S/repos/proj" >/dev/null || die "read checkout failed"
PROJ="$S/repos/proj"
# The gate: record the Change gated; land $S/race-id first when it exists.
cat > "$S/gate.sh" <<EOF
#!/bin/sh
sed -n '1s/.*"base":"\([0-9a-f]*\)".*/\1/p' .enth/checkout >> "$S/gated"
if [ -f "$S/race-id" ]; then
  "$EBIN/enth" --remote "$URL" --repo alice/proj land \$(cat "$S/race-id") > /dev/null || exit 9
  rm -f "$S/race-id"
fi
exit 0
EOF
chmod +x "$S/gate.sh"
mkdir -p "$PROJ/.pogo"
printf 'quality_gate = "%s"\n' "$S/gate.sh" > "$PROJ/.pogo/refinery.toml"
gated() { [ -f "$S/gated" ] && wc -l < "$S/gated" | tr -d ' ' || echo 0; }
last_gated() { tail -1 "$S/gated" 2>/dev/null; }

# A git repo for the control: a bare origin and a clone.
git init -q --bare -b main "$S/gitorigin.git" || die "git init --bare failed"
git clone -q "$S/gitorigin.git" "$S/gitwork" 2>/dev/null || die "git clone failed"
( cd "$S/gitwork" && git -c user.email=c@x -c user.name=c commit -q --allow-empty -m init && git push -q origin HEAD:main ) || die "git seed failed"

step "3. pogod; select enth for the read checkout"
mkdir -p "$XDG_CONFIG_HOME/pogo"
cat > "$XDG_CONFIG_HOME/pogo/config.toml" <<EOF
[agents]
coordinator = "mayor"
autostart = false
command = "sleep 600"

[search]
index_roots = ["$S/repos"]
index_interval = "1s"
EOF
mg init >/dev/null 2>&1 || die "mg init failed"
pogo_sandbox_daemon "$BIN_DIR/pogod" /health
export POGO_PORT="$POGO_SANDBOX_PORT"
note "pogod pid=$POGO_SANDBOX_PID log=$POGO_SANDBOX_LOG"
for _ in $(seq 1 30); do pogo project list 2>/dev/null | grep -qF "$PROJ" && break; sleep 1; done
pogo project set-vcs "$PROJ" --vcs=enth --enth-remote="$URL" --enth-repo=alice/proj >/dev/null \
    && ok "     set-vcs recorded $PROJ as enth" || die "set-vcs failed"

# work ITEM FILE CONTENT → a Change on the current trunk carrying the item's trailers
work() {
    local d="$S/work-$1-$RANDOM"
    E checkout main "$d" >/dev/null || return 1
    mkdir -p "$(dirname "$d/$2")"
    printf '%s' "$3" > "$d/$2"
    "$EBIN/enth" change -m "work for $1" --trailer "Pogo-Item=$1" --trailer "Pogo-Worker=w-$1" "$d" \
        | sed -n 's/.*"id":"\([0-9a-f]*\)".*/\1/p'
}
submit() { pogo refinery submit --item="$1" --repo="$PROJ" --author="$1" --json 2>&1 | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p'; }
show() { pogo refinery show "$1" --json 2>/dev/null; }
status() { show "$1" | sed -n 's/^  "status": *"\([a-z]*\)".*/\1/p' | head -1; }
wait_done() { # MR → final status
    local st=""
    for _ in $(seq 1 150); do
        st="$(status "$1")"
        case "$st" in merged|failed|cancelled|lost) echo "$st"; return ;; esac
        sleep 0.2
    done
    echo "${st:-unknown}"
}
flat() { tr -d ' \n'; }

step "4. CLEAN LAND (and the gated tree is the landed tree)"
C1="$(work mg-a x.txt 'x
')"
[ -n "$C1" ] || die "could not make mg-a's Change"
MR="$(submit mg-a)"
ST="$(wait_done "$MR")"
TIP="$(tip)"
[ "$ST" = merged ] && ok "R16  $MR merged through enth" || bad "R16  $MR is $ST: $(show "$MR" | head -60)"
show "$MR" | flat | grep -qF "\"new\":\"$TIP\"" && ok "R17  the MR's enth record names the new trunk tip ${TIP:0:12}" || bad "R17  record: $(show "$MR" | flat | grep -o '"enth":{[^}]*' )"
show "$MR" | flat | grep -qF "\"submitted\":\"$C1\"" && ok "R17  ... and the submitted Change" || bad "R17  record lacks $C1"
show "$MR" | flat | grep -q '"merged_sha":"[0-9a-f]' && bad "§7   merged_sha holds a value on an enth MR" || ok "§7   merged_sha is empty: a Change id is not a git sha"
[ "$(last_gated)" = "$TIP" ] && [ "$(gated)" = 1 ] && ok "R13  the gate ran once, on exactly the Change that landed" || bad "R13  gated $(cat "$S/gated" 2>&1), landed $TIP"

step "5. CONFLICT"
MINE="$(work mg-b a.txt 'a1
MINE
')"
THEIRS="$(work mg-z a.txt 'a1
THEIRS
')"
E land "$THEIRS" >/dev/null || die "landing the competing Change failed"
BEFORE="$(tip)"; GB="$(gated)"
MR="$(submit mg-b)"
ST="$(wait_done "$MR")"
[ "$ST" = failed ] && ok "R11  a conflicting MR fails" || bad "R11  $MR is $ST"
show "$MR" | flat | grep -q '"failure_class":"defect"' && show "$MR" | grep -q 'a.txt' \
    && ok "R11  ... class=defect, naming the conflicting path" || bad "R11  record: $(show "$MR" | head -40)"
[ "$(tip)" = "$BEFORE" ] && [ "$(gated)" = "$GB" ] && ok "R11  the trunk did not move and the gate never ran" || bad "R11  trunk moved or gate ran"

step "6. STALE TIP"
C3="$(work mg-c y.txt 'y
')"
RACE="$(work mg-r r.txt 'r
')"
printf '%s' "$RACE" > "$S/race-id"
GB="$(gated)"
MR="$(submit mg-c)"
ST="$(wait_done "$MR")"
TIP="$(tip)"
[ "$ST" = merged ] && ok "R8   $MR merged after the race" || bad "R8   $MR is $ST: $(show "$MR" | head -60)"
show "$MR" | grep -q 'enth stale_tip' && show "$MR" | flat | grep -q '"class":"contention"' \
    && ok "R8   attempt 1 was refused stale_tip, recorded as contention and retried" || bad "R8   attempts: $(show "$MR" | grep -i -A3 attempts | head -20)"
[ "$(gated)" = $((GB + 2)) ] && [ "$(last_gated)" = "$TIP" ] && ok "§6.4 the re-run re-planned and re-gated: the second gate ran on the landed tip" \
    || bad "§6.4 gated $(($(gated) - GB)) times, last $(last_gated), tip $TIP"
E query show "$TIP" >/dev/null && E cat "$TIP" r.txt >/dev/null 2>&1 && note "the racing land is under the tip"

step "7. RESULT MISMATCH"
C4="$(work mg-d m.txt 'm
')"
printf '%s' "$ROOT_ID" > "$CTL/bogus"; touch "$CTL/mismatch-once"
GB="$(gated)"
MR="$(submit mg-d)"
ST="$(wait_done "$MR")"
[ "$ST" = merged ] && ok "E2   $MR merged after a result_mismatch" || bad "E2   $MR is $ST: $(show "$MR" | head -60)"
show "$MR" | grep -q 'enth result_mismatch' && ok "E2   attempt 1 was refused result_mismatch and retried" || bad "E2   no result_mismatch attempt recorded"
[ "$(gated)" = $((GB + 1)) ] && ok "§6.4 the passed gate was held across the retry (same tip, same N)" || bad "§6.4 gated $(($(gated) - GB)) times"

step "8. KILL POGOD MID-LAND, then replay"
C5="$(work mg-e k.txt 'k
')"
touch "$CTL/kill-after-land"
GB="$(gated)"
OLDPID="$POGO_SANDBOX_PID"
MR="$(submit mg-e)"
for _ in $(seq 1 150); do kill -0 "$OLDPID" 2>/dev/null || break; sleep 0.2; done
if kill -0 "$OLDPID" 2>/dev/null; then
    bad "R9   pogod was never killed mid-land"
else
    ok "R9   pogod (pid $OLDPID) was SIGKILLed after enth landed and before it saw the answer"
fi
grep -q '"type":"landed"' "$CTL/landed-before-kill" 2>/dev/null && ok "R9   ... and enth had landed: $(sed -n 's/.*"entry":\([0-9]*\).*/entry \1/p' "$CTL/landed-before-kill")" \
    || bad "R9   the land before the kill: $(cat "$CTL/landed-before-kill" 2>&1)"
KILLTIP="$(tip)"
wait "$OLDPID" 2>/dev/null
POGO_SANDBOX_PID=""
pogo_sandbox_daemon "$BIN_DIR/pogod" /health
note "restarted pogod pid=$POGO_SANDBOX_PID"
ST="$(wait_done "$MR")"
[ "$ST" = merged ] && ok "R9   the restarted pogod recorded $MR merged" || bad "R9   $MR is $ST after restart: $(show "$MR" | head -60)"
show "$MR" | flat | grep -qF "\"request_id\":\"$MR/1\"" && show "$MR" | flat | grep -qF "\"new\":\"$KILLTIP\"" \
    && ok "R9   ... with the land replayed under its request id $MR/1" || bad "R9   record: $(show "$MR" | flat | grep -o '"enth":{[^}]*')"
[ "$(tip)" = "$KILLTIP" ] && ok "R9   nothing landed twice: the trunk is where the killed land left it" || bad "R9   the trunk moved after the restart"
[ "$(gated)" = $((GB + 1)) ] && ok "R9   the gate was not re-run after the restart" || bad "R9   gated $(($(gated) - GB)) times"
grep -q "recovery found in-flight enth MR $MR landed" "$POGO_SANDBOX_LOG" && ok "R9   recovery says so in pogod's log" || note "no recovery log line (log: $POGO_SANDBOX_LOG)"

step "9. GIT CONTROL: the same daemon, a git branch"
( cd "$S/gitwork" && git checkout -q -b polecat-g1 && echo g > g.txt && git add g.txt \
    && git -c user.email=c@x -c user.name=c commit -q -m "git work" && git push -q origin polecat-g1 ) || die "git branch setup failed"
HEAD="$(git -C "$S/gitwork" rev-parse HEAD)"
GOUT="$(pogo refinery submit polecat-g1 --repo="$S/gitorigin.git" --author=g1 --json 2>&1)"
GMR="$(printf '%s' "$GOUT" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')"
[ -n "$GMR" ] || bad "git  submit: $GOUT"
ST="$(wait_done "$GMR")"
[ "$ST" = merged ] && ok "git  $GMR merged" || bad "git  $GMR is $ST: $(show "$GMR" | head -40)"
show "$GMR" | flat | grep -qF "\"merged_sha\":\"$HEAD\"" && ok "git  merged_sha is the fast-forwarded head ${HEAD:0:12}" || bad "git  merged_sha: $(show "$GMR" | grep merged_sha)"
show "$GMR" | flat | grep -q '"enth":' && bad "git  a git MR carries an enth record" || ok "git  no enth record on the git MR"
[ "$(git -C "$S/gitorigin.git" rev-parse main)" = "$HEAD" ] && ok "git  origin/main is the head" || bad "git  origin/main did not move"

step "done"
echo "  ran $RUN observations; $PASSED passed"
[ "$RUN" = "$PASSED" ] || exit 1
