#!/usr/bin/env bash
#
# upgrade-smoke.sh — live v0.3.0 -> v0.4.0 upgrade smoke for the role-rename
# migration guard. This is the release gate for any pogo version that ships a
# change to a role-name default (mg-ce47 flipped coordinator mayor->ringmaster
# and worker polecat->pogocat).
#
# The guard (internal/config/migrate.go) pins the frozen legacy role names into
# an existing install's config.toml so the flip only reaches fresh installs. The
# guard being correct is not enough: both binaries must RUN it before they
# resolve a role name, or they spend their first boot holding the new names
# while writing the old ones to disk (mg-bc47). This script exercises both of
# those pin sites against real binaries, on a real config.toml, in a throwaway
# sandbox.
#
#   Phase A  `pogo install` pin site      (cmd/pogo/main.go, pinAndResolveRoles)
#   Phase B  pogod boot pin site          (cmd/pogod/main.go, pinAndResolveRoles)
#   Phase C  fresh install adopts the new defaults, worker identifiers frozen
#   Phase D  in-process frozen-identifier guard (internal/agent)
#
# Every phase runs under a sandboxed HOME / XDG_CONFIG_HOME / POGO_HOME and a
# private port, so the machine's live daemon, config and crew are never touched.
#
# Usage:  ./scripts/upgrade-smoke.sh
# Exit:   0 = gate passes, 1 = gate fails (do not publish)

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SMOKE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pogo-upgrade-smoke.XXXXXX")"
BIN_DIR="$SMOKE_DIR/bin"

# A distinctive agent command so the cleanup sweep cannot reap an unrelated
# process. Mirrors cmd/pogod/upgrade_boot_test.go's agentMarker.
AGENT_MARKER="sleep 61347"

FAILURES=0
DAEMON_PIDS=()

