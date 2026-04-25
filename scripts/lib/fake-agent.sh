#!/bin/bash
# Fake agent runtime used by scripts/test-e2e.sh.
#
# Replaces 'claude' so the e2e smoke test can exercise pogod's spawn / crew
# / polecat / refinery pipeline without making LLM calls. Crew agents just
# stay alive; polecat agents read their rendered prompt and execute the
# polecat protocol mechanically (claim → commit → push → submit → poll →
# done) so we can verify the merge loop end-to-end.
#
# Invoked the same way as 'claude': the prompt file path is passed via the
# POGO_AGENT_PROMPT env var (also as a positional arg by the default command
# template). pogod also injects POGO_AGENT_NAME, POGO_AGENT_TYPE, POGO_ROLE.

set -u

PROMPT="${POGO_AGENT_PROMPT:-}"
TYPE="${POGO_AGENT_TYPE:-crew}"
PORT="${POGO_PORT:-10000}"
NAME="${POGO_AGENT_NAME:-fake}"

log() { echo "[fake-agent $NAME/$TYPE] $*" >&2; }

# Crew agents (mayor, etc.) just stay alive — pogod will restart us if we
# exit, which is exactly what we want for the crash/restart leg of the test.
if [ "$TYPE" != "polecat" ]; then
    log "crew agent running; sleeping until killed"
    exec sleep 86400
fi

# Polecat: extract task metadata from the rendered prompt file.
if [ -z "$PROMPT" ] || [ ! -f "$PROMPT" ]; then
    log "no prompt file ($PROMPT); idling"
    exec sleep 86400
fi

# The polecat template emits these as plain markdown — no backticks, no
# trailing fences — so a simple "header line, take everything after the
# colon" extraction is enough.
extract_field() {
    grep -m1 "^\*\*$1:\*\*" "$PROMPT" \
        | sed -E "s/^\\*\\*$1:\\*\\*[[:space:]]+//;s/[[:space:]]+$//"
}
extract_id()       { grep -oE 'mg-[a-f0-9]{4,}' "$PROMPT" | head -1; }
extract_repo()     { extract_field "Repository"; }
extract_worktree() { extract_field "Working Directory"; }

ID="$(extract_id)"
REPO="$(extract_repo)"
WORKTREE="$(extract_worktree)"

log "id=$ID repo=$REPO worktree=$WORKTREE"

if [ -z "$ID" ] || [ -z "$WORKTREE" ] || [ ! -d "$WORKTREE" ]; then
    log "missing protocol inputs; idling"
    exec sleep 86400
fi

cd "$WORKTREE" || { log "cd $WORKTREE failed"; exec sleep 86400; }

# 1. Claim the work item (best-effort — the mayor may have pre-claimed for us).
mg claim "$ID" 2>&1 | sed 's/^/  mg claim: /' >&2 || true

# 2. Make a trivial change so the branch has commits to merge.
marker="smoke-${ID}.txt"
printf 'smoke %s %s\n' "$ID" "$(date -u +%FT%TZ)" > "$marker"
git add "$marker"
git \
    -c user.email=smoke@pogo.test \
    -c user.name='Pogo Smoke Test' \
    commit -m "test: smoke marker (${ID})" >/dev/null 2>&1 || {
    log "commit failed"; exec sleep 86400;
}

# 3. Push the polecat branch.
BRANCH="polecat-${POGO_AGENT_NAME}"
if ! git push -q origin "$BRANCH" 2>&1 | sed 's/^/  git push: /' >&2; then
    log "push failed"; exec sleep 86400;
fi

# 4. Submit to refinery.
SUBMIT_OUT="$(pogo refinery submit "$BRANCH" \
    --repo="$REPO" --author="$ID" --target=main --json 2>&1)"
log "submit output: $SUBMIT_OUT"
MR_ID="$(printf '%s' "$SUBMIT_OUT" \
    | grep -oE '"id"[[:space:]]*:[[:space:]]*"[^"]+"' \
    | head -1 \
    | sed -E 's/.*"([^"]+)"$/\1/')"

if [ -z "$MR_ID" ]; then
    log "could not parse MR id"
    exec sleep 86400
fi

# 5. Poll until merged or failed.
STATUS=""
for _ in $(seq 1 90); do
    STATUS="$(curl -sf "http://localhost:${PORT}/refinery/mr/${MR_ID}" \
        | grep -oE '"status"[[:space:]]*:[[:space:]]*"[^"]+"' \
        | head -1 \
        | sed -E 's/.*"([^"]+)"$/\1/')"
    case "$STATUS" in
        merged|failed) break ;;
    esac
    sleep 2
done

log "final refinery status: ${STATUS:-unknown}"

# 6. On merge, mark the work item done. On failure, mail the mayor.
if [ "$STATUS" = "merged" ]; then
    mg done "$ID" --result="{\"branch\":\"${BRANCH}\"}" 2>&1 \
        | sed 's/^/  mg done: /' >&2 || true
else
    mg mail send mayor --from="$ID" \
        --subject="merge failed for ${ID}" \
        --body="refinery rejected ${BRANCH} (status=${STATUS:-unknown})" \
        2>&1 | sed 's/^/  mg mail: /' >&2 || true
fi

# 7. Per the polecat contract, do not exit on our own.
log "protocol complete; idling"
exec sleep 86400
