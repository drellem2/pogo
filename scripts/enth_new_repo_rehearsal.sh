#!/usr/bin/env bash
# =============================================================================
# enth_new_repo_rehearsal.sh — GH #284 follow-up (mg-dbcac): "make me a new enth
# repo called x and add a README", said in plain words to a REAL coordinator on a
# fresh install. The coordinator works only from the prompts pogod installs.
#
#   scripts/enth_new_repo_rehearsal.sh [--budget-minutes N] [--token-budget N]
#                                      [--enth-url URL] [--seed-onboarding] [--keep]
#
# MANUAL ONLY. NOT IN test.sh, AND IT MUST NEVER BE: it spends real model turns
# for at least two agents (the coordinator and its worker). Run it on a quiet
# host. It stops itself at the wall-clock budget or the token budget, whichever
# comes first.
#
# The cheaper sibling is scripts/enth_new_repo_scenario_test.sh (#285). It runs
# the commands lifted from the shipped prompts, with `sleep`s for agents. This
# script is the part that sibling cannot show: that a real coordinator, given a
# plain-words request, finds the "Making a new enth repo" recipe in mayor.md and
# dispatches a worker that lands.
#
# WHAT IT SETS UP
#   - pogo + pogod built from THIS tree, with HOME, XDG_CONFIG_HOME, POGO_HOME and
#     MG_ROOT pinned under a fresh root by scripts/pogo-sandbox. There are no
#     prompt edits and no agent memory, only what pogod installs.
#     The root defaults to $HOME/pogo-rehearsal-<time>, not a temp dir: pogod's
#     `pogo project add` refuses paths under a temp root (isEphemeralPath), and
#     the recipe registers ~/dev/x.
#   - an enthd with one user, signed in with `enth login` (the token is read from
#     stdin). This stands in for the device login. `--enth-url https://enth.app`
#     does the real device login instead. That is a HUMAN step, and it creates a
#     real repo named x on that account.
#   - `[agents] autostart = true`, `command` unset (the harness default, claude).
#   - POGO_PORT exported to pogod, so the agents it spawns reach THIS daemon. The
#     sandbox daemon is started with -port, and agents inherit pogod's env. With
#     POGO_PORT unset they would talk to the live pogod on 10000 and dispatch onto
#     the live fleet. The script checks the coordinator's own env for it before
#     it sends the request.
#
# HUMAN STEPS (the report lists the ones this run used)
#   1. A Claude login for the agents. Run `claude setup-token` and export
#      CLAUDE_CODE_OAUTH_TOKEN (or export ANTHROPIC_API_KEY). Without one the
#      script refuses before it builds anything (exit 2). A fresh HOME has no
#      login, and pogod's auth preflight refuses to start the crew.
#   2. Claude's first-run theme picker. A fresh profile shows it EVEN WITH a token
#      (measured on 2.1.318: `claude auth status` reports loggedIn:true, then the
#      TUI opens on "Choose the text style"). pogod does not answer it, so the
#      coordinator would sit there. At a terminal the script runs `claude` once in
#      the sandbox HOME: pick a theme, then /exit (no model turn is spent).
#      --seed-onboarding writes {"hasCompletedOnboarding":true} to the sandbox
#      ~/.claude.json instead. The report records that as a deviation from
#      "fresh".
#   3. enth login (only with --enth-url; the sandboxed enthd needs none).
#   4. Anything the coordinator mails `human` during the run. Each one is printed
#      and counted.
#
# PASS (each one observed, not inferred)
#   P1  enth repo OWNER/x exists on the remote
#   P2  pogo records a project for it with vcs=enth, enth.repo=OWNER/x
#   P3  a work item on that project, and a polecat dispatched with
#       --template=polecat (read from the coordinator's own Bash calls in its
#       transcript)
#   P4  that item is done, and its result sidecar holds landed {vcs: enth, id}
#   P5  README.md is on OWNER/x's trunk
#
# Exit: 0 every PASS observed; 1 one or more not observed by the budget; 2 a
# prerequisite is missing (nothing was spent); 99 the sandbox could not be
# established.
# =============================================================================

