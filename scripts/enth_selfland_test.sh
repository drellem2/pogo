#!/usr/bin/env bash
# =============================================================================
# enth_selfland_test.sh — enth-vcs §6 as decided (mg-5b904), end to end: an
# enth polecat lands its OWN Change, with the self_land block of the prompt a
# REAL pogod rendered for it, against a REAL enthd; pogod records the land and
# closes the item exactly as a refinery merge closes one.
#
#   1  a sandboxed enthd serving alice/proj; a read checkout registered with a
#      sandboxed pogod and selected as enth; polecats are `sleep`s, so their
#      protocol steps are run here, from the prompt file pogod wrote for them
#   2  CLEAN SELF-LAND: the template's enth change, then its self_land → the
#      trunk moves to the land, the item is DONE with a sidecar naming
#      landed {enth, id} and the verdict, the refinery's history holds the MR
#      (self_landed, merged_sha absent), the coordinator is mailed MERGED, and
#      the polecat is stopped
#   3  STALE TIP: enth refuses the first land stale_tip (made by a wrapper —
#      a plain land has no --expect-tip, so a real one needs a CAS race); the
#      block lands again under the next request id, and the item closes
#   4  LOST ANSWER: the land happens and its answer is lost; the block
#      re-issues the SAME request id, enthd replays, and nothing lands twice
#   5  CONFLICT: a competing land on the same path; the block stops with exit
#      3 and lands nothing; the template's re-make (a fresh checkout, the
#      Pogo-Supersedes trailer) lands, the item closes, and check-stranded
#      does not report the retired Change — with a positive control: a done
#      item's unretired Change IS reported
#   6  THE REFINERY REFUSES enth work: `pogo refinery submit --item` on the
#      enth repo names `pogo land`
#   7  THE GIT CONTROL: the same daemon merges a git branch through a bare
#      origin as it always did — a merged_sha, no enth record
#
# WHICH enth. $POGO_ENTH_BIN_DIR, else the first enthd on PATH. An enth
# without `land --expect-new` SKIPS with exit 0; POGO_ENTH_REQUIRE=1 turns
# the skip into a failure.
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
        echo "enth_selfland_test.sh: FAIL (POGO_ENTH_REQUIRE=1): $1" >&2
        exit 1
    fi
    echo "enth_selfland_test.sh: SKIP — $1"
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
command -v jq >/dev/null || skip "no jq"

step "1. build pogo + pogod, isolate"
pogo_sandbox_create selfland
ITEMS=()
cleanup() {
    [ -n "${ENTHD_PID:-}" ] && kill "$ENTHD_PID" 2>/dev/null
    # self_land keeps its state in /tmp/<item>-land.* (the template's paths).
    for i in ${ITEMS[@]+"${ITEMS[@]}"}; do rm -f "/tmp/$i-land.attempt" "/tmp/$i-land.pending" "/tmp/$i-land.out" "/tmp/$i-land.err" "/tmp/$i-land.gate" "/tmp/$i-land.gerr" "/tmp/$i-verdict.json"; done
    if [ -z "${SELFLAND_KEEP:-}" ]; then
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

# The wrapper enth, first on the workers' PATH: passes every call through,
# except the next REAL land (one carrying --request-id) when a control file
# asks it to refuse that land stale_tip, or to land it and lose the answer.
WBIN="$S/wrap"; CTL="$S/ctl"
mkdir -p "$WBIN" "$CTL"
cat > "$WBIN/enth" <<EOF
#!/bin/sh
REAL="$EBIN/enth"; CTL="$CTL"
EOF
cat >> "$WBIN/enth" <<'EOF'
real=0
case " $* " in *" land "*) case " $* " in *" --dry-run "*) ;; *) case " $* " in *" --request-id "*) real=1 ;; esac ;; esac ;; esac
[ $real = 1 ] && echo "$*" >> "$CTL/lands"
if [ $real = 1 ] && [ -f "$CTL/stale-once" ]; then
  rm -f "$CTL/stale-once"
  echo '{"type":"error","code":"stale_tip","message":"refs/heads/main moved during the land"}' >&2
  exit 3
