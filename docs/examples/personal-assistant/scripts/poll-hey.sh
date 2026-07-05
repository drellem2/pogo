#!/usr/bin/env bash
# poll-hey.sh — polls HEY mailboxes via the official hey CLI (basecamp/hey-cli)
# and mails the pa agent for new or updated postings (mg-8066).
#
# Pipeline: hey box <box> --json  ->  seen.json dedupe  ->  mg mail send pa
#
# Cloned from the pogo-reminders skeleton (poll-reminders.sh). Same design
# rules: standalone script (never a pogo subcommand), pull-verify-consume
# delivery (mg-f73e — a zero exit from `mg mail send` is not proof of durable
# delivery; we stat the maildir file before recording the posting as seen).
#
# Pre-auth behavior: `hey auth status --json` reports authenticated=false
# (exit 0) until the operator runs `hey auth login` interactively. In that
# state the poller logs ONCE and idles quietly — deploy first, auth later,
# and ingest goes live the moment auth exists. No crash-looping under launchd.
#
# Dedupe key is "<box>:<posting id>" mapped to the posting's updated_at, so a
# thread that receives new mail (updated_at changes) is re-surfaced as
# "updated". seen.json is small text and is not pruned (same as reminders).
#
# Environment variables:
#   POLL_INTERVAL   — seconds between polls (default: 120)
#   STATE_DIR       — state directory (default: ~/.pogo/pa/heyfeed)
#   MAILDIR_ROOT    — agent maildir root (default: ~/.macguffin/mail)
#   TARGET_AGENT    — agent to mail (default: pa)
#   FROM_NAME       — mail sender name (default: hey-feed)
#   HEY_BIN         — hey CLI binary (default: hey; override for fixtures)
#   HEY_BOXES       — space-separated box names to poll (default: "imbox")
#   HEY_LIMIT       — max postings fetched per box per cycle (default: 30)
#   ONESHOT         — "true" to run a single poll cycle and exit (test mode)
#
# Secrets: this script never reads or prints credentials. hey-cli keeps its
# OAuth tokens in the system keyring (file fallback ~/.config/hey-cli/
# credentials.json) — do not cat, log, or mail that file.

set -euo pipefail

POLL_INTERVAL="${POLL_INTERVAL:-120}"
STATE_DIR="${STATE_DIR:-$HOME/.pogo/pa/heyfeed}"
STATE_FILE="$STATE_DIR/seen.json"
MAILDIR_ROOT="${MAILDIR_ROOT:-$HOME/.macguffin/mail}"
TARGET_AGENT="${TARGET_AGENT:-pa}"
FROM_NAME="${FROM_NAME:-hey-feed}"
HEY_BIN="${HEY_BIN:-hey}"
HEY_BOXES="${HEY_BOXES:-imbox}"
HEY_LIMIT="${HEY_LIMIT:-30}"
ONESHOT="${ONESHOT:-false}"

mkdir -p "$STATE_DIR"
if [ ! -f "$STATE_FILE" ]; then
  echo '{}' > "$STATE_FILE"
fi

log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*" >&2
}

# Log-once latches (plain strings, bash-3.2 compatible). Reset where noted.
AUTH_LOGGED=false
FAILURE_NOTIFIED=""

# True (exit 0) when the hey CLI is installed and authenticated. Never prints
# token material — only the boolean out of the status envelope.
hey_authenticated() {
  local out
  if ! out="$("$HEY_BIN" auth status --json 2>/dev/null)"; then
    return 1
  fi
  [ "$(printf '%s' "$out" | jq -r '.data.authenticated // false' 2>/dev/null)" = "true" ]
}

# Verify that the mail file `mg mail send` claims to have delivered exists on
# disk before we record the posting as seen (pull-verify-consume, mg-f73e).
# Success line shape:  Delivered: hey-feed → pa/new/1783118420080496000.46719.6000
verify_delivered() {
  local send_out="$1" rel base agent_dir
  rel="$(printf '%s\n' "$send_out" | sed -n 's/^Delivered: .* → //p' | tail -n 1)"
  if [ -z "$rel" ]; then
    return 1
  fi
  if [ -s "$MAILDIR_ROOT/$rel" ]; then
    return 0
  fi
  base="$(basename "$rel")"
  agent_dir="$(dirname "$(dirname "$rel")")"
  if compgen -G "$MAILDIR_ROOT/$agent_dir/cur/$base*" > /dev/null; then
    return 0
  fi
  return 1
}

# Mail human about a persistent delivery failure — once per posting key per
# poller lifetime, so a broken pipeline notifies once instead of every cycle.
notify_delivery_failure() {
  local key="$1" reason="$2"
  case " $FAILURE_NOTIFIED " in
    *" $key "*) return 0 ;;
  esac
  FAILURE_NOTIFIED="$FAILURE_NOTIFIED $key"
  if ! mg mail send human \
    --from="$FROM_NAME" \
    --subject="hey-feed delivery FAILED for posting $key" \
    --body="poll-hey.sh could not deliver posting $key to $TARGET_AGENT's maildir; it stays un-seen and will be retried every cycle. Reason: $reason" \
    >/dev/null 2>&1; then
    log "WARN: failure notification to human also failed for $key"
  fi
}

