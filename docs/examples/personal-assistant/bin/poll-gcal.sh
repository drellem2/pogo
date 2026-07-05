#!/usr/bin/env bash
# poll-gcal.sh — polls Google Calendar via gcalcli (insanum/gcalcli) and mails
# the pa agent about new, changed, or removed upcoming events (mg-a909).
#
# Pipeline: gcalcli agenda --tsv --details id  ->  seen.json dedupe  ->  mg mail send pa
#
# Cloned from the poll-hey.sh skeleton (mg-8066). Same design rules:
# standalone script (never a pogo subcommand), pull-verify-consume delivery
# (mg-f73e — a zero exit from `mg mail send` is not proof of durable delivery;
# we stat the maildir file before recording the event as seen).
#
# Pre-auth behavior: gcalcli AUTO-LAUNCHES an interactive OAuth flow when no
# token exists, which would hang under launchd — so this poller never invokes
# gcalcli until the token file exists on disk. Until then it logs ONCE and
# idles quietly — deploy first, auth later (one interactive `gcalcli init`),
# and ingest goes live the next cycle. No crash-looping under launchd.
#
# Dedupe key is the event id (from `--details id`; recurring-event instances
# get distinct ids), mapped to a fingerprint of the event's fields — so a
# rescheduled/renamed event re-surfaces as "updated", and an event that
# disappears from the window ahead of its start time surfaces as "removed".
# First authenticated cycle primes seen.json silently (one summary mail, not
# one mail per pre-existing event). seen.json is small text and is not pruned
# beyond dropping past events.
#
# Environment variables:
#   POLL_INTERVAL   — seconds between polls (default: 300)
#   STATE_DIR       — state directory (default: ~/.pogo/pa/gcalfeed)
#   MAILDIR_ROOT    — agent maildir root (default: ~/.macguffin/mail)
#   TARGET_AGENT    — agent to mail (default: pa)
#   FROM_NAME       — mail sender name (default: gcal-feed)
#   GCALCLI_BIN     — gcalcli binary (default: gcalcli; override for fixtures)
#   GCAL_TOKEN_FILE — OAuth token path gating polling (default: macOS
#                     "~/Library/Application Support/gcalcli/oauth"; on Linux
#                     use ~/.local/share/gcalcli/oauth). Existence-only check.
#   WINDOW_DAYS     — days ahead to watch (default: 7)
#   FAIL_ALERT_AFTER— consecutive fetch failures before alerting human
#                     (default: 5; covers token revocation/expiry)
#   ONESHOT         — "true" to run a single poll cycle and exit (test mode)
#
# Secrets: this script never reads or prints credentials. It checks only that
# the gcalcli oauth token file EXISTS — do not cat, log, or mail that file.

set -euo pipefail

POLL_INTERVAL="${POLL_INTERVAL:-300}"
STATE_DIR="${STATE_DIR:-$HOME/.pogo/pa/gcalfeed}"
STATE_FILE="$STATE_DIR/seen.json"
MAILDIR_ROOT="${MAILDIR_ROOT:-$HOME/.macguffin/mail}"
TARGET_AGENT="${TARGET_AGENT:-pa}"
FROM_NAME="${FROM_NAME:-gcal-feed}"
GCALCLI_BIN="${GCALCLI_BIN:-gcalcli}"
GCAL_TOKEN_FILE="${GCAL_TOKEN_FILE:-$HOME/Library/Application Support/gcalcli/oauth}"
WINDOW_DAYS="${WINDOW_DAYS:-7}"
FAIL_ALERT_AFTER="${FAIL_ALERT_AFTER:-5}"
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
FETCH_FAILURES=0
FETCH_ALERTED=false

# True (exit 0) when a gcalcli OAuth token exists on disk. Existence check
# only — never reads the file. Also honors the legacy ~/.gcalcli_oauth path.
gcal_authenticated() {
  [ -s "$GCAL_TOKEN_FILE" ] && return 0
  [ -s "$HOME/.gcalcli_oauth" ] && return 0
  return 1
}

