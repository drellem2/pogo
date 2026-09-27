#!/bin/bash
# mg-store-restore.sh — rebuild a macguffin store from the backup that
# com.pogo.mgbackup keeps outside it (mg-b01d).
#
#   scripts/mg-store-restore.sh <dest> [backup-repo]
#
# <dest> must not exist. backup-repo defaults to ~/backups/macguffin.git; the
# off-machine copy restores with
#   scripts/mg-store-restore.sh <dest> https://github.com/drellem2/macguffin-store.git
# It never touches ~/.macguffin: to put a restore live, stop pogod, move the
# damaged store aside, and restore to ~/.macguffin as <dest>.
#
# A bare `git clone` is NOT a complete restore: git does not record empty
# directories, so every maildir's empty new/, cur/ and tmp/ and every empty
# work/<state>/ are missing from the clone. `mg init` recreates the work/ tree;
# the maildir triplets are recreated here for every mail/<box> the backup holds.
# The clone keeps "backup" as a remote name so the backup job can resume
# pushing from it once it is the live store.

set -eu

dest="${1:?usage: mg-store-restore.sh <dest> [backup-repo]}"
repo="${2:-$HOME/backups/macguffin.git}"
MG="${MG_RESTORE_MG:-mg}"

[ -e "$dest" ] && {
	echo "refusing: $dest already exists" >&2
	exit 2
}
# A local path must be the bare backup; anything else is a URL for git clone
# (the off-machine copy: https://github.com/drellem2/macguffin-store.git).
if [ -e "$repo" ] && [ "$(git -C "$repo" rev-parse --is-bare-repository 2>/dev/null)" != true ]; then
	echo "not a bare git repo: $repo" >&2
	exit 2
fi

git clone --quiet --origin backup "$repo" "$dest"
# A remote whose HEAD names a branch it does not have (a bare repo created with
# init.defaultBranch=master, then pushed `main`) clones to an EMPTY checkout
# with no error. Check out its only branch instead; refuse to guess among many.
if ! git -C "$dest" rev-parse --verify --quiet HEAD >/dev/null; then
	branches=$(git -C "$dest" for-each-ref --format='%(refname:strip=3)' refs/remotes/backup/ | grep -v '^HEAD$')
	if [ "$(printf '%s\n' "$branches" | grep -c .)" != 1 ]; then
		echo "clone of $repo checked out nothing and has branches: ${branches:-none}; pick one with git -C $dest checkout" >&2
		exit 3
	fi
	git -C "$dest" checkout --quiet -b "$branches" "backup/$branches"
fi
"$MG" --root "$dest" init >/dev/null

boxes=0
for box in "$dest"/mail/*/; do
	[ -d "$box" ] || continue
	mkdir -p "$box/new" "$box/cur" "$box/tmp"
	boxes=$((boxes + 1))
done

echo "restored $(git -C "$dest" rev-parse --short HEAD) ($(git -C "$dest" rev-list --count HEAD) commits) from $repo to $dest; $boxes mailboxes"
