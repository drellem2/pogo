#!/usr/bin/env bash
# Controls on scripts/lib/go-trimpath.sh (drellem2/pogo#282): build.sh and
# test.sh run go with -trimpath, so polecat worktrees at different paths share
# build-cache entries.
#
#   (a) An unset/empty GOFLAGS becomes "-trimpath", exported to children.
#   (b) A GOFLAGS the caller set is kept, with -trimpath appended.
#   (c) -trimpath already present (bare or =true) is not added twice.
#   (d) POGO_NO_TRIMPATH=1 leaves GOFLAGS exactly as it was.
#   (e) build.sh and test.sh both source the library and call
#       go_trimpath_enable, after go_toolchain_resolve. Without this case,
#       deleting the call would leave (a)-(d) green and the gate untrimmed.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
LIB="$REPO_ROOT/scripts/lib/go-trimpath.sh"

# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create go-trimpath
pogo_sandbox_isolate
trap 'pogo_sandbox_down' EXIT INT TERM HUP

PASS=0
FAIL=0
pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

# child_goflags <env assignments...>: source the library in a clean bash, call
# it, and print GOFLAGS as a CHILD process sees it, so an unexported value
# would show up as empty.
child_goflags() {
    env -i PATH=/usr/bin:/bin "$@" /bin/bash --norc --noprofile -c '. "$1" && go_trimpath_enable && /bin/sh -c "printf %s \"\${GOFLAGS-<unset>}\""' _ "$LIB"
}

check() {
    local name="$1" want="$2"
    shift 2
    local got
    got="$(child_goflags "$@")"
    if [ "$got" = "$want" ]; then
        pass "$name"
    else
        fail "$name: want [$want], got [$got]"
    fi
}

check "(a) unset GOFLAGS becomes -trimpath, exported" "-trimpath"
check "(a) empty GOFLAGS becomes -trimpath" "-trimpath" GOFLAGS=
check "(b) caller's GOFLAGS kept, -trimpath appended" "-mod=mod -count=1 -trimpath" "GOFLAGS=-mod=mod -count=1"
check "(c) bare -trimpath not added twice" "-mod=mod -trimpath" "GOFLAGS=-mod=mod -trimpath"
check "(c) -trimpath=true not added twice" "-trimpath=true" "GOFLAGS=-trimpath=true"
check "(d) POGO_NO_TRIMPATH=1 leaves GOFLAGS unset" "<unset>" POGO_NO_TRIMPATH=1
check "(d) POGO_NO_TRIMPATH=1 leaves a caller's GOFLAGS untouched" "-mod=mod" POGO_NO_TRIMPATH=1 GOFLAGS=-mod=mod

# (e) The gate scripts call it, after go is resolved.
for script in build.sh test.sh; do
    f="$REPO_ROOT/$script"
    resolve_line="$(grep -n '^go_toolchain_resolve' "$f" | head -1 | cut -d: -f1)"
    source_line="$(grep -n '^\. ".*scripts/lib/go-trimpath\.sh"' "$f" | head -1 | cut -d: -f1)"
    enable_line="$(grep -n '^go_trimpath_enable$' "$f" | head -1 | cut -d: -f1)"
    if [ -z "$resolve_line" ]; then
        fail "(e) $script: no go_toolchain_resolve line found (the check below cannot be trusted)"
    elif [ -z "$source_line" ] || [ -z "$enable_line" ]; then
        fail "(e) $script does not source go-trimpath.sh and call go_trimpath_enable"
    elif [ "$source_line" -lt "$enable_line" ] && [ "$resolve_line" -lt "$enable_line" ]; then
        pass "(e) $script sources the library and calls go_trimpath_enable after go_toolchain_resolve"
    else
        fail "(e) $script: wrong order (resolve=$resolve_line source=$source_line enable=$enable_line)"
    fi
done

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
