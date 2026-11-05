#!/usr/bin/env bash
# =============================================================================
# enth_new_repo_scenario_test.sh — GH #284 (mg-d8dc9): on a FRESH pogo install,
# with only the prompts pogo ships, "make me a new enth repo called x" works
# end to end: the repo is created, registered with pogo, and a worker lands a
# first Change into it.
#
#   1  a fresh sandbox: pogo + pogod built from this tree, an empty POGO_HOME,
#      HOME, mail store and config; no prompt or memory but what pogod installs
#   2  an enthd with one user; the user signs in with `enth login`, reading a
#      token from stdin (the human-only step: see HUMAN STEPS below)
#   3  the coordinator prompt pogod installed has the enth section, and its
#      "Making a new enth repo" block is lifted from that file and run, with
#      only OWNER / URL / the path filled in
#   4  pogo doctor --check: the enth rows pass
#   5  a work item on x, a polecat spawned on the shipped template; the
#      template's `enth change` line and `self_land` block (lifted from the
#      prompt pogod rendered for it) land the first Change on an EMPTY repo,
#      and pogod closes the item
#   6  THE AUTO-REGISTRATION TRAP: an enth checkout found by the indexer is
#      recorded as git; doctor names it and the set-vcs line it prints fixes it
#
# HUMAN STEPS (not scriptable on a real enth.app; stood in for here):
#   - installing the enth CLI (here: the enth found on PATH / $POGO_ENTH_BIN_DIR)
#   - `enth login https://enth.app`: a device login approved in a browser
#     (here: a token minted by `enthd user add`, read by `enth login` from stdin)
#   - the coordinator agent itself: polecats here are `sleep`s, so the
#     coordinator's and worker's commands are run by this script, lifted
#     verbatim from the prompts pogod installed and rendered.
#
# WHICH enth. $POGO_ENTH_BIN_DIR, else the first enthd on PATH. An enth with
# no `repo create` or `login` SKIPS with exit 0; POGO_ENTH_REQUIRE=1 turns the
# skip into a failure.
#
# WHICH GATE (mg-7f8e4). The land in 5 is always the GATED one: this script
# builds enth-land-gate from this tree into the sandbox bin, and hides any
# enth-land-gate installed on the host (enth's install.sh puts one next to
# enthd, in ~/.local/bin). The worker's first Change declares a gate in
# .pogo/refinery.toml, as a repo must for a gated land to have something to
# run. Before this, the box's gate decided the path: absent, the land was
# ungated; installed, it fell back to ./build.sh, which x has none of, and
# section 5 failed on every branch.
#
# Exit: 0 all observations made (or skipped); 1 an observation failed; 99 the
# sandbox could not be established.
# =============================================================================

set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/pogo-sandbox"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/enth-hermetic-path"

RUN=0
PASSED=0
step() { echo; echo "===== $* ====="; }
note() { echo "  - $*"; }
ok()   { RUN=$((RUN+1)); PASSED=$((PASSED+1)); echo "  PASS  $*"; }
bad()  { RUN=$((RUN+1)); echo "  FAIL  $*" >&2; }
die()  { echo "FATAL: $*" >&2; exit 99; }
skip() {
    if [ "${POGO_ENTH_REQUIRE:-}" = 1 ]; then
        echo "enth_new_repo_scenario_test.sh: FAIL (POGO_ENTH_REQUIRE=1): $1" >&2
        exit 1
    fi
    echo "enth_new_repo_scenario_test.sh: SKIP — $1"
    exit 0
}

EBIN="${POGO_ENTH_BIN_DIR:-}"
if [ -z "$EBIN" ]; then
    EBIN="$(command -v enthd 2>/dev/null)" && EBIN="$(dirname "$EBIN")" || skip "no enthd on PATH and POGO_ENTH_BIN_DIR unset"
fi
for b in enth enthd; do
    [ -x "$EBIN/$b" ] || skip "$EBIN has no $b"