# Poll one box. Reads/writes the SEEN variable (JSON object as a string).
poll_box() {
  local box="$1" listing postings count
  if ! listing="$("$HEY_BIN" box "$box" --json --limit "$HEY_LIMIT" 2>&1)"; then
    log "ERROR: hey box $box failed: $(printf '%s' "$listing" | head -c 300)"
    return 0
  fi
  if ! postings="$(printf '%s' "$listing" | jq -c '.data.postings // [] | .[]' 2>/dev/null)"; then
    log "ERROR: could not parse postings JSON for box $box"
    return 0
  fi
  count=0

  local posting
  while IFS= read -r posting; do
    [ -z "$posting" ] && continue

    local id updated key prev kind
    id="$(printf '%s' "$posting" | jq -r '.id')"
    updated="$(printf '%s' "$posting" | jq -r '.updated_at // .created_at // ""')"
    key="$box:$id"
    prev="$(printf '%s' "$SEEN" | jq -r --arg k "$key" '.[$k] // ""')"

    if [ "$prev" = "$updated" ] && [ -n "$updated" ]; then
      continue
    fi
    if [ -n "$prev" ]; then
      kind="updated"
    else
      kind="new"
    fi

    local subject body
    subject="$(printf '%s' "$posting" | jq -r --arg box "$box" --arg kind "$kind" \
      '"[hey/\($box)] \(if $kind == "updated" then "updated: " else "" end)\(.name // "(no subject)")"' 2>/dev/null | tr '\n\t' '  ')"
    body="$(printf '%s' "$posting" | jq -r --arg box "$box" --arg kind "$kind" '
      "HEY \($kind) posting in \($box).",
      "",
      "From: \(.creator.name // "?") <\(.creator.email_address // "?")>",
      "Subject: \(.name // "(no subject)")",
      "Received: \(.created_at // "?")   Updated: \(.updated_at // "?")",
      "Seen in HEY app: \(.seen // false)   Entries in thread: \(.visible_entry_count // 1)",
      "Thread: \(.app_url // "?")   (read with: hey threads <topic id>)",
      "",
      "Summary:",
      (.summary // "(none)")
    ')"

    local send_out
    if send_out="$(mg mail send "$TARGET_AGENT" \
      --from="$FROM_NAME" \
      --subject="$subject" \
      --body="$body" 2>&1)"; then
      if ! verify_delivered "$send_out"; then
        log "ERROR: mg mail send reported success for $key but no mail file found under $MAILDIR_ROOT (output: $send_out) — will retry next cycle"
        notify_delivery_failure "$key" "mg exited 0 but the delivered mail file could not be verified on disk"
        continue
      fi
      SEEN="$(printf '%s' "$SEEN" | jq -c --arg k "$key" --arg v "$updated" '.[$k] = $v')"
      count=$((count + 1))
      log "Delivered $kind posting $key to $TARGET_AGENT"
    else
      log "ERROR: mg mail send failed for $key (output: $send_out) — will retry next cycle"
      notify_delivery_failure "$key" "mg mail send exited nonzero"
      continue
    fi
  done <<< "$postings"

  if [ "$count" -gt 0 ]; then
    log "Box $box: delivered $count posting(s)"
  fi
}

poll_once() {
  if ! command -v "$HEY_BIN" >/dev/null 2>&1 && [ ! -x "$HEY_BIN" ]; then
    if [ "$AUTH_LOGGED" = "false" ]; then
      log "hey CLI not found ($HEY_BIN) — install basecamp/hey-cli; polling idles until then"
      AUTH_LOGGED=true
    fi
    return 0
  fi
  if ! hey_authenticated; then
    if [ "$AUTH_LOGGED" = "false" ]; then
      log "hey CLI not authenticated — run 'hey auth login' once; polling idles quietly until then"
      AUTH_LOGGED=true
    fi
    return 0
  fi
  if [ "$AUTH_LOGGED" = "true" ]; then
    log "hey CLI authenticated — resuming ingest"
    AUTH_LOGGED=false
  fi

  SEEN="$(cat "$STATE_FILE")"
  local box
  for box in $HEY_BOXES; do
    poll_box "$box"
  done

  # Atomic state write — a crash mid-write must not corrupt seen.json.
  printf '%s\n' "$SEEN" > "$STATE_FILE.tmp"
  mv "$STATE_FILE.tmp" "$STATE_FILE"
}

log "Starting hey-feed poller (interval: ${POLL_INTERVAL}s, boxes: $HEY_BOXES, target: $TARGET_AGENT, state: $STATE_DIR)"

if [ "$ONESHOT" = "true" ]; then
  poll_once
  exit 0
fi

while true; do
  poll_once || true
  sleep "$POLL_INTERVAL"
done