cleanup() {
    for pid in "${DAEMON_PIDS[@]:-}"; do
        [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null
    done
    sleep 1
    for pid in "${DAEMON_PIDS[@]:-}"; do
        [ -n "$pid" ] && kill -KILL "$pid" 2>/dev/null
    done
    pkill -f "$AGENT_MARKER" 2>/dev/null
    rm -rf "$SMOKE_DIR"
}
trap cleanup EXIT

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

# expect_contains <label> <haystack> <needle>
expect_contains() {
    if printf '%s' "$2" | grep -qF -- "$3"; then pass "$1"; else
        fail "$1 — expected to find: $3"
    fi
}

# expect_absent <label> <haystack> <needle>
expect_absent() {
    if printf '%s' "$2" | grep -qF -- "$3"; then
        fail "$1 — found forbidden string: $3"
    else pass "$1"; fi
}

free_port() {
    python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

# new_sandbox <name> — prints the sandbox root. Creates state/, ws/, .config/.
new_sandbox() {
    local sb="$SMOKE_DIR/$1"
    mkdir -p "$sb/state" "$sb/.config" "$sb/ws"
    printf '%s' "$sb"
}

# in_sandbox <sandbox> <port> <cmd...> — run a command with every state path
# redirected into the sandbox and the daemon port made private.
in_sandbox() {
    local sb="$1" port="$2"; shift 2
    ( cd "$sb/ws" && env \
        HOME="$sb" \
        XDG_CONFIG_HOME="$sb/.config" \
        POGO_HOME="$sb/state" \
        POGO_PORT="$port" \
        PATH="$BIN_DIR:$PATH" \
        "$@" 2>&1 )
}

# wait_for_log <file> <needle> <seconds>
wait_for_log() {
    local file="$1" needle="$2" deadline=$(( SECONDS + $3 ))
    while [ "$SECONDS" -lt "$deadline" ]; do
        if [ -f "$file" ] && grep -qF -- "$needle" "$file"; then return 0; fi
        sleep 0.2
    done
    return 1
}

# kill_daemon_on <port>
kill_daemon_on() {
    local pid
    pid="$(lsof -ti "tcp:$1" 2>/dev/null | head -1)"
    if [ -n "$pid" ]; then
        kill -TERM "$pid" 2>/dev/null
        sleep 1
        kill -KILL "$pid" 2>/dev/null
    fi
}

# The v0.3.0-era config: an [agents] table that predates the role keys entirely.
# This is the exact shape the guard exists to upgrade.
seed_v030_config() {
    local sb="$1" port="$2" autostart="$3"
    cat > "$sb/state/config.toml" <<EOF
[server]
port = $port

[agents]
autostart = $autostart
EOF
    if [ "$autostart" = "true" ]; then
        printf 'command = "%s"\n' "$AGENT_MARKER" >> "$sb/state/config.toml"
    fi
}

# ---------------------------------------------------------------------------

say "pogo upgrade smoke — v0.3.0 -> $(grep -o '"[0-9.]*"' "$REPO_ROOT/internal/version/version.go" | head -1 | tr -d '"')"
info "repo:    $REPO_ROOT"
info "sandbox: $SMOKE_DIR"

say "Building pogo + pogod from this tree"
mkdir -p "$BIN_DIR"
if ! (cd "$REPO_ROOT" && go build -o "$BIN_DIR/pogo" ./cmd/pogo && go build -o "$BIN_DIR/pogod" ./cmd/pogod); then
    fail "build"
    exit 1
fi
info "built $BIN_DIR/pogo, $BIN_DIR/pogod"

# --- Phase A: `pogo install` pin site --------------------------------------
#
# main() resolves role names from config.Load() at startup, which fills a
# role-key-less [agents] with the LIVE Default* consts (ringmaster/pogocat).
# `pogo install` must pin the frozen legacy names and RE-resolve before it
# synthesizes prompts or prints its next steps. The "pogo agent start <name>"
# line is printed from agent.CoordinatorName() — the process-wide name that only
# pinAndResolveRoles sets — so it is a precise, daemon-independent probe of this
# pin site.

say "Phase A — upgrade via \`pogo install\` (cmd/pogo/main.go pin site)"
SB_A="$(new_sandbox sbA)"
PORT_A="$(free_port)"
seed_v030_config "$SB_A" "$PORT_A" false
info "seeded v0.3.0-style config.toml (no coordinator/worker keys):"
sed 's/^/    | /' "$SB_A/state/config.toml"

info "\$ pogo install"
OUT_A="$(in_sandbox "$SB_A" "$PORT_A" "$BIN_DIR/pogo" install)"
printf '%s\n' "$OUT_A" | sed 's/^/    | /'
kill_daemon_on "$PORT_A"

expect_contains "A1 install resolves coordinator to 'mayor'"      "$OUT_A" "pogo agent start mayor"
expect_absent   "A2 install never names 'ringmaster'"             "$OUT_A" "ringmaster"

CFG_A="$(cat "$SB_A/state/config.toml")"
expect_contains "A3 config pinned coordinator = \"mayor\""        "$CFG_A" 'coordinator = "mayor"'
expect_contains "A4 config pinned worker = \"polecat\""           "$CFG_A" 'worker = "polecat"'

# The installed prompt FILES keep their {{.Coordinator}}/{{.Worker}} placeholders
# — role names are expanded at synthesis, not at write time — so grepping the
# files on disk proves nothing. `pogo agent prompt show` runs the real synthesis
# path against the pinned config, which is what the agent harness will read.
PROSE_A="$(in_sandbox "$SB_A" "$PORT_A" "$BIN_DIR/pogo" agent prompt show mayor)"
expect_contains "A5 synthesized prose names the coordinator 'mayor'" "$PROSE_A" "You are the mayor —"
expect_contains "A6 synthesized prose names the worker 'polecat'"    "$PROSE_A" "spawn polecats (disposable worker agents)"
expect_absent   "A7 synthesized prose free of 'ringmaster'"          "$PROSE_A" "ringmaster"
expect_absent   "A8 synthesized prose free of 'pogocat'"             "$PROSE_A" "pogocat"

if in_sandbox "$SB_A" "$PORT_A" "$BIN_DIR/pogo" agent prompt show ringmaster >/dev/null 2>&1; then
    fail "A9 'ringmaster' resolves as coordinator on an install pinned to 'mayor'"
else
    pass "A9 'ringmaster' does not resolve on an install pinned to 'mayor'"
fi

# --- Phase B: pogod boot pin site ------------------------------------------
#
# pogod pins and re-resolves immediately after config.Load(), before prompt
# refresh, crew auto-start, the stall watcher, or refinery coordinator mail read
# a role name. The auto-start sweep names the agent after the coordinator, so the
# boot log is the probe.

say "Phase B — upgrade via pogod boot (cmd/pogod/main.go pin site)"
SB_B="$(new_sandbox sbB)"
PORT_B="$(free_port)"
seed_v030_config "$SB_B" "$PORT_B" true
info "seeded v0.3.0-style config.toml (no coordinator/worker keys, autostart on):"
sed 's/^/    | /' "$SB_B/state/config.toml"

LOG_B="$SB_B/pogod.log"
info "\$ pogod -port $PORT_B"
( cd "$SB_B/ws" && env \
    HOME="$SB_B" XDG_CONFIG_HOME="$SB_B/.config" POGO_HOME="$SB_B/state" \
    POGO_PORT="$PORT_B" PATH="$BIN_DIR:$PATH" \
    "$BIN_DIR/pogod" -port "$PORT_B" >"$LOG_B" 2>&1 ) &
DAEMON_PIDS+=($!)
# Suppress bash's "Terminated: 15" job notice when we SIGTERM the daemon below.
disown 2>/dev/null || true

if ! wait_for_log "$LOG_B" "auto-started " 60; then
    fail "B0 pogod never auto-started an agent within 60s"
    sed 's/^/    | /' "$LOG_B"
else
    kill_daemon_on "$PORT_B"
    pkill -f "$AGENT_MARKER" 2>/dev/null

    LOGS_B="$(cat "$LOG_B")"
    printf '%s\n' "$LOGS_B" | grep -E 'auto-started|stall watcher|pinned' | sed 's/^/    | /'

    # Bare literals throughout: comparing against config.DefaultCoordinator would
    # make these follow a future default flip instead of catching it.
    expect_contains "B1 boot 1 auto-started coordinator as 'mayor'"   "$LOGS_B" "auto-started mayor"
    expect_absent   "B2 boot 1 did not auto-start 'ringmaster'"       "$LOGS_B" "auto-started ringmaster"
    expect_absent   "B3 stall watcher not armed on 'ringmaster'"      "$LOGS_B" "stall watcher enabled (agent=ringmaster"

    CFG_B="$(cat "$SB_B/state/config.toml")"
    expect_contains "B4 config pinned coordinator = \"mayor\""        "$CFG_B" 'coordinator = "mayor"'
    expect_contains "B5 config pinned worker = \"polecat\""           "$CFG_B" 'worker = "polecat"'
fi

# --- Phase C: fresh install + frozen worker identifiers ---------------------
#
# The mirror image of Phase A: a machine with no config.toml and no stamped
# prompts is a FRESH install and is meant to adopt the new defaults. If this
# phase passed with mayor/polecat the guard would be pinning everywhere and the
# flip would be dead code.
#
# It also proves the flip moves DISPLAY prose only. Under the new defaults the
# worker is called a "pogocat" in prose while every load-bearing identifier
# stays "polecat": the prompt-file names (mayor.md, templates/polecat.md), the
# template lookup key, the spawn subcommand, and the polecat- branch prefix that
# the shipped build-pr template tells workers to push.

say "Phase C — fresh install adopts the new defaults; worker identifiers frozen"
SB_C="$(new_sandbox sbC)"
PORT_C="$(free_port)"
info "no config.toml, no stamped prompts (fresh machine)"

info "\$ pogo install"
OUT_C="$(in_sandbox "$SB_C" "$PORT_C" "$BIN_DIR/pogo" install)"
printf '%s\n' "$OUT_C" | sed 's/^/    | /'
kill_daemon_on "$PORT_C"

expect_contains "C1 fresh install resolves coordinator to 'ringmaster'" "$OUT_C" "pogo agent start ringmaster"
expect_absent   "C2 fresh install never names 'mayor'"                  "$OUT_C" "mayor"

PROSE_C="$(in_sandbox "$SB_C" "$PORT_C" "$BIN_DIR/pogo" agent prompt show ringmaster)"
expect_contains "C3 fresh prose names the coordinator 'ringmaster'"     "$PROSE_C" "You are the ringmaster —"
expect_contains "C4 fresh prose names the worker 'pogocat'"             "$PROSE_C" "spawn pogocats (disposable worker agents)"

# --- frozen worker identifiers, observed on the live binary under the NEW defaults

# The coordinator prompt FILE is always mayor.md; only the agent NAME it starts
# under follows [agents] coordinator (mechanism vs policy, prompt.go ListPrompts).
LS_C="$(ls "$SB_C/state/agents")"
expect_contains "C5 coordinator prompt file stays mayor.md"             "$LS_C" "mayor.md"
expect_absent   "C6 no ringmaster.md — the filename does not follow the name" "$LS_C" "ringmaster.md"

LS_TMPL_C="$(ls "$SB_C/state/agents/templates")"
expect_contains "C7 worker template stays templates/polecat.md"         "$LS_TMPL_C" "polecat.md"
expect_absent   "C8 no templates/pogocat.md"                            "$LS_TMPL_C" "pogocat.md"

# The template lookup KEY is frozen at "polecat" even though the prose it renders
# now says "pogocat" — the whole point of the display/identifier split.
TMPL_C="$(in_sandbox "$SB_C" "$PORT_C" "$BIN_DIR/pogo" agent prompt show polecat)"
expect_contains "C9 template key 'polecat' still resolves"              "$TMPL_C" "You are an ephemeral pogocat"
if in_sandbox "$SB_C" "$PORT_C" "$BIN_DIR/pogo" agent prompt show pogocat >/dev/null 2>&1; then
    fail "C10 template key 'pogocat' resolves — the display name reached the lookup key"
else
    pass "C10 template key 'pogocat' does not resolve — display name stayed out of the key"
fi

# The branch prefix the shipped build-pr template instructs workers to push.
BUILDPR_C="$(in_sandbox "$SB_C" "$PORT_C" "$BIN_DIR/pogo" agent prompt show polecat-build-pr)"
expect_contains "C11 branch prefix stays polecat- under the flip"       "$BUILDPR_C" "git push origin polecat-"
expect_absent   "C12 no pogocat- branch prefix"                        "$BUILDPR_C" "origin pogocat-"

HELP_C="$(in_sandbox "$SB_C" "$PORT_C" "$BIN_DIR/pogo" agent --help)"
expect_contains "C13 spawn subcommand stays \`spawn-polecat\`"          "$HELP_C" "spawn-polecat"
expect_absent   "C14 no \`spawn-pogocat\` subcommand"                   "$HELP_C" "spawn-pogocat"

# --- Phase D: in-process frozen-identifier guard ---------------------------
#
# The remaining five load-bearing identifiers (gitgc.BranchPrefix, the polecats
# dir, TypePolecat, the cat- event-actor prefix, POGO_ROLE) are in-process
# values with no CLI surface. The repo's round-trip guard (mg-d582) renames the
# worker to "pogocat" and asserts each stays frozen; run it here so the gate
# covers all of them.

say "Phase D — frozen worker identifiers (BranchPrefix, polecats dir, TypePolecat, cat- actor, POGO_ROLE)"
info "\$ go test ./internal/agent -run TestWorkerRenameFreezesIdentifiers -count=1 -v"
if OUT_D="$(cd "$REPO_ROOT" && go test ./internal/agent -run TestWorkerRenameFreezesIdentifiers -count=1 -v 2>&1)"; then
    printf '%s\n' "$OUT_D" | grep -E '^(=== RUN|--- PASS|--- FAIL|ok|FAIL|PASS)' | sed 's/^/    | /'
    pass "D1 all five frozen worker identifiers unchanged under a display rename"
else
    printf '%s\n' "$OUT_D" | sed 's/^/    | /'
    fail "D1 frozen-identifier guard failed"
fi

# ---------------------------------------------------------------------------

say "Result"
if [ "$FAILURES" -eq 0 ]; then
    printf '  \033[32mUPGRADE SMOKE PASSED\033[0m — v0.3.0 installs upgrade to this build keeping mayor/polecat.\n\n'
    exit 0
fi
printf '  \033[31mUPGRADE SMOKE FAILED\033[0m — %d assertion(s) failed. DO NOT PUBLISH.\n\n' "$FAILURES"
exit 1