fi
if [ $real = 1 ] && [ -f "$CTL/lose-answer-once" ]; then
  rm -f "$CTL/lose-answer-once"
  "$REAL" "$@" > "$CTL/lost-answer" 2>&1
  echo "connection reset by peer" >&2
  exit 4
fi
exec "$REAL" "$@"
EOF
chmod +x "$WBIN/enth"
export PATH="$BIN_DIR:$WBIN:$EBIN:$PATH"
note "sandbox: $S"

step "2. enthd serving an enth-written repo"
R="$S/enthd"
UOUT="$("$EBIN/enthd" --root "$R" user add alice --email alice@example.com)" || die "enthd user add failed"
ENTH_TOKEN="$(printf '%s' "$UOUT" | sed -n 's/.*"token":"\(enth_[0-9a-f]*\)".*/\1/p')"
[ -n "$ENTH_TOKEN" ] || die "enthd user add printed no token"
export ENTH_TOKEN
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
E() { ENTH_AUTHOR_NAME=op ENTH_AUTHOR_EMAIL=op@example.com ENTH_COMMITTER_NAME=op ENTH_COMMITTER_EMAIL=op@example.com \
      "$EBIN/enth" --remote "$URL" --repo alice/proj "$@"; }
tip() { E query show refs/heads/main | sed -n 's/.*"change":"\([0-9a-f]*\)".*/\1/p'; }
entries() { E query log 2>/dev/null | grep -c . ; }
note "enthd $URL, trunk at ${ROOT_ID:0:12}"
mkdir -p "$S/repos"
E checkout main "$S/repos/proj" >/dev/null || die "read checkout failed"
PROJ="$S/repos/proj"

# A git repo for the control: a bare origin and a clone.
git init -q --bare -b main "$S/gitorigin.git" || die "git init --bare failed"
git clone -q "$S/gitorigin.git" "$S/gitwork" 2>/dev/null || die "git clone failed"
( cd "$S/gitwork" && git -c user.email=c@x -c user.name=c commit -q --allow-empty -m init && git push -q origin HEAD:main ) || die "git seed failed"

step "3. pogod; select enth for the read checkout"
cat > "$S/agent.sh" <<EOF
#!/bin/sh
# The sandbox polecat: record the environment pogod gave it, then idle.
env > "$S/env.\$(basename "\$PWD")"
exec sleep 600
EOF
chmod +x "$S/agent.sh"
mkdir -p "$XDG_CONFIG_HOME/pogo"
cat > "$XDG_CONFIG_HOME/pogo/config.toml" <<EOF
[agents]
coordinator = "mayor"
autostart = false
command = "$S/agent.sh"

[search]
index_roots = ["$S/repos"]
index_interval = "1s"
EOF
mg init >/dev/null 2>&1 || die "mg init failed"
pogo_sandbox_daemon "$BIN_DIR/pogod" /health
export POGO_PORT="$POGO_SANDBOX_PORT"
note "pogod pid=$POGO_SANDBOX_PID log=$POGO_SANDBOX_LOG"
# The CLI reaches the SANDBOX daemon, not the live one, or nothing below runs.
pogo server status 2>&1 | grep -q "pid=$POGO_SANDBOX_PID" || die "the pogo CLI does not reach the sandbox pogod (pid $POGO_SANDBOX_PID): $(pogo server status 2>&1)"
for _ in $(seq 1 30); do pogo project list 2>/dev/null | grep -qF "$PROJ" && break; sleep 1; done
pogo project set-vcs "$PROJ" --vcs=enth --enth-remote="$URL" --enth-repo=alice/proj >/dev/null \
    && ok "     set-vcs recorded $PROJ as enth" || die "set-vcs failed"
mg mail send mayor --from=test --create --subject=init --body=init >/dev/null 2>&1 || true

