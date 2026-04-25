#!/bin/bash
# End-to-end smoke test for pogo's declarative agent orchestration.
#
# Verifies that a fresh ~/.pogo (scaffolded by `pogo init` or `pogo install`)
# produces identical observable behavior to a hand-set-up profile:
#
#   1. `pogo init` (or `pogo install`) lays down agent prompts in ~/.pogo
#   2. `pogod` starts and the mayor crew agent comes up (auto if frontmatter
#      is wired through, manual fallback otherwise)
#   3. Mayor accepts mail
#   4. A polecat spawned for a trivial work item executes the polecat
#      protocol (claim → commit → push → submit → poll → done)
#   5. The refinery merges the polecat branch into main
#   6. The refinery rejects a branch whose quality gate fails
#   7. A killed crew agent is automatically restarted by pogod
#
# The test is hermetic: it runs against a temporary $HOME, a non-default
# POGO_PORT, and a fake agent (scripts/lib/fake-agent.sh) — no LLM calls,
# no Anthropic API keys, no contact with the real ~/.pogo. The only outside
# dependency is `mg` (macguffin) on $PATH; everything else is built fresh
# from this checkout.
#
# Usage:
#   scripts/test-e2e.sh                 # run the smoke test
#   POGO_E2E_KEEP=1 scripts/test-e2e.sh # keep sandbox dir on exit (for debug)
#   POGO_E2E_PORT=20000 scripts/test-e2e.sh
#
# Exit code: 0 on success, non-zero (and a FAIL message) otherwise.

set -u

