# enth-stamp.sh — the enth half of the build stamp, sourced by build.sh and by
# scripts/enth_stamp_test.sh (which exercises these functions directly, so the
# table it re-runs does not cost a full pogo build per row). bash 3.2-safe.
#
# THE ENTH STAMP (docs/design/enth-vcs.md §10, mg-c8568)
#
# A build from an enth checkout has no git revision of its own. Worse, it can
# borrow one: Go walks UP from the module to the first .git, so an enth checkout
# under ~/.pogo (a git repo) is stamped with ~/.pogo's HEAD — measured, §10's
# table, re-run by scripts/enth_stamp_test.sh. `git rev-parse HEAD` walks up the
# same way, so "git rev-parse failed" is NOT the test for an enth build: in the
# one place pogo puts workspaces it succeeds, with the outer repo's answer.
#
# stamp_vcs DIR prints "enth" when DIR holds an enth checkout's state file
# (.enth/checkout, written by enth #9's `enth checkout`) AND git's top level for
# DIR is not DIR itself; "git" otherwise. A real git repo (or worktree) rooted
# at DIR wins even if it holds a .enth/checkout, so every git build is decided
# exactly as before.
stamp_vcs() {
  local dir="${1:-.}" top here
  if [ ! -f "$dir/.enth/checkout" ]; then
    echo git
    return 0
  fi
  here="$(cd "$dir" && pwd -P)"
  top="$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null || true)"
  if [ -n "$top" ] && [ "$(cd "$top" && pwd -P)" = "$here" ]; then
    echo git
    return 0
  fi
  echo enth
}

# enth_base DIR prints the base Change id recorded in DIR/.enth/checkout's
# first line ({"format":"enth-checkout","version":1,"base":"<id>",...}), or
# nothing for a state file this script cannot read. Read from the file, not
# from `enth status`, so the id does not depend on which enth is on PATH.
enth_base() {
  local first
  first="$(head -1 "${1:-.}/.enth/checkout" 2>/dev/null || true)"
  case "$first" in
    *'"format":"enth-checkout"'*'"version":1,'*) ;;
    *) return 0 ;;
  esac
  printf '%s\n' "$first" | sed -n 's/.*"base":"\([0-9a-f][0-9a-f]*\)".*/\1/p'
}

# enth_ldflags DIR: the enth half of version_ldflags.
#   Commit = the base Change id   Build = its first 12 hex   VCS = enth
#   Dirty  = `enth status DIR` reports ops > 0
# NO Branch: .enth/checkout records the base, origin and selection but not the
# ref that was checked out (enth #9 B), so there is nothing to read one from
# and the binary reports branch=unknown rather than a guess.
# Dirty when it cannot be measured is "true": an unchecked tree is not claimed
# clean. The reason goes to stderr so the build log says which it was.
enth_ldflags() {
  local dir="${1:-.}"
  local pkg="github.com/drellem2/pogo/internal/version"
  local base status ops dirty
  base="$(enth_base "$dir")"
  if [ -z "$base" ]; then
    echo "build.sh: $dir/.enth/checkout has no readable base Change id (not format enth-checkout version 1)" >&2
    echo ""
    return 0
  fi
  # 2>&1: a refusal (outside_selection, a corrupt state file) is one JSON line
  # on stderr, and it is the reason worth printing.
  status="$(enth status "$dir" 2>&1 | head -1 || true)"
  ops="$(printf '%s\n' "$status" | sed -n 's/.*"type":"status".*"ops":\([0-9][0-9]*\).*/\1/p')"
  if [ -z "$ops" ]; then
    echo "build.sh: \`enth status $dir\` gave no op count — stamping Dirty=true. It said: ${status:-<nothing; is enth on PATH?>}" >&2
    dirty=true
  elif [ "$ops" -gt 0 ]; then
    dirty=true
  else
    dirty=false
  fi
  printf -- '-X %s.Commit=%s -X %s.Build=%s -X %s.Dirty=%s -X %s.VCS=enth' \
    "$pkg" "$base" "$pkg" "${base:0:12}" "$pkg" "$dirty" "$pkg"
}
