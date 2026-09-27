#!/bin/bash
# Tests for scripts/mg-store-backup.sh and scripts/mg-store-restore.sh (mg-b01d).
#
# Runs entirely in a temp dir: a throwaway store, a throwaway backup repo, and a
# stub `mg` that records `mail send` instead of delivering it. HOME, POGO_HOME,
# XDG_CONFIG_HOME and MG_ROOT go through the packaged isolation, and the git
# global config is redirected, so nothing here can reach ~/.macguffin or ~/.pogo.

set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
BACKUP="$HERE/mg-store-backup.sh"
RESTORE="$HERE/mg-store-restore.sh"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create mgbackup
trap 'pogo_sandbox_down; exit' EXIT INT TERM HUP
pogo_sandbox_isolate
T="$POGO_SANDBOX_DIR/t"
mkdir -p "$T"

: >"$T/gitconfig"
export GIT_CONFIG_GLOBAL="$T/gitconfig" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

REAL_MG="$(command -v mg || true)"
cat >"$T/mg" <<EOF
#!/bin/bash
if [ "\$1" = --root ] && [ "\$3" = mail ] && [ "\$4" = send ]; then
	printf '%s\n' "\$*" >>"$T/mail.log"
	exit 0
fi
if [ "\$1" = --root ] && [ "\$3" = init ]; then
	# stand-in for mg init when the real mg is absent: the same dirs
	for d in work/available work/claimed work/done work/pending work/archive work/shelved agents mail log; do
		mkdir -p "\$2/\$d"
	done
	exit 0
fi
[ -n "$REAL_MG" ] && exec "$REAL_MG" "\$@"
echo "stub mg: unsupported: \$*" >&2
exit 2
EOF
chmod +x "$T/mg"

STORE="$T/store"
REMOTE="$T/backups/macguffin.git"
STATE="$T/state"
export MG_BACKUP_STORE="$STORE" MG_BACKUP_REMOTE_DIR="$REMOTE" MG_BACKUP_STATE_DIR="$STATE" \
	MG_BACKUP_MG="$T/mg" MG_BACKUP_NOTIFY=0 MG_RESTORE_MG="$T/mg"

pass=0
failn=0
ok() {
	echo "ok   - $1"
	pass=$((pass + 1))
}
bad() {
	echo "FAIL - $1"
	failn=$((failn + 1))
}
check() { # desc, command...
	local d="$1"
	shift
	if "$@"; then ok "$d"; else bad "$d"; fi
}
run() { bash "$BACKUP" >"$T/out" 2>&1; }
mails() { [ -f "$T/mail.log" ] && wc -l <"$T/mail.log" | tr -d ' ' || echo 0; }

# A store shaped like the live one: work items, a maildir with a message and
# EMPTY subdirs (which git cannot record).
mkdir -p "$STORE"/work/{available,claimed,done,pending,archive,shelved} "$STORE"/mail/mayor/{new,cur,tmp} \
	"$STORE"/mail/pb01d/{new,cur,tmp} "$STORE/log" "$STORE/agents"
printf -- '---\nid: mg-aaaa\n---\n' >"$STORE/work/available/mg-aaaa.md"
printf -- '---\nid: mg-cccc\n---\n' >"$STORE/work/claimed/mg-cccc.md.4242"
echo 'hello' >"$STORE/mail/mayor/new/1.2.3"
echo '{}' >"$STORE/mail/pb01d/.registration.json"
echo '{"e":1}' >"$STORE/events.jsonl"
git -C "$STORE" init --quiet -b main

# 1. First run on a store with no commits: creates the backup and pushes.
run
check "first run exits 0" [ $? -eq 0 ]
check "first run creates a bare repo" [ "$(git -C "$REMOTE" rev-parse --is-bare-repository)" = true ]
check "remote 'backup' points at it" [ "$(git -C "$STORE" remote get-url backup)" = "$REMOTE" ]
check "backup main == store HEAD" [ "$(git -C "$REMOTE" rev-parse main)" = "$(git -C "$STORE" rev-parse HEAD)" ]
check "mail/ is in the backup" git -C "$REMOTE" cat-file -e main:mail/mayor/new/1.2.3
check "commit is attributed to mgbackup" [ "$(git -C "$STORE" log -1 --format=%an)" = mgbackup ]
check "log says OK" grep -q ' OK: main @' "$T/out"
check "no alert on success" [ "$(mails)" = 0 ]