done
"$EBIN/enth" --help 2>&1 | grep -q 'repo create NAME' || skip "$EBIN/enth has no 'repo create'"
"$EBIN/enth" --help 2>&1 | grep -q 'login \[--device\] URL' || skip "$EBIN/enth has no 'login'"
command -v jq >/dev/null || skip "no jq"

step "1. a fresh install: build pogo + pogod, isolate"
pogo_sandbox_create enthnew
ITEMS=()
cleanup() {
    [ -n "${ENTHD_PID:-}" ] && kill "$ENTHD_PID" 2>/dev/null
    for i in ${ITEMS[@]+"${ITEMS[@]}"}; do rm -f "/tmp/$i-land.attempt" "/tmp/$i-land.pending" "/tmp/$i-land.out" "/tmp/$i-land.err" "/tmp/$i-verdict.json"; done
    if [ -z "${ENTHNEW_KEEP:-}" ]; then
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
( cd "$REPO_ROOT" && GOBIN="$BIN_DIR" go install -p "${POGO_WORKER_CORES:-2}" ./cmd/pogo ./cmd/pogod ./cmd/enth-land-gate ) || die "go install failed"
command -v mg >/dev/null || die "mg is not on PATH"
pogo_sandbox_isolate
export TMPDIR="$POGO_SANDBOX_DIR/tmp"
mkdir -p "$TMPDIR"
S="$POGO_SANDBOX_DIR"
# The enth binaries of $EBIN without the host's enth-land-gate, and the
# inherited PATH without any directory holding one: the only gate on PATH is
# the one just built into $BIN_DIR (mg-7f8e4).
EHB="$S/enth-bin"
enth_bin_without_gate "$EBIN" "$EHB" || die "could not link the enth binaries of $EBIN"
export PATH="$BIN_DIR:$EHB:$(path_without_land_gate "$PATH")"
[ "$(command -v enth-land-gate)" = "$BIN_DIR/enth-land-gate" ] || die "the gate on PATH is $(command -v enth-land-gate), not the sandbox's $BIN_DIR/enth-land-gate"
[ "$(command -v enth)" = "$EHB/enth" ] || die "the enth on PATH is $(command -v enth), not $EHB/enth"
# A fresh machine has a git identity or it has nothing; give it one, as a new
# user's ~/.gitconfig would. pogod hands it to enth workers (mg-30da0).
git config --global user.name "New User"
git config --global user.email "new@example.com"
note "sandbox: $S"
[ -z "$(ls -A "$POGO_HOME" 2>/dev/null)" ] && ok "fresh  POGO_HOME is empty before pogod starts" || bad "fresh  POGO_HOME holds: $(ls -A "$POGO_HOME")"

step "2. enthd; the user signs in with enth login"
R="$S/enthd"
UOUT="$("$EBIN/enthd" --root "$R" user add newuser --email new@example.com)" || die "enthd user add failed"
TOKEN="$(printf '%s' "$UOUT" | sed -n 's/.*"token":"\(enth_[0-9a-f]*\)".*/\1/p')"
[ -n "$TOKEN" ] || die "enthd user add printed no token"
unset ENTH_TOKEN ENTH_REMOTE ENTH_REPO
"$EBIN/enthd" --root "$R" serve --listen 127.0.0.1:0 --follow-interval 0 > "$S/enthd.out" 2> "$S/enthd.err" &
ENTHD_PID=$!
for _ in $(seq 1 100); do grep -q '"type":"listening"' "$S/enthd.out" 2>/dev/null && break; sleep 0.1; done
URL="http://$(sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' "$S/enthd.out")"
[ "$URL" != "http://" ] || die "enthd did not start: $(cat "$S/enthd.err")"
printf '%s\n' "$TOKEN" | enth login "$URL" > "$S/login.out" 2>&1 || die "enth login failed: $(cat "$S/login.out")"
unset TOKEN
enth whoami 2>/dev/null | grep -q '"name":"newuser"' && ok "login  enth whoami names newuser (the token enth login saved, in the sandbox HOME)" || bad "login  whoami: $(enth whoami 2>&1)"

step "3. pogod installs the shipped prompts; the coordinator prompt teaches enth"
cat > "$S/agent.sh" <<EOF
#!/bin/sh
env > "$S/env.\$(basename "\$PWD")"
exec sleep 600
EOF
chmod +x "$S/agent.sh"
mkdir -p "$XDG_CONFIG_HOME/pogo" "$S/repos"
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
pogo server status 2>&1 | grep -q "pid=$POGO_SANDBOX_PID" || die "the pogo CLI does not reach the sandbox pogod (pid $POGO_SANDBOX_PID): $(pogo server status 2>&1)"
MAYOR="$POGO_HOME/agents/mayor.md"
for _ in $(seq 1 50); do [ -s "$MAYOR" ] && break; sleep 0.1; done
[ -s "$MAYOR" ] || { pogo init >/dev/null 2>&1; note "pogod wrote no prompts; ran pogo init"; }
[ -s "$MAYOR" ] && ok "fresh  the shipped coordinator prompt is installed at $MAYOR" || die "no coordinator prompt installed"
RENDERED="$(pogo agent prompt show mayor 2>/dev/null)"
[ -n "$RENDERED" ] || RENDERED="$(cat "$MAYOR")"
printf '%s' "$RENDERED" > "$S/mayor.rendered"
grep -q '^## Enth-backed projects' "$S/mayor.rendered" && ok "P6   the rendered coordinator prompt has the enth section" || bad "P6   no enth section in the coordinator prompt"
mg mail send mayor --from=test --create --subject=init --body=init >/dev/null 2>&1 || true

# The coordinator's recipe, lifted from the prompt: the bash block after
# "**Making a new enth repo**", comments stripped.
awk '/^\*\*Making a new enth repo\*\*/{f=1} f&&/^```bash$/{p=1;next} p&&/^```$/{exit} p{print}' "$S/mayor.rendered" \
    | sed 's/[[:space:]]*#.*$//' > "$S/recipe.sh"
[ "$(grep -c . "$S/recipe.sh")" -ge 4 ] && ok "P6   lifted the coordinator's new-repo recipe ($(grep -c . "$S/recipe.sh") commands)" || bad "P6   recipe: $(cat "$S/recipe.sh")"

step "4. 'make me a new enth repo called x', by the coordinator's recipe"
OWNER="$(enth whoami | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')"
X="$S/repos/x"
note "OWNER=$OWNER URL=$URL path=$X"
# Fill in the recipe's placeholders and nothing else.
sed -e "s|OWNER|$OWNER|g" -e "s|URL|$URL|g" -e "s|~/dev/x|$X|g" "$S/recipe.sh" > "$S/recipe.filled.sh"
while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
        "pogo doctor --check")
            # The whole doctor fails in a sandbox (no service installed); the
            # enth rows are what the recipe checks.
            pogo doctor --check > "$S/doctor.out" 2>&1
            grep -E 'enth (in PATH|login)' "$S/doctor.out" | sed 's/^/      /'
            if grep -q '✓  enth in PATH' "$S/doctor.out" && grep -q "✓  enth login .*signed in as $OWNER" "$S/doctor.out"; then
                ok "4    pogo doctor --check: enth in PATH and enth login pass"
            else
                bad "4    doctor enth rows: $(grep -i enth "$S/doctor.out")"
            fi
            ;;
        "pogo project add "*)
            # pogod's visit refuses a path under a temp root (isEphemeralPath),
            # and under ./test.sh this sandbox IS under one (/tmp/pogo-gate-tmp…),
            # so the add answers not-found there. index_roots registration is
            # exempt, and visit returns an already-registered project, so let the
            # indexer find x first. A real ~/dev/x is not a temp path and needs
            # no wait; outside a temp root this costs one index pass.
            for _ in $(seq 1 30); do pogo project list 2>/dev/null | grep -qF "$X" && break; sleep 1; done
            if OUT="$(bash -c "$line" 2>&1)"; then
                ok "4    $line"
            else
                bad "4    $line → $OUT"
            fi
            ;;
        *)
            if OUT="$(bash -c "$line" 2>&1)"; then
                ok "4    $line"
            else
                bad "4    $line → $OUT"
            fi
            ;;
    esac