# new_item TITLE → a work item id
new_item() { mg new --title="$1" --repo="$PROJ" --body="selfland probe" 2>&1 | grep -o 'mg-[0-9a-f]*' | head -1; }
track() { ITEMS+=("$1"); }
# spawn NAME ITEM [--branch=main] → a sleeping polecat on the enth repo
spawn() {
    local name="$1" item="$2"; shift 2
    pogo agent spawn-polecat "$name" --id="$item" --template=polecat --task="selfland probe" --body="probe" --repo="$PROJ" "$@" 2>&1
}
wait_file() { for _ in $(seq 1 100); do [ -s "$1" ] && return 0; sleep 0.1; done; return 1; }
prompt_of() { pogo agent list --json 2>/dev/null | jq -r --arg n "$1" '.[] | select(.name == $n) | .prompt_file' | head -1; }
# The worker's environment, as pogod gave it, pointed at THIS daemon.
as_worker() { # NAME DIR CMD...
    local name="$1" dir="$2"; shift 2
    local envv=() line
    while IFS= read -r line; do envv+=("$line"); done < "$S/env.$name"
    ( cd "$dir" && env -i ${envv[@]+"${envv[@]}"} PATH="$PATH" POGO_PORT="$POGO_PORT" HOME="$HOME" \
        POGO_HOME="$POGO_HOME" MG_ROOT="$MG_ROOT" XDG_CONFIG_HOME="$XDG_CONFIG_HOME" TMPDIR="$TMPDIR" "$@" )
}
# lift BLOCK-NAME PROMPT → the shell the prompt renders, dedented
lift_self_land() { awk '/^   self_land\(\) \{$/{p=1} p{print} p&&/^   \}$/{exit}' "$1" | sed 's/^   //'; }
lift_change()    { grep -m1 '^   enth change -m ' "$1" | sed 's/^   //'; }
lift_remake()    { awk '/^     OLD=\$\(pogo presubmit/{p=1} p{print} p&&/^     self_land$/{exit}' "$1" | sed 's/^     //'; }
status_of() { mg show "$1" 2>/dev/null | sed -n 's/^Status: *//p' | head -1; }
sidecar() { cat "$MG_ROOT/work/done/$1.result.json" 2>/dev/null; }
wait_done() { for _ in $(seq 1 100); do [ "$(status_of "$1")" = done ] && return 0; sleep 0.1; done; return 1; }
gone() { for _ in $(seq 1 100); do pogo agent list --json 2>/dev/null | jq -e --arg n "$1" 'any(.[]; .name == $n)' >/dev/null || return 0; sleep 0.1; done; return 1; }

# setup ITEM NAME [spawn flags] → a polecat, its checkout D, its prompt P, and
# its two lifted blocks in $S/<name>.self_land / .change / .remake
setup() {
    local item="$1" name="$2"; shift 2
    OUT="$(spawn "$name" "$item" "$@")" || { bad "spawn $name: $OUT"; return 1; }
    D="$POGO_HOME/polecats/$name"
    wait_file "$S/env.$name" || { bad "polecat $name never started"; return 1; }
    PR="$(prompt_of "$name")"
    [ -s "$PR" ] || { bad "no prompt file for $name ($PR)"; return 1; }
    cp "$PR" "$S/$name.prompt"
    lift_self_land "$PR" > "$S/$name.self_land"
    lift_change "$PR" > "$S/$name.change"
    lift_remake "$PR" > "$S/$name.remake"
    cat > "$TMPDIR/$item-verdict.json" <<EOF
{"verdict":"pass","summary":"selfland probe $name"}
EOF
    cp "$TMPDIR/$item-verdict.json" "/tmp/$item-verdict.json"
}
self_land_as() { # NAME DIR → runs the lifted self_land; prints its output; returns its status
    as_worker "$1" "$2" bash -c ". '$S/$1.self_land'; self_land"
}

step "4. CLEAN SELF-LAND, from the rendered template"
A="$(new_item "clean self-land")"; [ -n "$A" ] || die "mg new failed"; track "$A"
setup "$A" sa || die "setup failed"
grep -qF 'enth land "${lreq[@]}" "${stack[@]}" --request-id "$item/$n"' "$S/sa.self_land" && grep -q "pogo land --item=\"\$item\" --repo=$PROJ" "$S/sa.self_land" \
    && ok "P5   the rendered prompt carries self_land (enth land + pogo land), filled in for $A" || bad "P5   self_land as rendered: $(cat "$S/sa.self_land")"
