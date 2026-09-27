#!/bin/bash
# mg-store-backup.sh — commit the macguffin store and push it to a repo that
# lives OUTSIDE it (mg-b01d). Run hourly by com.pogo.mgbackup.
#
# WHY OUTSIDE. `mg init --git` puts .git INSIDE ~/.macguffin, and the 2026-09-27
# 09:41Z loss was an `rm -rf ~/.macguffin` — a repo inside the directory dies
# with it. The copy that survives that is a bare repo elsewhere on disk
# (default ~/backups/macguffin.git), registered as the remote "backup".
#
# WHAT mg ITSELF DOES (read from ~/dev/macguffin, internal/workspace/git.go):
# nothing, automatically. `mg init --git` only runs `git init`; the ONLY commit
# path is the manual `mg snapshot`, which is `git add -A` + commit. No mg
# command commits as a side effect. So this job does the same `git add -A`
# (mail/, work/, events.jsonl, log/ — everything, there is no .gitignore) and
# pushes.
#
# APPEND-ONLY. It never forces, rebases, resets or amends. A push the backup
# refuses (non-fast-forward: someone rewrote the store's history) is a FAILURE
# and alerts; it is not "fixed" by overwriting the backup.
#
# WHAT GIT DOES NOT KEEP: empty directories. A maildir's empty new/cur/tmp and
# an empty work/<state>/ are absent from every commit. scripts/mg-store-restore.sh
# recreates them; a bare `git clone` alone is not a complete restore.
#
# FAILURE DETECTION. Every step's exit status is checked; the first non-zero
# one ends the run through fail(), which (1) logs a FAIL line, (2) writes
# $STATE_DIR/FAILED with the reason, (3) mails $ALERT_TO. The run then exits 1,
# which launchd records as `last exit code`. Mail goes out on the ok->fail
# transition and then at most once per $REALERT_SECONDS while it stays failed,
# so an hourly failure is not 24 mails a day; the fail->ok recovery is mailed
# too. If mail itself cannot be sent — the likely case when the store is the
# thing that is gone — the FAILED file and the log are the record, and a macOS
# notification is attempted.
#
# It does NOT create the store. A missing ~/.macguffin is reported, never
# re-initialised: an empty store pushed over a good backup is not possible
# anyway (a fresh repo's history is unrelated, so the push is refused), but a
# backup job has no business deciding the store should exist.

set -u

STORE="${MG_BACKUP_STORE:-$HOME/.macguffin}"
REMOTE_DIR="${MG_BACKUP_REMOTE_DIR:-$HOME/backups/macguffin.git}"
REMOTE_NAME="${MG_BACKUP_REMOTE_NAME:-backup}"
STATE_DIR="${MG_BACKUP_STATE_DIR:-$HOME/.pogo/mgbackup}"
ALERT_TO="${MG_BACKUP_ALERT_TO:-mayor}"
ALERT_FROM="${MG_BACKUP_ALERT_FROM:-mgbackup}"
REALERT_SECONDS="${MG_BACKUP_REALERT_SECONDS:-86400}"
MG="${MG_BACKUP_MG:-mg}"
# owner/name of a PRIVATE GitHub repo to push to as well; empty disables it.
GITHUB_REPO="${MG_BACKUP_GITHUB_REPO:-}"
GITHUB_REMOTE_NAME="${MG_BACKUP_GITHUB_REMOTE_NAME:-github}"
GH="${MG_BACKUP_GH:-gh}"
GH_TOKEN_FROM_ZSH="${MG_BACKUP_GH_TOKEN_FROM_ZSH:-1}"
NOTIFY="${MG_BACKUP_NOTIFY:-1}"

LOCK_DIR="$STATE_DIR/run.lock.d"
FAILED_FILE="$STATE_DIR/FAILED"
LAST_ALERT_FILE="$STATE_DIR/last_alert"
LAST_OK_FILE="$STATE_DIR/last_ok"

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "$(ts) mgbackup: $*"; }

# git with a fixed identity for the commits this job makes, so they are
# distinguishable from `mg snapshot` and from anyone's manual commit.
# Env, not -c: GIT_AUTHOR_* in the caller's environment would beat -c.
sgit() {
	GIT_AUTHOR_NAME=mgbackup GIT_AUTHOR_EMAIL=mgbackup@localhost \
		GIT_COMMITTER_NAME=mgbackup GIT_COMMITTER_EMAIL=mgbackup@localhost \
		git -C "$STORE" "$@"
}

send_alert() { # subject, body
	local subject="$1" body="$2"
	if [ -d "$STORE/mail/$ALERT_TO" ] &&
		"$MG" --root "$STORE" mail send "$ALERT_TO" --from="$ALERT_FROM" --subject="$subject" --body="$body" >/dev/null 2>&1; then
		log "alerted $ALERT_TO: $subject"
		date +%s >"$LAST_ALERT_FILE"
		return 0
	fi
	log "ALERT NOT DELIVERED to $ALERT_TO (no mailbox at $STORE/mail/$ALERT_TO, or mg mail send failed): $subject"
	if [ "$NOTIFY" = 1 ] && command -v osascript >/dev/null 2>&1; then
		osascript -e "display notification \"$subject\" with title \"mg store backup\"" >/dev/null 2>&1 || true
	fi
	return 1
}