# 2. Nothing changed: no new commit, still OK.
run
check "idle run exits 0" [ $? -eq 0 ]
check "idle run says nothing" grep -q 'commit: nothing' "$T/out"
check "idle run adds no commit" [ "$(git -C "$REMOTE" rev-list --count main)" = 1 ]

# 3. A foreign commit (e.g. mg snapshot) plus uncommitted changes: both land, appended.
printf -- '---\nid: mg-bbbb\n---\n' >"$STORE/work/available/mg-bbbb.md"
git -C "$STORE" add -A && git -C "$STORE" commit --quiet -m "state snapshot"
echo 'more' >>"$STORE/events.jsonl"
run
check "append run exits 0" [ $? -eq 0 ]
check "backup has all 3 commits" [ "$(git -C "$REMOTE" rev-list --count main)" = 3 ]

# 4. Rewritten store history is REFUSED, not force-pushed, and alerts once.
git -C "$STORE" commit --quiet --amend -m "rewritten"
before="$(git -C "$REMOTE" rev-parse main)"
run
check "non-fast-forward exits 1" [ $? -eq 1 ]
check "backup untouched by a rewrite" [ "$(git -C "$REMOTE" rev-parse main)" = "$before" ]
check "FAILED file written" [ -f "$STATE/FAILED" ]
check "failure mailed to mayor" grep -q 'mail send mayor --from=mgbackup --subject=mg store backup FAILED' "$T/mail.log"
check "alert goes to the backed-up store, not mg's default root" grep -q "^--root $STORE mail send" "$T/mail.log"
n="$(mails)"
run
check "repeat failure exits 1" [ $? -eq 1 ]
check "repeat failure within REALERT does not re-mail" [ "$(mails)" = "$n" ]
check "repeat failure says so" grep -q 'not re-alerting' "$T/out"
# Human repair: put the backup's history back (no rewrite of the backup).
git -C "$STORE" reset --quiet --hard "$before"

# 5. Recovery clears FAILED and mails once.
run
check "recovery exits 0" [ $? -eq 0 ]
check "FAILED cleared" [ ! -f "$STATE/FAILED" ]
check "recovery mailed" grep -q 'subject=mg store backup recovered' "$T/mail.log"

# 6. Restore: a clone plus the empty dirs git cannot carry.
bash "$RESTORE" "$T/restored" "$REMOTE" >"$T/rout" 2>&1
check "restore exits 0" [ $? -eq 0 ]
check "restore brings back work items" [ -f "$T/restored/work/available/mg-bbbb.md" ]
check "restore brings back mail" [ -f "$T/restored/mail/mayor/new/1.2.3" ]
check "restore recreates empty maildir subdirs" [ -d "$T/restored/mail/pb01d/cur" ] && [ -d "$T/restored/mail/mayor/tmp" ]
check "restore recreates empty work states" [ -d "$T/restored/work/shelved" ]
check "restore content matches the store" diff -r -x .git "$STORE" "$T/restored"
bash "$RESTORE" "$T/restored" "$REMOTE" >/dev/null 2>&1
check "restore refuses an existing dest" [ $? -eq 2 ]
if [ -n "$REAL_MG" ]; then
	"$REAL_MG" --root "$T/restored" list >"$T/list" 2>&1
	check "real mg lists the restored available item" grep -q mg-bbbb "$T/list"
	check "real mg lists the restored claimed item" grep -q mg-cccc "$T/list"
fi