grep -q 'pogo refinery submit' "$S/sa.prompt" && bad "P5   the enth prompt still submits to the refinery" || ok "P5   ... and no refinery submit"
echo "x" > "$D/x.txt"
as_worker sa "$D" bash -c "$(cat "$S/sa.change")" >/dev/null 2>&1 && ok "P3   the template's enth change line made the Change" || bad "P3   enth change failed"
BEFORE_E="$(entries)"
OUT="$(self_land_as sa "$D" 2>&1)"; RC=$?
TIP="$(tip)"
[ "$RC" = 0 ] && printf '%s' "$OUT" | grep -q "LANDED and recorded as MR" && ok "§6   self_land landed and recorded (rc 0)" || bad "§6   self_land rc $RC: $OUT"
[ "$(entries)" = $((BEFORE_E + 1)) ] && ok "§6   one trunk entry, tip ${TIP:0:12}" || bad "§6   entries $BEFORE_E → $(entries)"
wait_done "$A" && ok "§6   $A is DONE" || bad "§6   $A is $(status_of "$A")"
SC="$(sidecar "$A")"
printf '%s' "$SC" | jq -e --arg t "$TIP" '.landed.vcs == "enth" and .landed.id == $t' >/dev/null \
    && ok "§7   the sidecar names landed {enth, ${TIP:0:12}}" || bad "§7   sidecar: $SC"
printf '%s' "$SC" | jq -e '.verdict.summary == "selfland probe sa"' >/dev/null && ok "§6   ... and carries the verdict" || bad "§6   sidecar verdict: $SC"
printf '%s' "$SC" | jq -e 'has("merged_sha") | not' >/dev/null && ok "§7   ... with no merged_sha" || bad "§7   sidecar merged_sha: $SC"
MRID="$(printf '%s' "$OUT" | sed -n 's/.*recorded as MR \([^: ]*\).*/\1/p' | head -1)"
SHOW="$(pogo refinery show "$MRID" --json 2>/dev/null)"
printf '%s' "$SHOW" | jq -e --arg t "$TIP" '.status == "merged" and .self_landed == true and .enth.new == $t and .enth.request_id != null and (has("merged_sha")|not)' >/dev/null \
    && ok "§7   pogo refinery show $MRID: merged, self_landed, enth.new = tip, request id $(printf '%s' "$SHOW" | jq -r .enth.request_id)" || bad "§7   show: $SHOW"
gone sa && ok "§6   the polecat was stopped after the land was recorded" || bad "§6   polecat sa is still running"
for _ in $(seq 1 50); do mg mail list mayor --from=refinery 2>/dev/null | grep -q "MERGED: $MRID" && break; sleep 0.1; done
mg mail list mayor --from=refinery 2>/dev/null | grep -q "MERGED: $MRID" && ok "§6   the coordinator was mailed MERGED: $MRID" || bad "§6   no MERGED mail for $MRID: $(mg mail list mayor 2>&1 | tail -5)"
OUT2="$(as_worker sa "$PROJ" pogo land --item="$A" --repo="$PROJ" --landed-file="/tmp/$A-land.out" 2>&1)"
printf '%s' "$OUT2" | grep -q "already recorded" && ok "§6   re-running pogo land answers the first MR (already recorded)" || bad "§6   second pogo land: $OUT2"