done < "$S/recipe.filled.sh"
REC="$(pogo project list --json 2>/dev/null | jq -c --arg p "$X/" '.[] | select(.path == $p)')"
printf '%s' "$REC" | jq -e --arg u "$URL" --arg r "$OWNER/x" '.vcs == "enth" and .enth.remote == $u and .enth.repo == $r' >/dev/null \
    && ok "4    x is recorded as enth: $REC" || bad "4    project record: $REC ($(pogo project list --json 2>&1 | head -c 600))"
[ -f "$X/.enth/checkout" ] && [ ! -e "$X/.git" ] && ok "4    $X is an enth checkout with no .git" || bad "4    $X: $(ls -A "$X" 2>&1)"

step "5. a worker lands the first Change, from the shipped template"
ITEM="$(mg new --title="add a README" --repo="$X" --body="first change" 2>&1 | grep -o 'mg-[0-9a-f]*' | head -1)"
[ -n "$ITEM" ] || die "mg new failed"
ITEMS+=("$ITEM")
SOUT="$(pogo agent spawn-polecat px --id="$ITEM" --template=polecat --task="add a README" --body="first change" --repo="$X" 2>&1)" \
    && ok "5    spawn-polecat on the enth repo" || bad "5    spawn: $SOUT"
D="$POGO_HOME/polecats/px"
for _ in $(seq 1 100); do [ -s "$S/env.px" ] && break; sleep 0.1; done
[ -s "$S/env.px" ] || die "the polecat never started: $SOUT"
[ -f "$D/.enth/checkout" ] && [ ! -e "$D/.git" ] && ok "5    the polecat's workspace is an enth checkout, no git worktree" || bad "5    workspace $D: $(ls -A "$D" 2>&1)"
PR="$(pogo agent list --json 2>/dev/null | jq -r '.[] | select(.name == "px") | .prompt_file' | head -1)"
[ -s "$PR" ] || die "no prompt file for px"
awk '/^   self_land\(\) \{$/{p=1} p{print} p&&/^   \}$/{exit}' "$PR" | sed 's/^   //' > "$S/px.self_land"
grep -m1 '^   enth change -m ' "$PR" | sed 's/^   //' > "$S/px.change"
[ -s "$S/px.self_land" ] && [ -s "$S/px.change" ] && ok "5    lifted enth change + self_land from the prompt pogod rendered" || bad "5    blocks missing from $PR"
printf '{"verdict":"pass","summary":"first change"}\n' > "/tmp/$ITEM-verdict.json"
as_worker() {
    local envv=() line
    while IFS= read -r line; do envv+=("$line"); done < "$S/env.px"
    ( cd "$D" && env -i ${envv[@]+"${envv[@]}"} PATH="$PATH" POGO_PORT="$POGO_PORT" HOME="$HOME" \
        POGO_HOME="$POGO_HOME" MG_ROOT="$MG_ROOT" XDG_CONFIG_HOME="$XDG_CONFIG_HOME" TMPDIR="$TMPDIR" "$@" )
}
echo "# x" > "$D/README.md"
# The repo's merge gate, declared in the first Change. The gated land runs it
# on a checkout of the merged tree, so it passes only if the Change is in it.
mkdir -p "$D/.pogo"
echo 'quality_gate = "test -f README.md"' > "$D/.pogo/refinery.toml"
COUT="$(as_worker bash -c "$(sed 's/<type>: <description>/docs: add a README/' "$S/px.change")" 2>&1)" \
    && ok "5    the template's enth change made the first Change" || bad "5    enth change: $COUT"