# 7. A missing store is reported, never re-created; alert cannot be mailed.
mv "$STORE" "$T/store.aside"
: >"$T/mail.log"
run
check "missing store exits 1" [ $? -eq 1 ]
check "missing store is not re-created" [ ! -e "$STORE" ]
check "undeliverable alert is logged" grep -q 'ALERT NOT DELIVERED' "$T/out"
check "FAILED names the missing store" grep -q 'does not exist' "$STATE/FAILED"
mv "$T/store.aside" "$STORE"

# 8. A remote pointing elsewhere is not silently rewritten.
git -C "$STORE" remote set-url backup "$T/elsewhere.git"
run
check "wrong remote URL exits 1" [ $? -eq 1 ]
check "wrong remote URL left alone" [ "$(git -C "$STORE" remote get-url backup)" = "$T/elsewhere.git" ]

git -C "$STORE" remote set-url backup "$REMOTE"
run
check "repaired remote URL recovers" [ $? -eq 0 ]

# 9. Off-machine remote: GitHub, redirected to a local bare repo with
# url.insteadOf, and a stub gh answering the visibility question.
GHREMOTE="$T/gh/drellem2/macguffin-store.git"
git init --quiet --bare "$GHREMOTE"
git config --file "$GIT_CONFIG_GLOBAL" "url.$T/gh/.insteadOf" "https://github.com/"
cat >"$T/gh-stub" <<EOF
#!/bin/bash
[ "\$1 \$2 \$3" = "repo view drellem2/macguffin-store" ] || { echo "stub gh: \$*" >&2; exit 2; }
cat "$T/visibility"
EOF
chmod +x "$T/gh-stub"
export MG_BACKUP_GITHUB_REPO=drellem2/macguffin-store MG_BACKUP_GH="$T/gh-stub" MG_BACKUP_GH_TOKEN_FROM_ZSH=0

echo PUBLIC >"$T/visibility"
echo 'gh-a' >>"$STORE/events.jsonl"
: >"$T/mail.log"
run
check "public GitHub repo exits 1" [ $? -eq 1 ]
check "public GitHub repo receives nothing" [ -z "$(git -C "$GHREMOTE" for-each-ref)" ]
check "public GitHub repo: local backup still pushed" [ "$(git -C "$REMOTE" rev-parse main)" = "$(git -C "$STORE" rev-parse HEAD)" ]
check "public GitHub repo: reason names the refusal" grep -q "REFUSING to push to drellem2/macguffin-store: visibility is 'PUBLIC'" "$STATE/FAILED"
check "public GitHub repo: alert mailed" grep -q 'backup FAILED' "$T/mail.log"

echo PRIVATE >"$T/visibility"
run
check "private GitHub repo exits 0" [ $? -eq 0 ]
check "remote 'github' keeps the github.com URL" [ "$(git -C "$STORE" config --get remote.github.url)" = https://github.com/drellem2/macguffin-store.git ]
check "GitHub main == store HEAD" [ "$(git -C "$GHREMOTE" rev-parse main)" = "$(git -C "$STORE" rev-parse HEAD)" ]
check "GitHub OK logged" grep -q 'in drellem2/macguffin-store (PRIVATE)' "$T/out"
check "recovery after GitHub failure mailed" grep -q 'backup recovered' "$T/mail.log"

bash "$RESTORE" "$T/restored-gh" https://github.com/drellem2/macguffin-store.git >/dev/null 2>&1
check "restore from the GitHub URL exits 0" [ $? -eq 0 ]
check "restore from the GitHub URL brings back mail" [ -f "$T/restored-gh/mail/mayor/new/1.2.3" ]
bash "$RESTORE" "$T/restored-x" "$STORE" >/dev/null 2>&1
check "restore refuses a local non-bare repo" [ $? -eq 2 ]

rm -f "$T/visibility"
run
check "unreadable visibility exits 1" [ $? -eq 1 ]
check "unreadable visibility is not a push" grep -q 'cannot read visibility' "$STATE/FAILED"

echo
echo "$pass passed, $failn failed"
[ "$failn" -eq 0 ]
