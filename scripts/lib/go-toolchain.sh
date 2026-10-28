#!/bin/bash
# =============================================================================
# FIND THE GO TOOLCHAIN (drellem2/pogo#234, mg-bc32b)
# =============================================================================
#
# build.sh, fmt.sh and test.sh call bare `go`. The refinery gate runs them with
# pogod's environment, and pogod's PATH is whatever its service manager gave
# it: on darwin that is launchdPath() (internal/service/service.go), which has
# no /usr/local/go/bin, the official go.dev install location; on linux the
# systemd unit sets no PATH at all. On such a host every gate failed with
# `go: command not found`, whatever the branch contained.
#
# go_toolchain_resolve looks for go in this order:
#
#   1. POGO_GO, if set. It must name an executable file. If it does not, this
#      FAILS. It does not fall through to the steps below: someone who set
#      POGO_GO meant that go, and quietly building with another one would hide
#      the mistake.
#   2. `command -v go`. If go is already on PATH, it is used and nothing else
#      changes. That is the normal case on a developer machine.
#   3. The probe list, in order:
#        $GOROOT/bin           (only when GOROOT is set)
#        /usr/local/go/bin     (go.dev installer and tarball)
#        /opt/homebrew/bin     (Homebrew, Apple silicon)
#        /usr/local/bin        (Homebrew, Intel)
#        /usr/lib/go/bin       (Debian/Ubuntu golang-go)
#        ~/sdk/go*/bin         (`go install golang.org/dl/...`; newest only)
#      POGO_GO_PROBE, a colon-separated list, replaces this list. It is the
#      test seam that scripts/go-toolchain_test.sh uses so it does not depend
#      on what this host has installed.
#
# When go comes from step 1 or 3, the probed directory is NOT put on PATH.
# /usr/local/bin and /opt/homebrew/bin hold many other tools, and putting one of
# them in front would change which of those every later command runs. Instead
# go, and gofmt if it is next to it, are symlinked into a private directory, and
# only that directory goes on the front of PATH. Child scripts inherit it, so a
# build.sh that resolved go hands it to fmt.sh and test.sh, which then find it
# in step 2. cmd/go resolves its own symlink to locate GOROOT, the same as for
# Homebrew's /opt/homebrew/bin/go, which is itself a symlink.
#
# The private directory is removed on exit. The cleanup is added to the
# caller's existing EXIT trap rather than replacing it, so the caller's own
# report (gate_profile_report, gate_exit_report) still runs. INT, TERM and HUP
# are trapped as well when the caller has not trapped them, because an EXIT
# trap alone is not a guarantee on a signal (the mg-b9621 pattern). The traps
# are only touched when a private directory was made; the PATH case changes
# nothing.
#
# The version is still chosen by GOTOOLCHAIN against go.mod, as before. This
# file only decides which `go` binary is started.
#
# USAGE (call it after the caller has armed its own EXIT trap):
#   . "$DIR/scripts/lib/go-toolchain.sh"
#   go_toolchain_resolve || exit $?
#
# Exit statuses: 0 found; 2 POGO_GO is set but not an executable file;
# 127 no go anywhere (the status a bare `go` gave before).

go_toolchain_candidates() {
  if [ -n "${POGO_GO_PROBE+x}" ]; then
    printf '%s\n' "$POGO_GO_PROBE" | tr ':' '\n'
    return 0
  fi
  [ -n "${GOROOT:-}" ] && printf '%s\n' "$GOROOT/bin"
  printf '%s\n' /usr/local/go/bin /opt/homebrew/bin /usr/local/bin /usr/lib/go/bin
  # Newest ~/sdk/goX.Y.Z by numeric minor, then patch. The glob is left
  # unexpanded when nothing matches, and the -x test in the caller drops it.
  local sdk
  sdk="$(ls -d "$HOME"/sdk/go1.*/bin 2>/dev/null |
    sed 's|.*/sdk/go1\.\([0-9]*\)\.*\([0-9]*\).*|\1 \2 &|' |
    sort -k1,1n -k2,2n | tail -1 | cut -d' ' -f3-)"
  [ -n "$sdk" ] && printf '%s\n' "$sdk"
  return 0
}

# Symlink $1 (a go binary) and a gofmt beside it into a private directory, put
# that directory first on PATH, and arrange for it to be removed on exit.
go_toolchain_expose() {
  local go_bin="$1" dir prev
  dir="$(mktemp -d "${TMPDIR:-/tmp}/pogo-go.XXXXXX")" || {
    echo "go-toolchain: cannot create a private directory for $go_bin" >&2
    return 1
  }
  ln -s "$go_bin" "$dir/go"
  if [ -x "$(dirname "$go_bin")/gofmt" ]; then
    ln -s "$(dirname "$go_bin")/gofmt" "$dir/gofmt"
  fi
  GO_TOOLCHAIN_DIR="$dir"

  # Chain onto the caller's EXIT trap instead of replacing it. `trap -p EXIT`
  # prints `trap -- '<cmd>' EXIT`, quoted for re-reading, so eval recovers <cmd>.
  prev="$(trap -p EXIT)"
  if [ -n "$prev" ]; then
    eval "set -- $prev"
    prev="$3"
  fi
  # shellcheck disable=SC2064 # expand now: the trap must name this dir.
  trap "rm -rf '$dir'${prev:+; $prev}" EXIT
  [ -n "$(trap -p INT)" ] || trap 'exit 130' INT
  [ -n "$(trap -p TERM)" ] || trap 'exit 143' TERM
  [ -n "$(trap -p HUP)" ] || trap 'exit 129' HUP

  PATH="$dir:$PATH"
  export PATH
}

go_toolchain_resolve() {
  local found

  if [ -n "${POGO_GO+x}" ]; then
    if [ -z "$POGO_GO" ] || [ -d "$POGO_GO" ] || [ ! -x "$POGO_GO" ]; then
      echo "go-toolchain: POGO_GO=${POGO_GO:-<empty>} is not an executable file." >&2
      echo "  Point POGO_GO at a go binary, or unset it to use go from PATH." >&2
      return 2
    fi
    go_toolchain_expose "$POGO_GO" || return 1
    echo "go-toolchain: using $POGO_GO (from POGO_GO)"
    return 0
  fi

  if found="$(command -v go)" && [ -n "$found" ]; then
    echo "go-toolchain: using $found (from PATH)"
    return 0
  fi

  local cand tried=""
  while IFS= read -r cand; do
    [ -n "$cand" ] || continue
    tried="$tried
    $cand/go"
    if [ -f "$cand/go" ] && [ -x "$cand/go" ]; then
      go_toolchain_expose "$cand/go" || return 1
      echo "go-toolchain: using $cand/go (not on PATH; found by probing $cand)"
      return 0
    fi
  done <<EOF
$(go_toolchain_candidates)
EOF

  echo "go-toolchain: no go toolchain found." >&2
  echo "  POGO_GO is not set." >&2
  echo "  PATH=$PATH" >&2
  echo "  Also tried:${tried:- (no candidates)}" >&2
  echo "  Install go, put it on PATH, or set POGO_GO=/path/to/go." >&2
  return 127
}
