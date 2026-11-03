#!/usr/bin/env bash
# =============================================================================
# enth_workspaces_test.sh — enth-vcs slice 5 (mg-cc5cd), end to end: a REAL
# pogod spawns a polecat onto an enth-native repo served by a REAL enthd, the
# polecat's tree is edited, judged dirty, and removed (docs/design/enth-vcs.md
# §4, §9, §16 row 5).
#
#   1  a sandboxed enthd serving alice/proj, an ENTH-WRITTEN repo (enthd repo
#      create + an operator-landed root: enth epic item F's shape), and a read
#      checkout of it under an index root
#   2  a sandboxed pogod; discovery registers the read checkout by its
#      .enth/checkout marker (X1); `pogo project set-vcs --vcs=enth` selects
#      the backend
#   3  SPAWN: `pogo agent spawn-polecat --repo=<enth project>` makes an enth
#      checkout at $POGO_HOME/polecats/<name> — whole repo, .enth/checkout
#      origin = the enthd, NO .git, pogod's daemon files in .enth/info/exclude,
#      pogod's workspace record — and the worker's environment carries
#      ENTH_REMOTE, ENTH_REPO and the token
#   4  DIRTY: `pogo worktree-dirty` is empty on the fresh tree (pogod's own
#      .claude/settings.local.json is excluded, not carved out), and names an
#      edit in porcelain shape
#   5  REMOVE: `pogo agent stop` PRESERVES the dirty tree and REMOVES a clean
#      one; a tree whose edits were made into a Change is clean, and the
#      Change is on enthd after the tree is gone
#   6  THE GIT CONTROL: the same daemon spawns a polecat on a git repo and it
#      is the git worktree it always was — a polecat-<name> branch, a .git
#      pointer, no .enth, no ENTH_ variables — and stopping it reaps it.
#
# WHICH enth. $POGO_ENTH_BIN_DIR, else the first enthd on PATH. An enth that
# predates enth #9 (no `status` verb) SKIPS with exit 0 and says so;
# POGO_ENTH_REQUIRE=1 turns the skip into a failure (use it when the point of
# the run is the proof). Build #9 from drellem2/enth with
# `cargo build --release` and point POGO_ENTH_BIN_DIR at target/release.
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
        echo "enth_workspaces_test.sh: FAIL (POGO_ENTH_REQUIRE=1): $1" >&2
        exit 1
    fi
    echo "enth_workspaces_test.sh: SKIP — $1"
    exit 0
}

# --- which enth --------------------------------------------------------------
EBIN="${POGO_ENTH_BIN_DIR:-}"
if [ -z "$EBIN" ]; then
    EBIN="$(command -v enthd 2>/dev/null)" && EBIN="$(dirname "$EBIN")" || skip "no enthd on PATH and POGO_ENTH_BIN_DIR unset"
fi
for b in enth enthd enth-store enth-change enth-land; do
    [ -x "$EBIN/$b" ] || skip "$EBIN has no $b"
done
"$EBIN/enth" --help 2>&1 | grep -q 'status DIR' || skip "$EBIN/enth predates enth #9 (no status verb)"