set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

BUDGET_MIN=30
TOKEN_BUDGET=3000000
ENTH_URL=""
SEED_ONBOARDING=""
KEEP=""
REQUEST="make me a new enth repo called x and add a README"

usage() { sed -n '2,70p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
while [ $# -gt 0 ]; do
    case "$1" in
        --budget-minutes) BUDGET_MIN="$2"; shift 2 ;;
        --budget-minutes=*) BUDGET_MIN="${1#*=}"; shift ;;
        --token-budget) TOKEN_BUDGET="$2"; shift 2 ;;
        --token-budget=*) TOKEN_BUDGET="${1#*=}"; shift ;;
        --enth-url) ENTH_URL="$2"; shift 2 ;;
        --enth-url=*) ENTH_URL="${1#*=}"; shift ;;
        --seed-onboarding) SEED_ONBOARDING=1; shift ;;
        --keep) KEEP=1; shift ;;
        -h|--help) usage 0 ;;
        *) echo "unknown argument: $1" >&2; usage 2 ;;
    esac
done

step() { echo; echo "===== $* ====="; }
note() { echo "  - $*"; }
need() { echo "enth_new_repo_rehearsal.sh: MISSING PREREQUISITE — $*" >&2; echo "nothing was built or spent." >&2; exit 2; }
die()  { echo "FATAL: $*" >&2; exit 99; }

HUMAN_STEPS=()
DEVIATIONS=()

# --- prerequisites: every refusal here happens before anything is spent ------
for b in claude enth mg jq go curl; do command -v "$b" >/dev/null || need "$b is not on PATH"; done
if [ -z "$ENTH_URL" ]; then
    command -v enthd >/dev/null || need "no enthd on PATH (or pass --enth-url for a real remote)"
fi
enth --help 2>&1 | grep -q 'repo create NAME' || need "this enth has no 'repo create'"
if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    HUMAN_STEPS+=("claude login: CLAUDE_CODE_OAUTH_TOKEN supplied (claude setup-token)")
elif [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    HUMAN_STEPS+=("claude login: ANTHROPIC_API_KEY supplied")
else
    need "no Claude login for a fresh HOME. Run \`claude setup-token\` and export CLAUDE_CODE_OAUTH_TOKEN (or export ANTHROPIC_API_KEY), then re-run. pogod refuses to start a crew whose harness reports no login, and a fresh HOME has none."
fi
if [ -z "$SEED_ONBOARDING" ] && [ ! -t 0 ]; then
    need "stdin is not a terminal, so no one can answer Claude's first-run theme picker. Run at a terminal, or pass --seed-onboarding."
fi
case "$BUDGET_MIN$TOKEN_BUDGET" in *[!0-9]*) need "--budget-minutes and --token-budget take whole numbers" ;; esac

# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/pogo-sandbox"

step "0. the host"
pogo host load 2>/dev/null | sed 's/^/      /' || note "pogo host load unavailable (no live pogod?)"
note "budget: $BUDGET_MIN min wall clock, $TOKEN_BUDGET tokens (input + cache writes + output, cache reads reported separately)"

step "1. a fresh install: build pogo + pogod, isolate"
export POGO_SANDBOX_ROOT="${POGO_SANDBOX_ROOT:-$HOME/pogo-rehearsal-$(date +%Y%m%d-%H%M%S)}"
pogo_sandbox_create rehearse
S="$POGO_SANDBOX_DIR"
ENTHD_PID=""
stop_agents() {
    # `pogo agent stop` first, then the pids: agents outlive a killed pogod.
    local names pids p
    [ -n "${POGO_PORT:-}" ] || return 0
    pids="$(pogo agent list --json 2>/dev/null | jq -r '.[].pid // empty')"
    names="$(pogo agent list --json 2>/dev/null | jq -r '.[].name')"
    for p in $names; do pogo agent stop "$p" >/dev/null 2>&1; done
    for p in $pids; do kill "$p" 2>/dev/null; done
}
cleanup() {
    stop_agents
    [ -n "$ENTHD_PID" ] && kill "$ENTHD_PID" 2>/dev/null
    if [ -z "$KEEP" ]; then
        pogo_sandbox_down
    else
        pogo_sandbox_daemon_kill
        sandbox_port_release
        echo "KEPT: $S"
    fi
}
trap cleanup EXIT INT TERM HUP