fail() {
	local reason="$*"
	log "FAIL: $reason"
	local was_failed=0 now last=0
	[ -f "$FAILED_FILE" ] && was_failed=1
	printf '%s %s\n' "$(ts)" "$reason" >"$FAILED_FILE"
	now=$(date +%s)
	[ -f "$LAST_ALERT_FILE" ] && last=$(cat "$LAST_ALERT_FILE" 2>/dev/null || echo 0)
	if [ "$was_failed" = 0 ] || [ $((now - ${last:-0})) -ge "$REALERT_SECONDS" ]; then
		send_alert "mg store backup FAILED: $reason" "com.pogo.mgbackup could not back up $STORE to $REMOTE_DIR.

Reason: $reason

Log: ~/Library/Logs/pogo/mgbackup.log
State: $FAILED_FILE (removed by the next successful run)
Last success: $(cat "$LAST_OK_FILE" 2>/dev/null || echo never)

The job exits non-zero on the first failing step, so launchd's last exit code is 1 too:
  launchctl print gui/\$(id -u)/com.pogo.mgbackup | grep 'last exit'
While it stays failed this mail repeats at most once per ${REALERT_SECONDS}s."
	else
		log "still failing; last alert was $((now - last))s ago, not re-alerting"
	fi
	rmdir "$LOCK_DIR" 2>/dev/null
	exit 1
}

mkdir -p "$STATE_DIR" || {
	log "FAIL: cannot create $STATE_DIR"
	exit 1
}

# One run at a time. A lock older than 30 minutes is a crashed run's.
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
	if [ -n "$(find "$LOCK_DIR" -maxdepth 0 -mmin +30 2>/dev/null)" ]; then
		log "removing stale lock $LOCK_DIR"
		rmdir "$LOCK_DIR" && mkdir "$LOCK_DIR" || fail "cannot take lock $LOCK_DIR"
	else
		log "another run holds $LOCK_DIR; skipping"
		exit 0
	fi
fi

# --- preconditions --------------------------------------------------------
[ -d "$STORE" ] || fail "store $STORE does not exist (not re-creating it; restore with scripts/mg-store-restore.sh)"
[ -d "$STORE/.git" ] || fail "store $STORE is not a git repo (run: mg init --git)"

if [ ! -d "$REMOTE_DIR" ]; then
	# First run, or the backup was deleted. Either way an empty bare repo that
	# receives the full history is the correct state; say so loudly in the log.
	log "backup repo $REMOTE_DIR missing; creating it (bare)"
	mkdir -p "$(dirname "$REMOTE_DIR")" && git init --bare --quiet "$REMOTE_DIR" ||
		fail "cannot create bare repo $REMOTE_DIR"
fi
[ "$(git -C "$REMOTE_DIR" rev-parse --is-bare-repository 2>/dev/null)" = true ] ||
	fail "$REMOTE_DIR exists but is not a bare git repo"

url=$(git -C "$STORE" config --get "remote.$REMOTE_NAME.url")
if [ -z "$url" ]; then
	git -C "$STORE" remote add "$REMOTE_NAME" "$REMOTE_DIR" || fail "cannot add remote $REMOTE_NAME"
	log "added remote $REMOTE_NAME -> $REMOTE_DIR"
elif [ "$url" != "$REMOTE_DIR" ]; then
	fail "remote $REMOTE_NAME points at $url, expected $REMOTE_DIR (not changing it)"
fi

branch=$(git -C "$STORE" symbolic-ref --short HEAD 2>/dev/null) ||
	fail "store HEAD is detached; refusing to guess a branch"

# --- commit ---------------------------------------------------------------
# Retry once: a concurrent `mg snapshot` or manual commit holds index.lock for
# a moment, and that is not worth an alert.
commit_once() {
	sgit add -A || return 1
	if sgit diff --cached --quiet; then
		echo nothing
		return 0
	fi
	sgit commit --quiet -m "backup snapshot $(ts) (com.pogo.mgbackup)" || return 1
	echo committed
}
# commit_once's last line is its verdict; anything else is git's error text.
result=$(commit_once 2>&1 | tail -1)
case "$result" in
nothing | committed) ;;
*)
	log "commit attempt failed ($result); retrying in 5s"
	sleep 5
	result=$(commit_once 2>&1 | tail -1)
	case "$result" in
	nothing | committed) ;;
	*) fail "commit failed: $result" ;;
	esac
	;;
esac
log "commit: $result"

head=$(git -C "$STORE" rev-parse --verify --quiet HEAD) ||
	fail "store has no commits and nothing to commit — an empty store is not backed up"