# End of the watch window as YYYY-MM-DD (BSD date first, GNU fallback).
window_end() {
  date -v "+${WINDOW_DAYS}d" +%Y-%m-%d 2>/dev/null \
    || date -d "+${WINDOW_DAYS} days" +%Y-%m-%d
}

# Verify that the mail file `mg mail send` claims to have delivered exists on
# disk before we record the event as seen (pull-verify-consume, mg-f73e).
# Success line shape:  Delivered: gcal-feed → pa/new/1783118420080496000.46719.6000
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

# Mail human about a persistent delivery failure — once per event key per
# poller lifetime, so a broken pipeline notifies once instead of every cycle.
notify_delivery_failure() {
  local key="$1" reason="$2"
  case " $FAILURE_NOTIFIED " in
    *" $key "*) return 0 ;;
  esac
  FAILURE_NOTIFIED="$FAILURE_NOTIFIED $key"
  if ! mg mail send human \
    --from="$FROM_NAME" \
    --subject="gcal-feed delivery FAILED for event $key" \
    --body="poll-gcal.sh could not deliver event $key to $TARGET_AGENT's maildir; it stays un-seen and will be retried every cycle. Reason: $reason" \
    >/dev/null 2>&1; then
    log "WARN: failure notification to human also failed for $key"
  fi
}

# Track consecutive gcalcli fetch failures; alert human once per outage
# (token revocation, password change, API errors — the poller has no way to
# self-heal these, so the operator must re-run `gcalcli init`).
note_fetch_failure() {
  FETCH_FAILURES=$((FETCH_FAILURES + 1))
  if [ "$FETCH_FAILURES" -ge "$FAIL_ALERT_AFTER" ] && [ "$FETCH_ALERTED" = "false" ]; then
    FETCH_ALERTED=true
    mg mail send human \
      --from="$FROM_NAME" \
      --subject="gcal-feed: calendar fetch failing ($FETCH_FAILURES consecutive)" \
      --body="poll-gcal.sh has failed $FETCH_FAILURES consecutive gcalcli fetches. The OAuth token may be revoked or expired (Google revokes on password change; Testing-status consent screens expire refresh tokens after 7 days). Fix: run 'gcalcli init' interactively once, and confirm the consent screen is published In production. Details in $STATE_DIR/poller.log." \
      >/dev/null 2>&1 || log "WARN: fetch-failure alert to human failed"
  fi
}

note_fetch_success() {
  if [ "$FETCH_ALERTED" = "true" ]; then
    log "gcalcli fetch recovered after $FETCH_FAILURES failure(s)"
  fi
  FETCH_FAILURES=0
  FETCH_ALERTED=false
}

# Deliver one event mail; returns 0 iff verified on disk.
deliver() {
  local key="$1" subject="$2" body="$3" send_out
  if send_out="$(mg mail send "$TARGET_AGENT" \
    --from="$FROM_NAME" \
    --subject="$subject" \
    --body="$body" 2>&1)"; then
    if ! verify_delivered "$send_out"; then
      log "ERROR: mg mail send reported success for $key but no mail file found under $MAILDIR_ROOT (output: $send_out) — will retry next cycle"
      notify_delivery_failure "$key" "mg exited 0 but the delivered mail file could not be verified on disk"
      return 1
    fi
    return 0
  fi
  log "ERROR: mg mail send failed for $key (output: $send_out) — will retry next cycle"
  notify_delivery_failure "$key" "mg mail send exited nonzero"
  return 1
}