BIN_DIR="$S/bin"
mkdir -p "$BIN_DIR"
( cd "$REPO_ROOT" && GOBIN="$BIN_DIR" go install -p "${POGO_WORKER_CORES:-2}" ./cmd/pogo ./cmd/pogod ./cmd/lsp ) || die "go install failed"
pogo_sandbox_isolate
# pogo_sandbox_isolate turns autostart off for every sandbox. This one is about
# the crew starting by itself, so it is turned back on here.
unset POGO_AGENT_AUTOSTART
export TMPDIR="$S/tmp"
mkdir -p "$TMPDIR"
export PATH="$BIN_DIR:$PATH"
git config --global user.name "New User"
git config --global user.email "new@example.com"
note "sandbox: $S"
[ -z "$(ls -A "$POGO_HOME" 2>/dev/null)" ] && note "POGO_HOME is empty before pogod starts" || die "POGO_HOME is not fresh: $(ls -A "$POGO_HOME")"

step "2. enth login"
unset ENTH_TOKEN ENTH_REMOTE ENTH_REPO
if [ -z "$ENTH_URL" ]; then
    R="$S/enthd"
    UOUT="$(enthd --root "$R" user add newuser --email new@example.com)" || die "enthd user add failed"
    TOKEN="$(printf '%s' "$UOUT" | sed -n 's/.*"token":"\(enth_[0-9a-f]*\)".*/\1/p')"
    [ -n "$TOKEN" ] || die "enthd user add printed no token"
    enthd --root "$R" serve --listen 127.0.0.1:0 --follow-interval 0 > "$S/enthd.out" 2> "$S/enthd.err" &
    ENTHD_PID=$!
    for _ in $(seq 1 100); do grep -q '"type":"listening"' "$S/enthd.out" 2>/dev/null && break; sleep 0.1; done
    ENTH_URL="http://$(sed -n 's/.*"addr":"\([^"]*\)".*/\1/p' "$S/enthd.out")"
    [ "$ENTH_URL" != "http://" ] || die "enthd did not start: $(cat "$S/enthd.err")"
    printf '%s\n' "$TOKEN" | enth login "$ENTH_URL" > "$S/login.out" 2>&1 || die "enth login failed: $(cat "$S/login.out")"
    unset TOKEN
    DEVIATIONS+=("enth: a sandboxed enthd at $ENTH_URL; the device login was stood in for by a token read from stdin")
else
    echo "  enth login --device $ENTH_URL: approve the code in a browser."
    enth login --device "$ENTH_URL" || die "enth login failed"
    HUMAN_STEPS+=("enth login --device $ENTH_URL (browser approval)")
fi
OWNER="$(enth whoami 2>/dev/null | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')"
[ -n "$OWNER" ] || die "enth whoami names no one after login: $(enth whoami 2>&1)"
note "signed in to $ENTH_URL as $OWNER"
E() { enth --remote "$ENTH_URL" --repo "$OWNER/x" "$@"; }

step "3. Claude's first-run onboarding, in the sandbox HOME"
if [ -n "$SEED_ONBOARDING" ]; then
    printf '{"hasCompletedOnboarding":true}\n' > "$HOME/.claude.json"
    DEVIATIONS+=("claude: --seed-onboarding wrote hasCompletedOnboarding to the sandbox ~/.claude.json (the theme picker was skipped, not answered)")
else
    mkdir -p "$S/onboard"
    echo "  Claude opens now, in the sandbox HOME. Pick a theme, answer the trust prompt, then type /exit."
    echo "  Do not send it a message: that would spend a turn outside the budget."
    ( cd "$S/onboard" && claude )
    grep -q '"hasCompletedOnboarding": *true' "$HOME/.claude.json" 2>/dev/null \
        || die "onboarding is still incomplete in $HOME/.claude.json; the coordinator would stall at the theme picker"
    HUMAN_STEPS+=("claude first-run theme picker, answered at a terminal")
fi
AUTH="$(claude auth status --json 2>/dev/null)"
printf '%s' "$AUTH" | jq -e '.loggedIn == true' >/dev/null || die "claude reports no login in the sandbox HOME: $AUTH"
note "claude auth status in the sandbox: $(printf '%s' "$AUTH" | jq -c '{loggedIn, authMethod}')"

step "4. pogod starts the crew by itself"
mkdir -p "$XDG_CONFIG_HOME/pogo"
cat > "$XDG_CONFIG_HOME/pogo/config.toml" <<EOF
[agents]
coordinator = "mayor"
autostart = true
EOF
mg init >/dev/null 2>&1 || die "mg init failed"
# Reserve the port FIRST and export it, so pogod's env carries it and every agent
# it spawns reaches this daemon rather than the live one.
sandbox_port_reserve
POGO_SANDBOX_PORT="$SANDBOX_PORT"
POGO_SANDBOX_URL="http://127.0.0.1:$POGO_SANDBOX_PORT"
export POGO_PORT="$POGO_SANDBOX_PORT"
START_EPOCH="$(date +%s)"
DEADLINE=$((START_EPOCH + BUDGET_MIN * 60))
pogo_sandbox_daemon "$BIN_DIR/pogod" /health
pogo server status 2>&1 | grep -q "pid=$POGO_SANDBOX_PID" || die "the pogo CLI does not reach the sandbox pogod (pid $POGO_SANDBOX_PID): $(pogo server status 2>&1)"
MAYOR_MD="$POGO_HOME/agents/mayor.md"
for _ in $(seq 1 100); do [ -s "$MAYOR_MD" ] && break; sleep 0.2; done
[ -s "$MAYOR_MD" ] || die "pogod installed no coordinator prompt at $MAYOR_MD"
if ! pogo agent prompt show mayor 2>/dev/null | grep -q '^\*\*Making a new enth repo\*\*'; then
    echo "  FAIL  the shipped coordinator prompt has no \"Making a new enth repo\" recipe." >&2
    echo "        This tree predates it (drellem2/pogo#285). Nothing was sent to the coordinator." >&2
    exit 1
fi
note "the installed coordinator prompt carries the \"Making a new enth repo\" recipe"
MPID=""
for _ in $(seq 1 180); do
    MPID="$(pogo agent list --json 2>/dev/null | jq -r '.[] | select(.name == "mayor" and .status == "running") | .pid // empty' | head -1)"
    [ -n "$MPID" ] && break
    sleep 1
done
[ -n "$MPID" ] || die "the coordinator never started: $(pogo agent list 2>&1); log: $(tail -20 "$POGO_SANDBOX_LOG")"
# The guard that keeps this run off the live fleet. Read the coordinator's env
# from its own pid (checked non-empty above, so `ps` cannot describe this shell).
MENV="$(ps eww -o command= -p "$MPID" 2>/dev/null)"
printf '%s' "$MENV" | grep -q "POGO_PORT=$POGO_PORT" || die "the coordinator (pid $MPID) does not carry POGO_PORT=$POGO_PORT; it would reach the live pogod"
printf '%s' "$MENV" | grep -q "HOME=$HOME" || die "the coordinator (pid $MPID) does not carry the sandbox HOME"
note "coordinator running, pid $MPID, POGO_PORT=$POGO_PORT, HOME=$HOME"

# Give the coordinator its boot turn before the request lands. The request is mail,
# so it reaches it by wakewatch pointer whenever that turn ends.
sleep 20

step "5. the request, in plain words: \"$REQUEST\""
# The mayor's box exists once it is spawned; --create only if this fresh store
# refuses the send, which would be the first write to a new correspondent.
if ! OUT="$(mg mail send mayor --from=human --subject="new repo" --body="$REQUEST" 2>&1)"; then
    OUT="$(mg mail send mayor --from=human --create --subject="new repo" --body="$REQUEST" 2>&1)" || die "could not mail the coordinator: $OUT"
fi
note "$OUT"
SENT_EPOCH="$(date +%s)"

# --- observation --------------------------------------------------------------
X_PATH=""; ITEM=""; P1=""; P2=""; P3=""; P4=""; P5=""
SEEN_HUMAN="$S/seen-human"; : > "$SEEN_HUMAN"   # bash 3.2 (macOS): no associative arrays
HUMAN_MAILS=0
tokens() {
    # Sum usage over every transcript in the sandbox HOME (all agents).
    find "$HOME/.claude/projects" -name '*.jsonl' -print0 2>/dev/null \
        | xargs -0 cat 2>/dev/null \
        | jq -r 'select(.type == "assistant") | .message.usage // empty
                 | [(.input_tokens // 0) + (.cache_creation_input_tokens // 0) + (.output_tokens // 0), (.cache_read_input_tokens // 0)] | @tsv' 2>/dev/null \
        | awk '{a += $1; c += $2} END {printf "%d %d\n", a, c}'
}
coordinator_bash() {
    # Every Bash command any agent ran, newest last. The coordinator's are the
    # ones from the mayor's transcript; read them all and let the grep decide.
    find "$HOME/.claude/projects" -name '*.jsonl' -print0 2>/dev/null \
        | xargs -0 cat 2>/dev/null \
        | jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use" and .name == "Bash") | .input.command' 2>/dev/null
}
elapsed() { echo "$(( $(date +%s) - SENT_EPOCH ))s"; }

step "6. watching (until $(date -r "$DEADLINE" '+%H:%M:%S' 2>/dev/null || echo "+${BUDGET_MIN}m"))"
STOP_REASON="budget: wall clock"
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
    # Mail to human is a human step the coordinator asked for.
    for f in "$MG_ROOT"/mail/human/new/* "$MG_ROOT"/mail/human/cur/*; do
        [ -f "$f" ] || continue
        b="$(basename "$f")"; b="${b%%:*}"   # a maildir name keeps its stem across new/ -> cur/
        grep -qxF "$b" "$SEEN_HUMAN" && continue
        echo "$b" >> "$SEEN_HUMAN"
        HUMAN_MAILS=$((HUMAN_MAILS + 1))
        echo "  [$(elapsed)] MAIL TO HUMAN:"; sed 's/^/        /' "$f" | head -30
    done
    # `enth repos`, not `query log`: query log fails (rc 3) on a repo that
    # `repo create` just made, until something lands. Measured both ways.
    if [ -z "$P1" ] && enth --remote "$ENTH_URL" repos 2>/dev/null | grep -qF "\"repo\":\"$OWNER/x\""; then P1="$(elapsed)"; echo "  [$P1] P1 enth repo $OWNER/x exists"; fi
    if [ -z "$P2" ]; then
        REC="$(pogo project list --json 2>/dev/null | jq -c --arg r "$OWNER/x" '[.[] | select(.vcs == "enth" and .enth.repo == $r)][0] // empty')"
        if [ -n "$REC" ]; then P2="$(elapsed)"; X_PATH="$(printf '%s' "$REC" | jq -r .path)"; echo "  [$P2] P2 registered: $REC"; fi
    fi
    if [ -n "$X_PATH" ] && [ -z "$ITEM" ]; then
        ITEM="$(mg list --all --json 2>/dev/null | jq -r --arg p "${X_PATH%/}" 'select((.repo // "" | rtrimstr("/")) == $p) | .id' | head -1)"
        [ -n "$ITEM" ] && echo "  [$(elapsed)] work item on x: $ITEM"
    fi
    if [ -n "$ITEM" ] && [ -z "$P3" ]; then
        SPAWN="$(coordinator_bash | grep -E "spawn-polecat.*$ITEM|spawn-polecat.*--id=$ITEM" | tail -1)"
        if [ -n "$SPAWN" ]; then
            if printf '%s' "$SPAWN" | grep -qE -- '--template[= ]polecat( |$)'; then
                P3="$(elapsed)"; echo "  [$P3] P3 dispatched on the polecat template: $SPAWN"
            elif ! printf '%s' "$SPAWN" | grep -q -- '--template'; then
                P3="$(elapsed)"; echo "  [$P3] P3 dispatched with the default template: $SPAWN"
                DEVIATIONS+=("P3: the coordinator passed no --template (the default is polecat)")
            fi
        fi
    fi
    if [ -n "$ITEM" ] && [ -z "$P4" ]; then
        if [ "$(mg show "$ITEM" 2>/dev/null | sed -n 's/^Status: *//p' | head -1)" = done ]; then
            SC="$(cat "$MG_ROOT/work/done/$ITEM.result.json" 2>/dev/null)"
            if printf '%s' "$SC" | jq -e '.landed.vcs == "enth" and (.landed.id | length > 0)' >/dev/null 2>&1; then
                P4="$(elapsed)"; echo "  [$P4] P4 $ITEM done, sidecar landed $(printf '%s' "$SC" | jq -c .landed)"
            else
                echo "  [$(elapsed)] $ITEM is done but its sidecar has no landed {enth, id}: $SC"
                P4="FAIL"
            fi
        fi
    fi
    if [ -z "$P5" ] && E query cat README.md --rev refs/heads/main >/dev/null 2>&1; then P5="$(elapsed)"; echo "  [$P5] P5 README.md is on the trunk"; fi
    if [ -n "$P1" ] && [ -n "$P2" ] && [ -n "$P3" ] && [ -n "$P4" ] && [ -n "$P5" ]; then STOP_REASON="every criterion observed"; break; fi
    [ "$P4" = FAIL ] && { STOP_REASON="item closed without a landed sidecar"; break; }
    read -r USED CREAD <<<"$(tokens)"
    if [ "${USED:-0}" -ge "$TOKEN_BUDGET" ]; then STOP_REASON="budget: $USED tokens"; break; fi
    sleep 10
done
read -r USED CREAD <<<"$(tokens)"
stop_agents

step "report"
pass() { [ -n "$1" ] && [ "$1" != FAIL ] && echo "PASS  ($1)" || echo "NOT OBSERVED"; }
echo "  stopped: $STOP_REASON after $(elapsed)"
echo "  P1 enth repo $OWNER/x exists ............... $(pass "$P1")"
echo "  P2 registered vcs=enth ..................... $(pass "$P2")"
echo "  P3 dispatched on the polecat template ...... $(pass "$P3")"
echo "  P4 item done, sidecar landed {enth, id} .... $(pass "$P4")   item=${ITEM:-none}"
echo "  P5 README.md on trunk ...................... $(pass "$P5")"
echo "  tokens: ${USED:-0} (input + cache writes + output), ${CREAD:-0} cache reads; budget $TOKEN_BUDGET"
echo "  human steps used:"
for h in ${HUMAN_STEPS[@]+"${HUMAN_STEPS[@]}"}; do echo "    - $h"; done
echo "    - mail the coordinator sent to human during the run: $HUMAN_MAILS"
echo "  deviations from a bare fresh install:"
for d in ${DEVIATIONS[@]+"${DEVIATIONS[@]}"}; do echo "    - $d"; done
echo "  the coordinator's commands (Bash, all agents, in transcript order):"
coordinator_bash | grep -E '^(enth|pogo|mg) ' | sed 's/^/    $ /' | head -60
[ -n "$P1" ] && [ -n "$P2" ] && [ -n "$P3" ] && [ -n "$P4" ] && [ "$P4" != FAIL ] && [ -n "$P5" ]