# --- push -----------------------------------------------------------------
out=$(git -C "$STORE" push --quiet "$REMOTE_NAME" "refs/heads/$branch:refs/heads/$branch" 2>&1) ||
	fail "push to $REMOTE_NAME refused: $(echo "$out" | tr '\n' ' ' | cut -c1-300)"

# Verify rather than trust the push's exit status: the backup must now hold
# exactly the store's HEAD.
remote_head=$(git -C "$REMOTE_DIR" rev-parse --verify --quiet "refs/heads/$branch") ||
	fail "backup has no refs/heads/$branch after push"
[ "$remote_head" = "$head" ] || fail "backup $branch is $remote_head, store HEAD is $head"
# A bare repo's HEAD defaults to init.defaultBranch (often master); if it does
# not name the pushed branch, `git clone` of the backup checks out NOTHING.
if [ "$(git -C "$REMOTE_DIR" symbolic-ref HEAD 2>/dev/null)" != "refs/heads/$branch" ]; then
	git -C "$REMOTE_DIR" symbolic-ref HEAD "refs/heads/$branch" || fail "cannot point $REMOTE_DIR HEAD at $branch"
	log "pointed $REMOTE_DIR HEAD at $branch"
fi
git -C "$REMOTE_DIR" fsck --connectivity-only --no-dangling --no-progress >/dev/null 2>&1 ||
	fail "git fsck failed on $REMOTE_DIR"

count=$(git -C "$REMOTE_DIR" rev-list --count "refs/heads/$branch")
log "OK: $branch @ ${head:0:12} in $REMOTE_DIR ($count commits)"

# --- off-machine remote (optional) ----------------------------------------
# The local backup is on the same disk as the store; only an off-machine copy
# survives losing the disk. Daniel approved a PRIVATE GitHub repo (2026-09-27
# 16:33Z). This step runs AFTER the local push, so a network outage never
# costs the local copy; a failure here still fails the run and alerts.
if [ -n "$GITHUB_REPO" ]; then
	gh_url="https://github.com/$GITHUB_REPO.git"
	# The raw configured URL: `remote get-url` applies url.*.insteadOf rewrites.
	url=$(git -C "$STORE" config --get "remote.$GITHUB_REMOTE_NAME.url")
	if [ -z "$url" ]; then
		git -C "$STORE" remote add "$GITHUB_REMOTE_NAME" "$gh_url" || fail "cannot add remote $GITHUB_REMOTE_NAME"
		log "added remote $GITHUB_REMOTE_NAME -> $gh_url"
	elif [ "$url" != "$gh_url" ]; then
		fail "remote $GITHUB_REMOTE_NAME points at $url, expected $gh_url (not changing it; local backup OK)"
	fi

	# launchd gives no GH_TOKEN; it lives in ~/.zshenv, which every zsh reads.
	# Imported into this process's env only — never logged.
	if [ -z "${GH_TOKEN:-}" ] && [ "$GH_TOKEN_FROM_ZSH" = 1 ] && command -v zsh >/dev/null 2>&1; then
		GH_TOKEN=$(zsh -c 'print -r -- "${GH_TOKEN:-}"' 2>/dev/null)
		export GH_TOKEN
	fi

	# Checked on EVERY run, not once: the store holds every agent's mail, and a
	# repo flipped to public must stop receiving it the next hour.
	vis=$("$GH" repo view "$GITHUB_REPO" --json visibility -q .visibility 2>&1) ||
		fail "cannot read visibility of $GITHUB_REPO ($(echo "$vis" | tr '\n' ' ' | cut -c1-200)); not pushing there; local backup OK"
	[ "$vis" = PRIVATE ] ||
		fail "REFUSING to push to $GITHUB_REPO: visibility is '$vis', not PRIVATE; local backup OK"

	out=$(git -C "$STORE" push --quiet "$GITHUB_REMOTE_NAME" "refs/heads/$branch:refs/heads/$branch" 2>&1) ||
		fail "push to $GITHUB_REPO refused: $(echo "$out" | tr '\n' ' ' | cut -c1-300); local backup OK"
	gh_head=$(git -C "$STORE" ls-remote "$GITHUB_REMOTE_NAME" "refs/heads/$branch" 2>/dev/null | cut -f1)
	[ "$gh_head" = "$head" ] || fail "$GITHUB_REPO $branch is '${gh_head:-missing}', store HEAD is $head; local backup OK"
	log "OK: $branch @ ${head:0:12} in $GITHUB_REPO (PRIVATE)"
fi

ts >"$LAST_OK_FILE"

if [ -f "$FAILED_FILE" ]; then
	prev=$(cat "$FAILED_FILE")
	rm -f "$FAILED_FILE"
	send_alert "mg store backup recovered" "com.pogo.mgbackup succeeded again: $branch @ ${head:0:12}, $count commits in $REMOTE_DIR${GITHUB_REPO:+ and $GITHUB_REPO}.
Previous failure: $prev" || true
fi

rmdir "$LOCK_DIR" 2>/dev/null
exit 0