poll_once() {
  if ! command -v "$GCALCLI_BIN" >/dev/null 2>&1 && [ ! -x "$GCALCLI_BIN" ]; then
    if [ "$AUTH_LOGGED" = "false" ]; then
      log "gcalcli not found ($GCALCLI_BIN) — install with 'brew install gcalcli'; polling idles until then"
      AUTH_LOGGED=true
    fi
    return 0
  fi
  if ! gcal_authenticated; then
    if [ "$AUTH_LOGGED" = "false" ]; then
      log "gcalcli not authenticated (no token at $GCAL_TOKEN_FILE) — run 'gcalcli init' once; polling idles quietly until then"
      AUTH_LOGGED=true
    fi
    return 0
  fi
  if [ "$AUTH_LOGGED" = "true" ]; then
    log "gcalcli token present — resuming ingest"
    AUTH_LOGGED=false
  fi

  local today wend listing
  today="$(date +%Y-%m-%d)"
  wend="$(window_end)"

  # Fetch from local midnight (not "now") so events earlier today stay in the
  # listing all day — otherwise every event would false-positive as "removed"
  # the cycle after it starts.
  if ! listing="$("$GCALCLI_BIN" --nocolor agenda "$today" "$wend" \
      --tsv --military --details id --details calendar --details location \
      2>"$STATE_DIR/gcalcli.err" </dev/null)"; then
    log "ERROR: gcalcli agenda failed: $(head -c 300 "$STATE_DIR/gcalcli.err" 2>/dev/null | tr '\n' ' ')"
    note_fetch_failure
    return 0
  fi
  note_fetch_success

  # Normalize by header row (gcalcli 4.4+ prints TSV fieldnames) so column
  # order/extras don't matter: id, start_date, start_time, end_date,
  # end_time, title, location, calendar.
  local normalized awk_rc=0
  normalized="$(printf '%s\n' "$listing" | awk -F'\t' '
    NR==1 {
      for (i = 1; i <= NF; i++) col[$i] = i
      if (!("id" in col) || !("title" in col) || !("start_date" in col)) exit 2
      next
    }
    !NF { next }
    {
      printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", \
        $(col["id"]), $(col["start_date"]), \
        ("start_time" in col ? $(col["start_time"]) : ""), \
        ("end_date" in col ? $(col["end_date"]) : ""), \
        ("end_time" in col ? $(col["end_time"]) : ""), \
        $(col["title"]), \
        ("location" in col ? $(col["location"]) : ""), \
        ("calendar" in col ? $(col["calendar"]) : "")
    }')" || awk_rc=$?
  if [ "$awk_rc" -eq 2 ]; then
    log "ERROR: unexpected gcalcli TSV header (no id/title/start_date column) — check gcalcli version"
    note_fetch_failure
    return 0
  fi

  SEEN="$(cat "$STATE_FILE")"
  local priming=false
  if [ "$SEEN" = "{}" ]; then
    priming=true
  fi

  local count=0 cur_ids='[]'
  local id sdate stime edate etime title loc cal
  while IFS=$'\t' read -r id sdate stime edate etime title loc cal; do
    [ -z "$id" ] && continue
    cur_ids="$(printf '%s' "$cur_ids" | jq -c --arg id "$id" '. + [$id]')"

    local fp prev kind
    fp="$(printf '%s|%s|%s|%s|%s|%s|%s' "$sdate" "$stime" "$edate" "$etime" "$title" "$loc" "$cal" | cksum | awk '{print $1}')"
    prev="$(printf '%s' "$SEEN" | jq -r --arg k "$id" '.[$k].fp // ""')"

    if [ "$prev" = "$fp" ]; then
      continue
    fi
    if [ "$priming" = "true" ]; then
      # First authenticated cycle: record without mailing (summary sent below).
      SEEN="$(printf '%s' "$SEEN" | jq -c --arg k "$id" --arg fp "$fp" --arg s "$sdate" '.[$k] = {fp: $fp, start: $s}')"
      count=$((count + 1))
      continue
    fi
    if [ -n "$prev" ]; then
      kind="updated"
    else
      kind="new"
    fi

    local when prefix subject body
    when="$sdate${stime:+ $stime}"
    prefix=""
    if [ "$kind" = "updated" ]; then
      prefix="updated: "
    fi
    subject="$(printf '[gcal] %s%s — %s' "$prefix" "$when" "${title:-(no title)}" | tr '\n\t' '  ')"
    body="Google Calendar $kind event.

When: $when${etime:+ – ${edate:+$edate }$etime}
Title: ${title:-(no title)}
Location: ${loc:-(none)}
Calendar: ${cal:-(default)}
Event id: $id

Feed is read-only; query live with: gcalcli agenda today (or 'gcalcli agenda $today $wend')."

    if deliver "$id" "$subject" "$body"; then
      SEEN="$(printf '%s' "$SEEN" | jq -c --arg k "$id" --arg fp "$fp" --arg s "$sdate" '.[$k] = {fp: $fp, start: $s}')"
      count=$((count + 1))
      log "Delivered $kind event $id to $TARGET_AGENT"
    fi
  done <<< "$normalized"

  # Removals: seen events whose start is still inside [today, window end] but
  # which vanished from the listing were cancelled or moved. Past events are
  # pruned silently.
  local removed_keys key start
  removed_keys="$(printf '%s' "$SEEN" | jq -r --argjson cur "$cur_ids" --arg t "$today" --arg w "$wend" \
    '[to_entries[] | select(.value.start >= $t and .value.start <= $w and ((.key | IN($cur[])) | not)) | .key] | .[]')"
  while IFS= read -r key; do
    [ -z "$key" ] && continue
    if [ "$priming" = "true" ]; then
      SEEN="$(printf '%s' "$SEEN" | jq -c --arg k "$key" 'del(.[$k])')"
      continue
    fi
    start="$(printf '%s' "$SEEN" | jq -r --arg k "$key" '.[$k].start // "?"')"
    if deliver "$key" "[gcal] removed: event on $start" \
      "Google Calendar event $key (start $start) is no longer on the calendar within the ${WINDOW_DAYS}-day window — cancelled or rescheduled. If rescheduled, the new time arrives as a separate 'new' event mail."; then
      SEEN="$(printf '%s' "$SEEN" | jq -c --arg k "$key" 'del(.[$k])')"
      log "Delivered removal notice for $key to $TARGET_AGENT"
    fi
  done <<< "$removed_keys"

  # Prune past events silently.
  SEEN="$(printf '%s' "$SEEN" | jq -c --arg t "$today" 'with_entries(select(.value.start >= $t))')"

  if [ "$priming" = "true" ]; then
    if deliver "prime" "[gcal] calendar feed is live — $count event(s) in the next $WINDOW_DAYS days" \
      "The Google Calendar feed (poll-gcal.sh) completed its first authenticated cycle. $count upcoming event(s) were primed into the dedupe state without individual mails. From now on you get one mail per new/updated/removed event. Query the live agenda read-only with: gcalcli agenda today (never gcalcli add/edit/delete — calendar writes are Phase 3)."; then
      log "Primed $count event(s); feed live"
    else
      # Leave state empty so priming (and the summary) retries next cycle.
      SEEN='{}'
    fi
  elif [ "$count" -gt 0 ]; then
    log "Delivered $count event change(s)"
  fi

  # Atomic state write — a crash mid-write must not corrupt seen.json.
  printf '%s\n' "$SEEN" > "$STATE_FILE.tmp"
  mv "$STATE_FILE.tmp" "$STATE_FILE"
}

log "Starting gcal-feed poller (interval: ${POLL_INTERVAL}s, window: ${WINDOW_DAYS}d, target: $TARGET_AGENT, state: $STATE_DIR)"

if [ "$ONESHOT" = "true" ]; then
  poll_once
  exit 0
fi

while true; do
  poll_once || true
  sleep "$POLL_INTERVAL"
done