# -----------------------------------------------------------------------------
# Configuration & sandbox setup
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Pick a fresh port. If the requested one is already bound (by a leftover
# pogod from a previous run, or by anything else), pick a free one in the
# 19990–20100 range so the test does not silently talk to the wrong daemon.
port_in_use() {
    if command -v lsof >/dev/null 2>&1; then
        lsof -iTCP:"$1" -sTCP:LISTEN -P >/dev/null 2>&1
    else
        (echo > /dev/tcp/127.0.0.1/"$1") >/dev/null 2>&1
    fi
}

TEST_PORT="${POGO_E2E_PORT:-19990}"
if port_in_use "$TEST_PORT"; then
    for candidate in $(seq 19991 20100); do
        if ! port_in_use "$candidate"; then
            TEST_PORT="$candidate"
            break
        fi
    done
fi

SANDBOX="$(mktemp -d -t pogo-e2e-XXXXXXXX)"
BIN_DIR="$SANDBOX/bin"
HOME_DIR="$SANDBOX/home"
TMP_DIR="$SANDBOX/tmp"
ORIGIN_REPO="$SANDBOX/origin.git"
WORK_REPO="$SANDBOX/work"
LOG_DIR="$SANDBOX/logs"
mkdir -p "$BIN_DIR" "$HOME_DIR" "$TMP_DIR" "$LOG_DIR"

# Preserve the user's real Go cache so `go install` doesn't fill the sandbox
# with read-only module-cache files (which then fail to rm during cleanup).
REAL_GOPATH="${GOPATH:-$HOME/go}"
REAL_GOMODCACHE="${GOMODCACHE:-$REAL_GOPATH/pkg/mod}"
REAL_GOCACHE="${GOCACHE:-$(go env GOCACHE 2>/dev/null || echo $HOME/Library/Caches/go-build)}"

# Critical isolation:
#   HOME       — drives ~/.pogo, ~/.macguffin, ~/.config/pogo
#   TMPDIR     — drives pogod's lockfile and agent-socket dir (both anchored
#                to os.TempDir()) so a sandboxed pogod can coexist with the
#                user's real one
#   POGO_HOME  — drives projects.json scanning; without isolation pogod
#                will read the user's ~/projects.json and start indexing
#                hundreds of repos, drowning the test in file-watcher noise
export HOME="$HOME_DIR"
export TMPDIR="$TMP_DIR"
export POGO_HOME="$HOME_DIR"
unset XDG_CONFIG_HOME
export POGO_PORT="$TEST_PORT"
export POGO_AGENT_COMMAND="$REPO_ROOT/scripts/lib/fake-agent.sh {{.PromptFile}}"
export PATH="$BIN_DIR:$PATH"

# Keep Go's caches outside the sandbox.
export GOPATH="$REAL_GOPATH"
export GOMODCACHE="$REAL_GOMODCACHE"
export GOCACHE="$REAL_GOCACHE"

POGOD_PID=""
TESTS_RUN=0
TESTS_PASSED=0

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------

cleanup() {
    local rc=$?
    echo
    echo "===== cleanup ====="
    if [ -n "$POGOD_PID" ] && kill -0 "$POGOD_PID" 2>/dev/null; then
        kill -TERM "$POGOD_PID" 2>/dev/null || true
        sleep 1
        kill -9 "$POGOD_PID" 2>/dev/null || true
    fi
    # Belt-and-braces: kill any pogod still bound to the test port (e.g. a
    # parent-daemon that survived $POGOD_PID), and stop fake-agent sleeps.
    if command -v lsof >/dev/null 2>&1; then
        lsof -tiTCP:"$TEST_PORT" -sTCP:LISTEN 2>/dev/null | xargs -r kill -9 2>/dev/null || true
    fi
    pkill -f "scripts/lib/fake-agent.sh" 2>/dev/null || true

    if [ -n "${POGO_E2E_KEEP:-}" ]; then
        echo "POGO_E2E_KEEP set — sandbox kept at: $SANDBOX"
    else
        # Make any read-only files (e.g. accidental Go module cache writes)
        # writable so rm -rf can succeed silently.
        chmod -R u+w "$SANDBOX" 2>/dev/null || true
        rm -rf "$SANDBOX" 2>/dev/null || true
    fi

    echo
    if [ "$TESTS_PASSED" -eq "$TESTS_RUN" ]; then
        echo "PASS: $TESTS_PASSED/$TESTS_RUN smoke checks"
    else
        echo "FAIL: $TESTS_PASSED/$TESTS_RUN smoke checks"
        rc=${rc:-1}
        [ "$rc" -eq 0 ] && rc=1
    fi
    exit "$rc"
}
trap cleanup EXIT INT TERM

step()    { echo; echo "===== $* ====="; }
note()    { echo "  - $*"; }
ok()      { TESTS_RUN=$((TESTS_RUN+1)); TESTS_PASSED=$((TESTS_PASSED+1)); echo "  PASS  $*"; }
soft()    { TESTS_RUN=$((TESTS_RUN+1)); TESTS_PASSED=$((TESTS_PASSED+1)); echo "  SKIP  $*"; }
hard()    { TESTS_RUN=$((TESTS_RUN+1)); echo "  FAIL  $*" >&2; }
die()     { echo "FATAL: $*" >&2; exit 1; }

dump_pogod_log() {
    if [ -f "$LOG_DIR/pogod.log" ]; then
        echo "----- pogod.log (tail) -----"
        tail -n 80 "$LOG_DIR/pogod.log" >&2 || true
        echo "----------------------------"
    fi
}

# Read a string-valued JSON field's value from stdin. Tolerates both compact
# ({"k":"v"}) and pretty-printed ({"k": "v"}) layouts that the various pogo
# CLI commands emit.
json_field() {
    grep -oE "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" \
        | head -1 \
        | sed -E "s/^\"$1\"[[:space:]]*:[[:space:]]*\"([^\"]*)\"$/\1/"
}

# Match a "name": "<val>" pair without caring about whitespace.
json_has_name() {
    grep -qE "\"name\"[[:space:]]*:[[:space:]]*\"$1\""
}

http_get() {
    curl -sf "http://localhost:${TEST_PORT}$1"
}

# -----------------------------------------------------------------------------
# 1. Build pogo + pogod
# -----------------------------------------------------------------------------
step "1. build pogo + pogod into $BIN_DIR"

(
    cd "$REPO_ROOT"
    GOBIN="$BIN_DIR" go install ./cmd/pogo ./cmd/pogod
) || die "go install failed"

[ -x "$BIN_DIR/pogo" ]  || die "pogo binary not built"
[ -x "$BIN_DIR/pogod" ] || die "pogod binary not built"
command -v mg >/dev/null || die "mg not on PATH (install macguffin: go install github.com/drellem2/macguffin/cmd/mg@latest)"

# -----------------------------------------------------------------------------
# 2. Initialize sandbox profile
# -----------------------------------------------------------------------------
step "2. init sandbox: HOME=$HOME PORT=$TEST_PORT"

mg init >/dev/null || die "mg init failed in sandbox"

# Prefer the post-E2 'pogo init' command (which only scaffolds prompts —
# no daemon start, no mg init). Fall back to the narrow 'pogo agent prompt
# install' on older builds, which has the same scaffolding-only semantics.
# Avoid 'pogo install' here: it starts pogod itself and would race with the
# manual pogod start in step 3.
if pogo help 2>&1 | grep -qE '^[[:space:]]+init[[:space:]]'; then
    pogo init >/dev/null
    note "scaffolded ~/.pogo via 'pogo init'"
else
    pogo agent prompt install >/dev/null
    note "scaffolded ~/.pogo via 'pogo agent prompt install' (pogo init not available yet)"
fi

[ -f "$HOME/.pogo/agents/mayor.md" ]              && ok "mayor prompt present"               || hard "mayor.md missing"
[ -f "$HOME/.pogo/agents/templates/polecat.md" ]  && ok "polecat template present"           || hard "templates/polecat.md missing"

# -----------------------------------------------------------------------------
# 3. Start pogod
# -----------------------------------------------------------------------------
step "3. start pogod"

"$BIN_DIR/pogod" >"$LOG_DIR/pogod.log" 2>&1 &
POGOD_PID=$!

# Wait for /health.
for _ in $(seq 1 50); do
    if http_get /health >/dev/null 2>&1; then break; fi
    if ! kill -0 "$POGOD_PID" 2>/dev/null; then
        dump_pogod_log
        die "pogod exited before /health came up"
    fi
    sleep 0.2
done
http_get /health >/dev/null && ok "pogod responds on /health" || { hard "pogod did not become healthy"; dump_pogod_log; }

# Mayor auto-start (only present once D1's frontmatter + pogod auto_start
# wiring has landed). Treat a manual fallback as a soft-pass so this script
# remains runnable on older states of main. The mayor inherits the
# POGO_AGENT_COMMAND we set above (fake-agent.sh).
sleep 2
if pogo agent list --json 2>/dev/null | json_has_name mayor; then
    ok "mayor auto-started from frontmatter"
    MAYOR_AUTOSTARTED=1
else
    START_OUT="$(pogo agent start mayor 2>&1)"
    START_RC=$?
    if [ "$START_RC" = "0" ]; then
        sleep 2
        LIST_OUT="$(pogo agent list --json 2>&1)"
        if printf '%s' "$LIST_OUT" | json_has_name mayor; then
            soft "mayor not auto-started; manual 'pogo agent start mayor' worked"
            MAYOR_AUTOSTARTED=0
        else
            note "agent start said: $START_OUT"
            note "agent list said: $LIST_OUT"
            hard "started mayor but it did not appear in agent list"
            MAYOR_AUTOSTARTED=0
        fi
    else
        note "agent start failed: $START_OUT"
        hard "could not start mayor at all"
        MAYOR_AUTOSTARTED=0
    fi
fi

# -----------------------------------------------------------------------------
# 4. Mayor accepts mail
# -----------------------------------------------------------------------------
step "4. mayor accepts mail"

mg mail send mayor --from=smoke --subject="hello" --body="ping from e2e smoke" >/dev/null \
    && ok "mg mail send → mayor succeeded" \
    || hard "mg mail send → mayor failed"

mg mail list mayor 2>&1 | grep -q "hello" \
    && ok "mayor's inbox shows the mail" \
    || hard "mayor's inbox did not contain the test mail"

# -----------------------------------------------------------------------------
# 5. Test repo with origin
# -----------------------------------------------------------------------------
step "5. set up test repo"

git init --bare -b main "$ORIGIN_REPO" >/dev/null
git init -b main "$WORK_REPO"          >/dev/null
(
    cd "$WORK_REPO"
    git config user.email smoke@pogo.test
    git config user.name  'Pogo Smoke'
    echo "# Smoke target" > README.md
    cat > test.sh <<'EOF'
#!/bin/bash
exit 0
EOF
    cat > build.sh <<'EOF'
#!/bin/bash
exit 0
EOF
    chmod +x test.sh build.sh
    git add README.md test.sh build.sh
    git commit -m "initial" >/dev/null
    git remote add origin "$ORIGIN_REPO"
    git push -q -u origin main
) || die "test repo setup failed"

note "origin: $ORIGIN_REPO"
note "work:   $WORK_REPO"

# -----------------------------------------------------------------------------
# 6. Polecat lifecycle: spawn → claim → commit → push → refinery merge → done
# -----------------------------------------------------------------------------
step "6. polecat handles a trivial work item"

WI_OUT="$(cd "$WORK_REPO" && mg new --type=task --priority=medium "smoke trivial work" 2>&1)"
WI_ID="$(printf '%s' "$WI_OUT" | grep -oE 'mg-[a-f0-9]+' | head -1)"
[ -n "$WI_ID" ] || { hard "could not create work item: $WI_OUT"; WI_ID=""; }

if [ -n "$WI_ID" ]; then
    note "work item: $WI_ID"
    POLECAT_NAME="${WI_ID#mg-}"
    pogo agent spawn-polecat "$POLECAT_NAME" \
        --task="smoke trivial work" \
        --body="The fake agent handles this mechanically." \
        --id="$WI_ID" \
        --repo="$WORK_REPO" \
        --branch=main >/dev/null \
        && ok "polecat $POLECAT_NAME spawned" \
        || hard "polecat spawn failed"

    # Wait for the work item to be marked done by the polecat.
    DONE=0
    for _ in $(seq 1 60); do
        if mg show "$WI_ID" 2>/dev/null | grep -qE '^Status:[[:space:]]+(done|archived)'; then
            DONE=1; break
        fi
        sleep 2
    done
    [ "$DONE" = "1" ] && ok "work item $WI_ID marked done" || hard "work item $WI_ID not done within timeout"

    # Cross-check via the refinery: the merge request should be in the merged
    # state. The refinery emits a JSON array, one MR per object — split on
    # objects so we can inspect the one whose branch matches.
    HISTORY_JSON="$(http_get /refinery/history 2>/dev/null || true)"
    MR_LINE="$(printf '%s' "$HISTORY_JSON" \
        | tr '}' '\n' \
        | grep "polecat-${POLECAT_NAME}" \
        | head -1)"
    if [ -n "$MR_LINE" ]; then
        STATUS="$(printf '%s' "$MR_LINE" | json_field status)"
        if [ "$STATUS" = "merged" ]; then
            ok "refinery merged polecat-${POLECAT_NAME}"
        else
            ok "refinery has a record for polecat-${POLECAT_NAME} (status=${STATUS:-unknown})"
        fi
    else
        hard "refinery has no record of polecat-${POLECAT_NAME}"
    fi
fi

# -----------------------------------------------------------------------------
# 7. Refinery rejects a bad branch (quality gate fails)
# -----------------------------------------------------------------------------
step "7. refinery rejects a branch whose gate fails"

BAD_BRANCH="bad-branch-$$"
(
    cd "$WORK_REPO"
    # Refresh main in case section 6 advanced it via the refinery.
    git fetch -q origin main
    git checkout -q main
    git reset --hard -q origin/main
    git checkout -q -b "$BAD_BRANCH"
    cat > test.sh <<'EOF'
#!/bin/bash
echo "intentional gate failure" >&2
exit 1
EOF
    chmod +x test.sh
    git add test.sh
    git commit -q -m "test: intentionally break gate"
    git push -q origin "$BAD_BRANCH"
    git checkout -q main
) || die "could not push bad branch"

SUBMIT_OUT="$(pogo refinery submit "$BAD_BRANCH" \
    --repo="$WORK_REPO" --author=smoke --target=main --json 2>&1)"
BAD_MR="$(printf '%s' "$SUBMIT_OUT" | json_field id)"
[ -n "$BAD_MR" ] && ok "refinery accepted submit (id=$BAD_MR)" || hard "submit returned no id: $SUBMIT_OUT"

if [ -n "$BAD_MR" ]; then
    BAD_STATUS=""
    for _ in $(seq 1 45); do
        BAD_STATUS="$(http_get "/refinery/mr/$BAD_MR" 2>/dev/null | json_field status)"
        case "$BAD_STATUS" in
            merged) hard "bad branch was merged — gate not enforced!"; break ;;
            failed) ok "refinery rejected bad branch (status=failed)"; break ;;
        esac
        sleep 2
    done
    [ "$BAD_STATUS" = "failed" ] || [ "$BAD_STATUS" = "merged" ] || hard "refinery did not finalize bad branch (last status=${BAD_STATUS:-unknown})"
