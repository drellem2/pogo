#!/usr/bin/env bash
# enth-hermetic-path_test.sh — the helpers that keep a host enth-land-gate out
# of the enth scenario scripts (mg-7f8e4). Each check has a positive control:
# the gate IS visible before the helper runs.
set -u
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
. "$REPO_ROOT/scripts/enth-hermetic-path"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT INT TERM HUP
FAILS=0
fail() { echo "FAIL: $*" >&2; FAILS=$((FAILS+1)); }
mk() { mkdir -p "$(dirname "$1")"; printf '#!/bin/sh\necho %s\n' "$(basename "$1")" > "$1"; chmod +x "$1"; }

# A box install: the gate next to enth and enthd, plus a non-enth tool.
for b in enth enthd enth-land enth-land-gate claude; do mk "$T/box/$b"; done
mk "$T/elsewhere/enth-land-gate"
mk "$T/tools/jq"

# enth_bin_without_gate
enth_bin_without_gate "$T/box" "$T/link" || fail "enth_bin_without_gate returned $?"
[ -x "$T/box/enth-land-gate" ] || fail "control: the box has no gate"
for b in enth enthd enth-land; do [ -x "$T/link/$b" ] || fail "link: $b missing"; done
[ -e "$T/link/enth-land-gate" ] && fail "link: the box's enth-land-gate was linked"
[ -e "$T/link/claude" ] && fail "link: a non-enth binary was linked"
[ "$(readlink "$T/link/enth")" = "$T/box/enth" ] || fail "link: enth is not a link to the box's"

# path_without_land_gate
P="$T/tools:$T/box:$T/elsewhere:/usr/bin:/bin"
[ "$(PATH="$P" command -v enth-land-gate)" = "$T/box/enth-land-gate" ] || fail "control: the gate is not visible on the unfiltered PATH"
F="$(path_without_land_gate "$P")"
[ "$F" = "$T/tools:/usr/bin:/bin" ] || fail "path_without_land_gate: got $F"
PATH="$F" command -v enth-land-gate >/dev/null && fail "the filtered PATH still finds a gate"
[ "$(path_without_land_gate "::$T/tools::")" = "$T/tools" ] || fail "empty entries: got $(path_without_land_gate "::$T/tools::")"

# What the scenario scripts build: sandbox bin, linked enth, filtered PATH.
mk "$T/sbin/enth-land-gate"
NP="$T/sbin:$T/link:$(path_without_land_gate "$P")"
[ "$(PATH="$NP" command -v enth-land-gate)" = "$T/sbin/enth-land-gate" ] || fail "the sandbox's own gate is not the one found"
[ "$(PATH="$NP" command -v enth)" = "$T/link/enth" ] || fail "enth is not found through the link dir"

[ "$FAILS" = 0 ] && echo "enth-hermetic-path_test.sh: PASS" || { echo "enth-hermetic-path_test.sh: $FAILS failure(s)"; exit 1; }