step "5. STALE TIP is landed again under the next request id"
B="$(new_item "stale tip")"; track "$B"
setup "$B" sb --branch=main || die "setup failed"
grep -qF 'enth land --ref refs/heads/main "${lreq[@]}" "${stack[@]}"' "$S/sb.self_land" && ok "P5   a --branch=main render lands with --ref refs/heads/main" || bad "P5   --branch render: $(grep 'enth land --ref' "$S/sb.self_land")"
echo "y" > "$D/y.txt"
as_worker sb "$D" bash -c "$(cat "$S/sb.change")" >/dev/null 2>&1 || bad "enth change failed"
touch "$CTL/stale-once"
OUT="$(self_land_as sb "$D" 2>&1)"; RC=$?
printf '%s' "$OUT" | grep -q "stale_tip: the trunk moved" && ok "§6   the first land was refused stale_tip" || bad "§6   no stale_tip seen: $OUT"
[ "$RC" = 0 ] && printf '%s' "$OUT" | grep -q "LANDED as $B/2" && ok "§6   ... and it landed again as $B/2" || bad "§6   rc $RC: $OUT"
wait_done "$B" && sidecar "$B" | jq -e --arg t "$(tip)" '.landed.id == $t' >/dev/null && ok "§6   $B is DONE, landed at the tip" || bad "§6   $B is $(status_of "$B"): $(sidecar "$B")"

step "6. LOST ANSWER: the SAME request id, nothing landed twice"
C="$(new_item "lost answer")"; track "$C"
setup "$C" sc || die "setup failed"
echo "k" > "$D/k.txt"
as_worker sc "$D" bash -c "$(cat "$S/sc.change")" >/dev/null 2>&1 || bad "enth change failed"
touch "$CTL/lose-answer-once"
BEFORE_E="$(entries)"
OUT="$(self_land_as sc "$D" 2>&1)"; RC=$?
grep -q '"type":"landed"' "$CTL/lost-answer" 2>/dev/null && ok "R9   the first land happened, and its answer was lost" || bad "R9   the lost land: $(cat "$CTL/lost-answer" 2>&1)"
printf '%s' "$OUT" | grep -q "re-issuing the SAME request $C/1" && ok "R9   self_land re-issued $C/1, not a new id" || bad "R9   $OUT"
[ "$RC" = 0 ] && [ "$(entries)" = $((BEFORE_E + 1)) ] && ok "R9   enthd replayed it: one trunk entry, recorded" || bad "R9   rc $RC, entries $BEFORE_E → $(entries)"
wait_done "$C" && ok "R9   $C is DONE" || bad "R9   $C is $(status_of "$C")"

step "7. CONFLICT: stop, re-make on the new trunk, land"
Dn="$(new_item "conflict")"; track "$Dn"
setup "$Dn" sd || die "setup failed"
DD="$D"
printf 'a1\nMINE\n' > "$DD/a.txt"
as_worker sd "$DD" bash -c "$(cat "$S/sd.change")" >/dev/null 2>&1 || bad "enth change failed"
OLD="$(as_worker sd "$DD" pogo presubmit --item="$Dn" --print-stack 2>/dev/null)"
W="$S/theirs"; E checkout main "$W" >/dev/null; printf 'a1\nTHEIRS\n' > "$W/a.txt"
THEIRS="$(E change -m "theirs" "$W" | sed -n 's/.*"id":"\([0-9a-f]*\)".*/\1/p')"
E land "$THEIRS" >/dev/null || die "landing the competing Change failed"
BEFORE="$(tip)"
OUT="$(self_land_as sd "$DD" 2>&1)"; RC=$?
[ "$RC" = 3 ] && printf '%s' "$OUT" | grep -q '"type":"conflict"' && printf '%s' "$OUT" | grep -q "a.txt" \
    && ok "R11  self_land stopped with exit 3, the conflict line naming a.txt" || bad "R11  rc $RC: $OUT"
[ "$(tip)" = "$BEFORE" ] && [ "$(status_of "$Dn")" = claimed ] && ok "R11  nothing landed and $Dn is still claimed" || bad "R11  tip moved or $Dn is $(status_of "$Dn")"
grep -q 'Pogo-Supersedes="\$OLD"' "$S/sd.remake" && ok "§6   the rendered re-make names the Change it replaces" || bad "§6   re-make block: $(cat "$S/sd.remake")"
# The re-make as rendered; the worker's hand merge and its ./build.sh are the two lines a script cannot do.
REMAKE_SH="$(sed -e 's|^\./build\.sh$|true  # ./build.sh: the worker'"'"'s test|' \
    -e 's|^enth checkout .* && cd "\$REMAKE"$|&; printf "a1\\nTHEIRS\\nMINE\\n" > a.txt|' "$S/sd.remake")"