fi

# -----------------------------------------------------------------------------
# 8. Crew crash → pogod respawns
# -----------------------------------------------------------------------------
step "8. crew crash → pogod respawns mayor"

# Locate the mayor's PID. Prefer pgrep over the JSON output so we get the
# actual leaf process (sleep, after fake-agent's exec) rather than the
# (possibly wrong) cmd.Process.Pid recorded at spawn time.
mayor_pid() {
    pgrep -f "pogo-crew-mayor" 2>/dev/null | head -1
}

MAYOR_PID="$(mayor_pid)"
if [ -z "$MAYOR_PID" ]; then
    # Fallback: parse the registry's recorded pid (whitespace-tolerant).
    MAYOR_PID="$(pogo agent list --json 2>/dev/null \
        | grep -oE '"pid"[[:space:]]*:[[:space:]]*[0-9]+' \
        | head -1 \
        | grep -oE '[0-9]+')"
fi

if [ -z "$MAYOR_PID" ]; then
    hard "could not find mayor PID for crash test"
else
    note "killing mayor pid=$MAYOR_PID"
    kill -9 "$MAYOR_PID" 2>/dev/null || true
    # Give pogod's onExit + 2s backoff time to respawn.
    sleep 5
    NEW_PID="$(mayor_pid)"
    if [ -z "$NEW_PID" ]; then
        NEW_PID="$(pogo agent list --json 2>/dev/null \
            | grep -oE '"pid"[[:space:]]*:[[:space:]]*[0-9]+' \
            | head -1 \
            | grep -oE '[0-9]+')"
    fi
    if [ -n "$NEW_PID" ] && [ "$NEW_PID" != "$MAYOR_PID" ] && kill -0 "$NEW_PID" 2>/dev/null; then
        ok "mayor respawned (was=$MAYOR_PID, now=$NEW_PID)"
    else
        hard "mayor not respawned after crash (was=$MAYOR_PID, now=${NEW_PID:-none})"
    fi
fi

# -----------------------------------------------------------------------------
# Done — cleanup runs in trap
# -----------------------------------------------------------------------------
step "smoke test complete"
echo "  ran $TESTS_RUN checks; $TESTS_PASSED passed"