# --- sandbox, and a pogo/pogod built from THIS checkout ----------------------
step "1. build pogo + pogod, isolate"
pogo_sandbox_create enthws
AGENTS=()
cleanup() {
    local a
    for a in "${AGENTS[@]:-}"; do
        [ -n "$a" ] && "$BIN_DIR/pogo" agent stop "$a" >/dev/null 2>&1
    done
    [ -n "${ENTHD_PID:-}" ] && kill "$ENTHD_PID" 2>/dev/null
    if [ -z "${ENTHWS_KEEP:-}" ]; then
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
export PATH="$BIN_DIR:$EBIN:$PATH"
S="$POGO_SANDBOX_DIR"
note "sandbox: $S"
note "enth: $EBIN"

# --- 1. enthd and an enth-written repo ----------------------------------------
step "2. enthd serving an enth-written repo"
R="$S/enthd"
UOUT="$("$EBIN/enthd" --root "$R" user add alice --email alice@example.com)" || die "enthd user add failed"
ENTH_TOKEN="$(printf '%s' "$UOUT" | sed -n 's/.*"token":"\(enth_[0-9a-f]*\)".*/\1/p')"
[ -n "$ENTH_TOKEN" ] || die "enthd user add printed no token"
export ENTH_TOKEN
"$EBIN/enthd" --root "$R" repo create alice/proj --owner alice >/dev/null || die "enthd repo create failed"
P="$R/repos/alice~proj"
mkdir -p "$S/w"
printf 'a1\na2\n' > "$S/w/a"
printf 'b1\n' > "$S/w/b"
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
note "enthd $URL, trunk at ${ROOT_ID:0:12}"

# The read checkout pogo keeps as the project path (§2), under an index root.
mkdir -p "$S/repos"
"$EBIN/enth" --remote "$URL" --repo alice/proj checkout main "$S/repos/proj" >/dev/null || die "read checkout failed"
PROJ="$S/repos/proj"
# And a git repo beside it, for the control.
git init -q -b main "$S/repos/gitproj" || die "git init failed"
( cd "$S/repos/gitproj" && git -c user.email=c@x -c user.name=c commit -q --allow-empty -m init ) || die "git commit failed"

# --- 2. pogod -----------------------------------------------------------------
step "3. pogod discovers both repos; select enth for one"
cat > "$S/agent.sh" <<EOF
#!/bin/sh
# The sandbox agent: record the environment pogod gave it, then idle.
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

registered() { pogo project list 2>/dev/null | grep -qF "$1"; }
for _ in $(seq 1 30); do registered "$PROJ" && registered "$S/repos/gitproj" && break; sleep 1; done
registered "$PROJ" && ok "X1   discovery registered the enth checkout by its .enth/checkout marker" \
    || bad "X1   the enth read checkout was not registered"
registered "$S/repos/gitproj" && ok "     (control) discovery registered the git repo" || bad "the git repo was not registered"

pogo project set-vcs "$PROJ" --vcs=enth --enth-remote="$URL" --enth-repo=alice/proj >/dev/null \
    && ok "     set-vcs recorded $PROJ as enth" || bad "set-vcs failed"

spawn() { # NAME REPO
    AGENTS+=("$1")
    pogo agent spawn-polecat "$1" --template=polecat --task="enth workspace probe" \
        --body="probe" --repo="$2" --branch=main 2>&1
}
wait_env() { for _ in $(seq 1 50); do [ -s "$S/env.$1" ] && return 0; sleep 0.1; done; return 1; }
wait_gone() { for _ in $(seq 1 50); do [ -e "$1" ] || return 0; sleep 0.2; done; return 1; }

# --- 3. spawn ----------------------------------------------------------------
step "4. SPAWN onto the enth repo"
OUT="$(spawn e1 "$PROJ")"; RC=$?
D1="$POGO_HOME/polecats/e1"
[ "$RC" = 0 ] && ok "spawn-polecat on the enth repo succeeded" || { bad "spawn failed (rc $RC): $OUT"; tail -30 "$POGO_SANDBOX_LOG" >&2; }
[ -f "$D1/.enth/checkout" ] && ok "W2   $D1 is an enth checkout (.enth/checkout)" || bad "W2   no .enth/checkout in $D1"
[ ! -e "$D1/.git" ] && ok "W2   ... with no .git" || bad "W2   the enth workspace has a .git"
[ -f "$D1/a.txt" ] && [ -f "$D1/src/b.txt" ] && ok "W3   whole-repo checkout (a.txt and src/b.txt)" || bad "W3   the checkout is not the whole repo"
HDR="$(head -1 "$D1/.enth/checkout" 2>/dev/null)"
printf '%s' "$HDR" | grep -qF "\"remote\":\"$URL\",\"repo\":\"alice/proj\"" && ok "W4   origin is the enthd ($URL, alice/proj)" || bad "W4   origin: $HDR"
printf '%s' "$HDR" | grep -qF "\"base\":\"$ROOT_ID\"" && ok "W4   based on the trunk tip ${ROOT_ID:0:12}" || bad "W4   base: $HDR"
printf '%s' "$HDR" | grep -qF '"paths":[]' && ok "     selection is the whole tree" || bad "     selection: $HDR"
grep -qxF '/.claude/settings.local.json' "$D1/.enth/info/exclude" 2>/dev/null \
    && grep -qxF '**/.claude/pogo-mailhook.stamp' "$D1/.enth/info/exclude" \
    && ok "W8   pogod's daemon files are in .enth/info/exclude" || bad "W8   exclude: $(cat "$D1/.enth/info/exclude" 2>&1)"
grep -qF "\"repo\": \"$PROJ" "$D1/.enth/pogo-workspace.json" 2>/dev/null \
    && ok "W9   pogod's workspace record names the project" || bad "W9   record: $(cat "$D1/.enth/pogo-workspace.json" 2>&1)"
if wait_env e1; then
    grep -qxF "ENTH_REMOTE=$URL" "$S/env.e1" && ok "ENV  ENTH_REMOTE is the enthd" || bad "ENV  ENTH_REMOTE missing/wrong"
    grep -qxF "ENTH_REPO=alice/proj" "$S/env.e1" && ok "ENV  ENTH_REPO is alice/proj" || bad "ENV  ENTH_REPO missing/wrong"
    # Presence and equality only: the value is never printed.
    grep -qxF "ENTH_TOKEN=$ENTH_TOKEN" "$S/env.e1" && ok "ENV  the token pogod holds reached the worker" || bad "ENV  ENTH_TOKEN did not reach the worker"
else
    bad "ENV  the agent never recorded its environment"
fi

# --- 4. dirty ----------------------------------------------------------------
step "5. DIRTY"
[ -f "$D1/.claude/settings.local.json" ] && note "pogod wrote $D1/.claude/settings.local.json (the hook settings)" \
    || note "no hook settings file was written (provider without hooks) — writing one to stand in"
mkdir -p "$D1/.claude"; [ -f "$D1/.claude/settings.local.json" ] || echo '{}' > "$D1/.claude/settings.local.json"
DOUT="$(pogo worktree-dirty "$D1" 2>&1)"; DRC=$?
[ "$DRC" = 0 ] && [ -z "$DOUT" ] && ok "P1   a fresh enth workspace is clean, pogod's own files included" || bad "P1   fresh tree: rc $DRC, $DOUT"
printf 'a1\nEDITED\n' > "$D1/a.txt"
echo new > "$D1/src/new.txt"
DOUT="$(cd "$D1/src" && pogo worktree-dirty 2>&1)"; DRC=$?
[ "$DRC" = 0 ] && [ "$DOUT" = "$(printf ' M a.txt\n?? src/new.txt')" ] \
    && ok "P1   worktree-dirty (run from a subdirectory) names the edit and the new file" || bad "P1   dirty tree: rc $DRC, [$DOUT]"
DJSON="$(pogo worktree-dirty --json "$D1" 2>&1)"
printf '%s' "$DJSON" | grep -qE '"dirty": *true' && ok "P1   --json says dirty" || bad "P1   --json: $DJSON"

# --- 5. remove ---------------------------------------------------------------
step "6. REMOVE through pogo agent stop"
pogo agent stop e1 >/dev/null 2>&1
sleep 2
if [ -f "$D1/a.txt" ] && grep -q EDITED "$D1/a.txt"; then
    ok "C6   stopping a polecat with uncommitted enth edits PRESERVED its tree"
else
    bad "C6   the dirty enth workspace was removed — uncommitted work lost"
fi
grep -q 'PRESERVED worktree .*polecats/e1' "$POGO_SANDBOX_LOG" && ok "C6   ... and said so" || note "no PRESERVED log line found"

OUT="$(spawn e2 "$PROJ")" || bad "second spawn failed: $OUT"
D2="$POGO_HOME/polecats/e2"
wait_env e2 || true
[ -f "$D2/.enth/checkout" ] && ok "     second enth workspace created" || bad "     no second workspace"
pogo agent stop e2 >/dev/null 2>&1
wait_gone "$D2" && ok "C6   stopping a polecat with a CLEAN enth workspace removed it" || bad "C6   the clean enth workspace was not removed"

OUT="$(spawn e3 "$PROJ")" || bad "third spawn failed: $OUT"
D3="$POGO_HOME/polecats/e3"
wait_env e3 || true
echo "work of e3" > "$D3/e3.txt"
CH="$(ENTH_AUTHOR_NAME=e3 ENTH_AUTHOR_EMAIL=e3@example.com ENTH_COMMITTER_NAME=e3 ENTH_COMMITTER_EMAIL=e3@example.com \
    "$EBIN/enth" change -m "probe (mg-x)" --trailer Pogo-Item=mg-x --trailer Pogo-Worker=e3 "$D3" 2>&1)"
CID="$(printf '%s' "$CH" | sed -n 's/.*"id":"\([0-9a-f]*\)".*/\1/p')"
[ -n "$CID" ] && ok "P4   the worker's enth change went to enthd (${CID:0:12})" || bad "P4   enth change: $CH"
DOUT="$(pogo worktree-dirty "$D3" 2>&1)"
[ -z "$DOUT" ] && ok "P1   committed edits leave the tree clean" || bad "P1   after the change: $DOUT"
pogo agent stop e3 >/dev/null 2>&1
wait_gone "$D3" && ok "C6   the committed workspace was removed" || bad "C6   the committed workspace was not removed"
"$EBIN/enth" --remote "$URL" --repo alice/proj changes --unlanded --trailer Pogo-Item=mg-x 2>&1 | grep -qF "\"id\":\"$CID\"" \
    && ok "S5   the Change outlives its workspace: enthd lists it unlanded" || bad "S5   the Change is not on enthd"

# --- 6. the git control ------------------------------------------------------
step "7. GIT CONTROL: the same daemon, a git repo"
OUT="$(spawn g1 "$S/repos/gitproj")"; RC=$?
G1="$POGO_HOME/polecats/g1"
[ "$RC" = 0 ] && ok "spawn on the git repo succeeded" || bad "git spawn failed: $OUT"
[ -f "$G1/.git" ] && ok "git  the workspace is a linked git worktree (.git pointer)" || bad "git  no .git pointer in $G1"
[ ! -e "$G1/.enth" ] && ok "git  ... with no .enth" || bad "git  the git workspace has .enth"
git -C "$S/repos/gitproj" rev-parse -q --verify refs/heads/polecat-g1 >/dev/null && ok "git  branch polecat-g1 exists" || bad "git  no polecat-g1 branch"
if wait_env g1; then
    grep -qE '^ENTH_(REMOTE|REPO|TRUNK)=' "$S/env.g1" && bad "git  the git worker got enth variables: $(grep -oE '^ENTH_(REMOTE|REPO|TRUNK)' "$S/env.g1" | tr '\n' ' ')" \
        || ok "git  no ENTH_REMOTE/ENTH_REPO/ENTH_TRUNK in the git worker's environment"
else
    bad "git  the git agent never recorded its environment"
fi
# ENTH_TOKEN is in pogod's own environment (this script exported it), and a git
# worker inherits pogod's environment as before: the control is that pogod
# ADDS nothing, not that it scrubs what it inherited.
[ -z "$(pogo worktree-dirty "$G1" 2>&1)" ] && ok "git  worktree-dirty is clean on the fresh git worktree" || bad "git  fresh git worktree dirty"
pogo agent stop g1 >/dev/null 2>&1
wait_gone "$G1" && ok "git  stopping it reaped the git worktree, as before" || bad "git  the git worktree was not reaped"

step "done"
echo "  ran $RUN observations; $PASSED passed"
[ "$RUN" = "$PASSED" ] || exit 1