LOUT="$(as_worker bash -c ". '$S/px.self_land'; self_land" 2>&1)"; RC=$?
[ "$RC" = 0 ] && printf '%s' "$LOUT" | grep -q "LANDED and recorded" && ok "5    self_land landed onto the empty trunk and recorded it" || bad "5    self_land rc $RC: $LOUT"
if ! enth land --help 2>&1 | grep -q -- '--require'; then
    printf '%s' "$LOUT" | grep -q '^GATE NOT INSTALLED' && ok "5    ... ungated: $EBIN/enth predates land --require" || bad "5    an enth without land --require, and no GATE NOT INSTALLED line: $LOUT"
else printf '%s' "$LOUT" | grep -q '^GATE COMMAND: test -f README.md$' && printf '%s' "$LOUT" | grep -qE '^(GATED|TEST-ONLY) \(mg-64f2e\)' \
    && ok "5    ... gated by the sandbox's enth-land-gate, running the declared gate: $(printf '%s' "$LOUT" | grep -oE '^(GATED|TEST-ONLY) \(mg-64f2e\)')" \
    || bad "5    self_land did not take the gated path with the declared gate: $LOUT"
fi
for _ in $(seq 1 100); do [ "$(mg show "$ITEM" 2>/dev/null | sed -n 's/^Status: *//p' | head -1)" = done ] && break; sleep 0.1; done
[ "$(mg show "$ITEM" 2>/dev/null | sed -n 's/^Status: *//p' | head -1)" = done ] && ok "5    $ITEM is done" || bad "5    $ITEM is $(mg show "$ITEM" 2>&1 | sed -n 's/^Status: *//p')"
enth --remote "$URL" --repo "$OWNER/x" query cat README.md --rev refs/heads/main 2>/dev/null | grep -q '# x' \
    && ok "5    README.md is on $OWNER/x's trunk" || bad "5    trunk: $(enth --remote "$URL" --repo "$OWNER/x" query log 2>&1)"

