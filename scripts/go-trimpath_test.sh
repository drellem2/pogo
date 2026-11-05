#!/usr/bin/env bash
# Controls on scripts/lib/go-trimpath.sh (drellem2/pogo#282): build.sh and
# test.sh run go with -trimpath, so polecat worktrees at different paths share
# build-cache entries.
#
#   (a) An unset/empty GOFLAGS becomes "-trimpath", exported to children.
#   (b) A GOFLAGS the caller set is kept, with -trimpath appended.
#   (c) -trimpath already present (bare or =true) is not added twice.
#   (d) POGO_NO_TRIMPATH=1 leaves GOFLAGS exactly as it was.
#   (e) test.sh sources the library and calls go_trimpath_enable, after
#       go_toolchain_resolve. Without this case, deleting the call would leave
#       (a)-(d) green and the test compile untrimmed.
#   (f) build.sh does NOT call it: the binaries it builds and installs must keep
#       their -ldflags build-info record, which -trimpath drops and
#       internal/selfdrift reads (see the library's header).
#   (g) build.sh's go build / go install pass -trimpath=false, so an inherited
#       GOFLAGS=-trimpath (test.sh runs a nested ./build.sh --install) cannot
#       strip that record either.

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

# (e) test.sh calls it, after go is resolved.
f="$REPO_ROOT/test.sh"
resolve_line="$(grep -n '^go_toolchain_resolve' "$f" | head -1 | cut -d: -f1)"
source_line="$(grep -n '^\. ".*scripts/lib/go-trimpath\.sh"' "$f" | head -1 | cut -d: -f1)"
enable_line="$(grep -n '^go_trimpath_enable$' "$f" | head -1 | cut -d: -f1)"
if [ -z "$resolve_line" ]; then
    fail "(e) test.sh: no go_toolchain_resolve line found (the check below cannot be trusted)"
elif [ -z "$source_line" ] || [ -z "$enable_line" ]; then
    fail "(e) test.sh does not source go-trimpath.sh and call go_trimpath_enable"
elif [ "$source_line" -lt "$enable_line" ] && [ "$resolve_line" -lt "$enable_line" ]; then
    pass "(e) test.sh sources the library and calls go_trimpath_enable after go_toolchain_resolve"
else
    fail "(e) test.sh: wrong order (resolve=$resolve_line source=$source_line enable=$enable_line)"
fi

# (f) build.sh does not. Control: the same pattern finds test.sh's call above.
if grep -q '^[[:space:]]*go_trimpath_enable' "$REPO_ROOT/build.sh"; then
    fail "(f) build.sh calls go_trimpath_enable: its installed binaries would lose their -ldflags build-info record"
elif ! grep -q '^[[:space:]]*go_trimpath_enable' "$REPO_ROOT/test.sh"; then
    fail "(f) control: the pattern does not find test.sh's call either, so it proves nothing about build.sh"
else
    pass "(f) build.sh does not call go_trimpath_enable (control: the pattern finds test.sh's)"
fi

# (g) build.sh's go build and go install pass -trimpath=false, so a GOFLAGS
# =-trimpath inherited from the caller cannot reach the binaries either.
for verb in build install; do
    line="$(grep -E "^[^#]*gate_step \"go $verb \./cmd/\.\.\.\" go $verb " "$REPO_ROOT/build.sh")"
    if [ -z "$line" ]; then
        fail "(g) build.sh: no 'go $verb ./cmd/...' gate step found (the check below cannot be trusted)"
    elif printf '%s\n' "$line" | grep -q -- ' -trimpath=false '; then
        pass "(g) build.sh's go $verb passes -trimpath=false"
    else
        fail "(g) build.sh's go $verb does not pass -trimpath=false: an inherited GOFLAGS=-trimpath would strip its -ldflags record"
    fi
done

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