OUT="$(as_worker sd "$DD" bash -c ". '$S/sd.self_land'; $REMAKE_SH" 2>&1)"; RC=$?
[ "$RC" = 0 ] && printf '%s' "$OUT" | grep -q "LANDED and recorded" && ok "§6   the re-made Change landed and was recorded" || bad "§6   re-make rc $RC: $OUT"
E query cat a.txt --rev refs/heads/main 2>/dev/null | grep -q MINE && ok "§6   the trunk has both edits" || bad "§6   a.txt on the trunk: $(E query cat a.txt --rev refs/heads/main 2>&1)"
wait_done "$Dn" && ok "§6   $Dn is DONE" || bad "§6   $Dn is $(status_of "$Dn")"
# The retired Change is still unlanded in enth's own listing …
E changes --unlanded --trailer "Pogo-Item=$Dn" | grep -q "$OLD" && note "enth itself still lists ${OLD:0:12} as unlanded (it cannot withdraw a Change)"
# … and a positive control: a done item whose Change was never landed or retired IS stranded.
Ex="$(new_item "stranded control")"; track "$Ex"
W="$S/stray"; E checkout main "$W" >/dev/null; echo s > "$W/s.txt"
E change -m "stray" --trailer "Pogo-Item=$Ex" "$W" >/dev/null
mg claim "$Ex" >/dev/null 2>&1; mg done "$Ex" --result='{"verdict":"control"}' >/dev/null 2>&1
CS="$(pogo check-stranded --json 2>&1)"
printf '%s' "$CS" | grep -q "$Ex" && ok "S2   (control) check-stranded reports a done item's unlanded Change" || bad "S2   control not reported: $CS"
printf '%s' "$CS" | grep -q "$Dn" && bad "S2   check-stranded reports $Dn's retired Change: $CS" || ok "S2   ... and does not report $Dn's superseded Change"

step "8. THE REFINERY REFUSES enth work"
Fx="$(new_item "refinery refusal")"; track "$Fx"
OUT="$(pogo refinery submit --item="$Fx" --repo="$PROJ" --author="$Fx" 2>&1)"; RC=$?
[ "$RC" != 0 ] && printf '%s' "$OUT" | grep -q "no merge queue" && printf '%s' "$OUT" | grep -q "pogo land --item=$Fx" \
    && ok "R1   pogo refinery submit --item on the enth repo is refused, naming pogo land" || bad "R1   submit: rc $RC $OUT"

step "9. GIT CONTROL: the same daemon, a git branch"
( cd "$S/gitwork" && git checkout -q -b polecat-g1 && echo g > g.txt && git add g.txt \
    && git -c user.email=c@x -c user.name=c commit -q -m "git work" && git push -q origin polecat-g1 ) || die "git branch setup failed"
HEAD="$(git -C "$S/gitwork" rev-parse HEAD)"
GOUT="$(pogo refinery submit polecat-g1 --repo="$S/gitorigin.git" --author=g1 --json 2>&1)"
GMR="$(printf '%s' "$GOUT" | sed -n 's/.*"id": *"\([^"]*\)".*/\1/p')"
[ -n "$GMR" ] || bad "git  submit: $GOUT"
for _ in $(seq 1 150); do st="$(pogo refinery show "$GMR" --json 2>/dev/null | jq -r .status)"; case "$st" in merged|failed) break ;; esac; sleep 0.2; done
GS="$(pogo refinery show "$GMR" --json 2>/dev/null)"
printf '%s' "$GS" | jq -e --arg h "$HEAD" '.status == "merged" and .merged_sha == $h and (has("enth")|not) and (has("self_landed")|not)' >/dev/null \
    && ok "git  $GMR merged through the refinery: merged_sha ${HEAD:0:12}, no enth record" || bad "git  $GS"
[ "$(git -C "$S/gitorigin.git" rev-parse main)" = "$HEAD" ] && ok "git  origin/main is the head" || bad "git  origin/main did not move"

step "done"
echo "  ran $RUN observations; $PASSED passed"
[ "$RUN" = "$PASSED" ] || exit 1