step "6. the auto-registration trap: an indexed enth checkout is recorded as git"
enth repo create "$OWNER/y" >/dev/null 2>&1 || bad "6    repo create y"
Y="$S/repos/y"
enth --remote "$URL" --repo "$OWNER/y" checkout main "$Y" >/dev/null 2>&1 || bad "6    checkout y"
pogo visit "$Y" >/dev/null 2>&1
for _ in $(seq 1 30); do pogo project list 2>/dev/null | grep -qF "$Y" && break; sleep 1; done
YREC="$(pogo project list --json 2>/dev/null | jq -c --arg p "$Y/" '.[] | select(.path == $p)')"
if [ -z "$YREC" ]; then
    bad "6    y was never auto-registered: $(pogo project list 2>&1)"
else
    printf '%s' "$YREC" | jq -e 'has("vcs") | not' >/dev/null && note "y auto-registered with no vcs key (git): $YREC"
    pogo doctor --check > "$S/doctor2.out" 2>&1
    FIX="$(grep 'enth repo recorded as git' "$S/doctor2.out" | sed -n 's/.*run: \(pogo project set-vcs .*\)$/\1/p' | head -1)"
    [ -n "$FIX" ] && printf '%s' "$FIX" | grep -q -- "--enth-remote=$URL --enth-repo=$OWNER/y" \
        && ok "6    doctor flags y and prints: $FIX" || bad "6    doctor: $(grep -i enth "$S/doctor2.out")"
    [ -n "$FIX" ] && bash -c "$FIX" >/dev/null 2>&1
    pogo project list --json 2>/dev/null | jq -e --arg p "$Y/" '.[] | select(.path == $p) | .vcs == "enth"' >/dev/null \
        && ok "6    the printed line recorded y as enth" || bad "6    y: $(pogo project list --json 2>&1 | jq -c --arg p "$Y/" '.[] | select(.path == $p)')"
    pogo doctor --check 2>&1 | grep -q 'enth repo recorded as git' && bad "6    doctor still flags y" || ok "6    ... and doctor no longer flags it"
fi

step "done"
echo "  ran $RUN observations; $PASSED passed"
[ "$RUN" = "$PASSED" ] || exit 1
