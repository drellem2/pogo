#!/usr/bin/env bash
# enth_stamp_test.sh — docs/design/enth-vcs.md §10's table, re-run on a real
# enth #9 checkout, and the build.sh enth branch proved against it (mg-c8568,
# enth-vcs slice 10).
#
# WHAT §10 MEASURED, AND WHAT THIS RE-RUNS. A Go binary built in an enth
# checkout has no vcs.revision of its own — and when the checkout sits inside
# another git repo (every pogo workspace does: ~/.pogo is a git repo) Go walks up
# and stamps the OUTER repo's HEAD. §10 measured that on the M1 enth with a STUB
# .enth/checkout and said slice 10 must re-run it on a #9 checkout before
# merging. Rows 1-6 below are that table, raw `go build`, read with
# `go version -m`:
#
#   1  the git source repo                 vcs.revision = its commit  (POSITIVE CONTROL)
#   2  enth checkout, outside any git repo  absent
#   3  the same, with its #9 .enth/checkout absent (asserted: the file is real, v1)
#   4  enth checkout INSIDE a git repo      the OUTER repo's HEAD      (the hazard)
#   5  the same, GIT_CEILING_DIRECTORIES    the OUTER repo's HEAD again
#   6  the same, -buildvcs=false            absent
#
# Then the fix, on an enth checkout of THIS pogo tree nested inside a git repo
# (row 4's shape, the worst case):
#
#   7  ./build.sh --install: Commit = the base Change id, VCS=enth, source=ldflags,
#      NO vcs.revision; verify_installed_stamps passes, and fails when told to
#      expect another id (its positive control)
#   8  stamp_vcs / enth_ldflags directly: Dirty tracks `enth status`, a git repo
#      ROOTED at the dir stays git even with a .enth/checkout in it, the pogo
#      git tree itself is git with no VCS flag (the git build stamp unchanged)
#
# WHICH enth. The first `enth` on PATH (POGO_ENTH_BIN_DIR is prepended when
# set). If it predates enth #9 B — its checkout writes no .enth/checkout — the
# suite SKIPS with exit 0 and says so, because nothing here can be measured
# with it; POGO_ENTH_REQUIRE=1 turns that skip into a failure (use it when the
# point of the run is the proof). The git rows need only git and go.
#
# THE PHASES, as scripts/stamp_test.sh: every build under the real HOME (Go's
# module cache), then pogo_sandbox_isolate, then run the built binaries.

set -u

# shellcheck source=/dev/null
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pogo-sandbox"
# shellcheck source=/dev/null
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/enth-stamp.sh"

ES_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ES_REPO="$(cd "$ES_HERE/.." && pwd)"
[ -n "${POGO_ENTH_BIN_DIR:-}" ] && PATH="$POGO_ENTH_BIN_DIR:$PATH"

ES_RESULTS=$(mktemp)
pogo_sandbox_create enthstamp
ES_TMP="$(cd "$POGO_SANDBOX_DIR" && pwd -P)"
es_cleanup() { pogo_sandbox_down; rm -f "$ES_RESULTS"; }
trap es_cleanup EXIT
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$ES_RESULTS"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$ES_RESULTS"; }
skip() {
    if [ "${POGO_ENTH_REQUIRE:-}" = 1 ]; then
        echo "enth_stamp_test.sh: FAIL (POGO_ENTH_REQUIRE=1): $1" >&2
        exit 1
    fi
    echo "enth_stamp_test.sh: SKIP — $1"
    exit 0
}

es_git() {
    git -c user.email=enthstamp@test -c user.name="enth stamp" -c commit.gpgsign=false "$@"
}
# es_vcsrev BIN: Go's vcs.revision, or "" when the binary carries none.
es_vcsrev() { go version -m "$1" 2>/dev/null | sed -n 's/.*vcs[.]revision=\([0-9a-f]*\).*/\1/p' | head -1; }
es_field() { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -1; }
es_bool()  { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\([tf][a-z]*\).*/\1/p" | head -1; }

# The "outside any git repo" rows mean nothing if the sandbox itself is inside
# one; refuse rather than report a row that measured something else.
if git -C "$ES_TMP" rev-parse --git-dir >/dev/null 2>&1; then
    echo "SETUP FAILURE: the sandbox $ES_TMP is inside a git repo ($(git -C "$ES_TMP" rev-parse --show-toplevel)); rows 2-3 cannot be measured here" >&2
    exit 1
fi

# --- which enth --------------------------------------------------------------
command -v enth >/dev/null 2>&1 || skip "no enth on PATH (set POGO_ENTH_BIN_DIR to a #9 build)"
ES_STORE="$ES_TMP/store"

# A one-file module, as §10 used.
ES_SRC="$ES_TMP/src"
mkdir -p "$ES_SRC"
printf 'module example.com/enthstamp\n\ngo 1.24\n' > "$ES_SRC/go.mod"
printf 'package main\n\nfunc main() {}\n' > "$ES_SRC/main.go"
es_git -C "$ES_SRC" init -q -b main && es_git -C "$ES_SRC" add . && es_git -C "$ES_SRC" commit -qm src \
    || { echo "SETUP FAILURE: could not make the git source repo" >&2; exit 1; }
ES_SRC_SHA="$(git -C "$ES_SRC" rev-parse HEAD)"
enth --store "$ES_STORE" import "$ES_SRC" >"$ES_TMP/import.log" 2>&1 \
    || { echo "SETUP FAILURE: enth import failed: $(tail -2 "$ES_TMP/import.log")" >&2; exit 1; }

ES_OUT="$ES_TMP/out/co"
mkdir -p "$ES_TMP/out"
enth --store "$ES_STORE" checkout main "$ES_OUT" >"$ES_TMP/co.log" 2>&1 \
    || { echo "SETUP FAILURE: enth checkout failed: $(tail -2 "$ES_TMP/co.log")" >&2; exit 1; }
[ -f "$ES_OUT/.enth/checkout" ] \
    || skip "$(command -v enth) predates enth #9 B: its checkout wrote no .enth/checkout ($(enth --version 2>&1 | head -1))"
ES_BASE="$(enth_base "$ES_OUT")"
ES_BASE_CO="$(sed -n 's/.*"change":"\([0-9a-f]*\)".*/\1/p' "$ES_TMP/co.log" | head -1)"
echo "enth: $(command -v enth) — #9 checkout state present; base $ES_BASE"

# The outer git repo, shaped like ~/.pogo: workspaces live in a gitignored
# subtree of it.
ES_OUTER="$ES_TMP/outer"
mkdir -p "$ES_OUTER/ws"
printf 'ws/\n' > "$ES_OUTER/.gitignore"
es_git -C "$ES_OUTER" init -q -b main && es_git -C "$ES_OUTER" add .gitignore && es_git -C "$ES_OUTER" commit -qm outer \
    || { echo "SETUP FAILURE: could not make the outer git repo" >&2; exit 1; }
ES_OUTER_SHA="$(git -C "$ES_OUTER" rev-parse HEAD)"
ES_IN="$ES_OUTER/ws/co"
enth --store "$ES_STORE" checkout main "$ES_IN" >"$ES_TMP/coin.log" 2>&1 \
    || { echo "SETUP FAILURE: nested enth checkout failed: $(tail -2 "$ES_TMP/coin.log")" >&2; exit 1; }

# =============================================================================
# PHASE 1 — builds.
# =============================================================================
ES_B="$ES_TMP/bins"; mkdir -p "$ES_B"
( cd "$ES_SRC" && go build -o "$ES_B/r1" . ) 2>"$ES_TMP/r1.err"
( cd "$ES_OUT" && go build -o "$ES_B/r2" . ) 2>"$ES_TMP/r2.err"
( cd "$ES_IN"  && go build -o "$ES_B/r4" . ) 2>"$ES_TMP/r4.err"
( cd "$ES_IN"  && GIT_CEILING_DIRECTORIES="$ES_OUTER" go build -o "$ES_B/r5" . ) 2>"$ES_TMP/r5.err"
( cd "$ES_IN"  && go build -buildvcs=false -o "$ES_B/r6" . ) 2>"$ES_TMP/r6.err"

# Row 7's fixture: an enth checkout of THIS pogo tree (tracked and untracked-not-ignored files as they
# are on disk, so an uncommitted build.sh is the one tested), nested in the
# outer repo.
ES_PSRC="$ES_TMP/pogosrc"; mkdir -p "$ES_PSRC"
( cd "$ES_REPO" && git ls-files -z -co --exclude-standard | while IFS= read -r -d '' f; do
      if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\0' "$f"; fi
  done | tar --null -T - -cf - ) | ( cd "$ES_PSRC" && tar -xf - ) \
    || { echo "SETUP FAILURE: could not copy the pogo tree" >&2; exit 1; }
es_git -C "$ES_PSRC" init -q -b main && es_git -C "$ES_PSRC" add -A && es_git -C "$ES_PSRC" commit -qm pogo \
    || { echo "SETUP FAILURE: could not commit the pogo tree copy" >&2; exit 1; }
enth --store "$ES_STORE" import "$ES_PSRC" >"$ES_TMP/pimport.log" 2>&1 \
    || { echo "SETUP FAILURE: enth import of the pogo tree failed: $(tail -2 "$ES_TMP/pimport.log")" >&2; exit 1; }
ES_PIN="$ES_OUTER/ws/pogo"
enth --store "$ES_STORE" checkout main "$ES_PIN" >"$ES_TMP/pco.log" 2>&1 \
    || { echo "SETUP FAILURE: enth checkout of the pogo tree failed: $(tail -2 "$ES_TMP/pco.log")" >&2; exit 1; }
ES_PBASE="$(enth_base "$ES_PIN")"
ES_PBIN="$ES_TMP/pbin"; ES_PGOBIN="$ES_TMP/pgobin"; mkdir -p "$ES_PBIN" "$ES_PGOBIN"
( cd "$ES_PIN" && POGO_GATE_PROFILE=0 POGO_BUILD_DIR="$ES_PBIN" GOBIN="$ES_PGOBIN" ./build.sh --skip-tests --install ) \
    >"$ES_TMP/pbuild.log" 2>&1 && ES_PBUILT=yes || ES_PBUILT=no
# verify_installed_stamps' positive control: the same install, told to expect
# an id it does not carry, must fail.
( cd "$ES_PIN" && POGO_GATE_PROFILE=0 POGO_BUILD_DIR="$ES_PBIN" GOBIN="$ES_PGOBIN" \
    POGO_INSTALL_VERIFY_EXPECT=0000000000000000000000000000000000000000000000000000000000000000 ./build.sh --skip-tests --install ) \
    >"$ES_TMP/pctrl.log" 2>&1 && ES_PCTRL=green || ES_PCTRL=red

# =============================================================================
# PHASE 2 — isolation, before any built binary runs.
# =============================================================================
pogo_sandbox_isolate

# =============================================================================
# PHASE 3 — the table, and the assertions.
# =============================================================================
es_row() { printf '  %-58s %s\n' "$1" "$2"; }
es_show() { local r; r="$(es_vcsrev "$1")"; [ -n "$r" ] && echo "$r" || echo "absent"; }
echo
echo "§10 table, re-run on enth #9 ($(date -u +%Y-%m-%dT%H:%MZ), $(go version | awk '{print $3, $4}'))"
es_row "build directory" "vcs.revision"
es_row "1 the git source repo (positive control)" "$(es_show "$ES_B/r1")"
es_row "2 enth checkout, outside any git repo" "$(es_show "$ES_B/r2")"
es_row "3 the same, with its #9 .enth/checkout" "$(es_show "$ES_B/r2")"
es_row "4 enth checkout inside a git repo" "$(es_show "$ES_B/r4")  (outer HEAD $ES_OUTER_SHA)"
es_row "5 the same, GIT_CEILING_DIRECTORIES=outer" "$(es_show "$ES_B/r5")"
es_row "6 the same, -buildvcs=false" "$(es_show "$ES_B/r6")"
echo

[ "$(es_vcsrev "$ES_B/r1")" = "$ES_SRC_SHA" ] \
    && pass "row 1 (positive control): a git build stamps its own commit — the instrument reads vcs.revision" \
    || fail "row 1: the git control reads '$(es_vcsrev "$ES_B/r1")', want $ES_SRC_SHA — every 'absent' below is unproven ($(tail -1 "$ES_TMP/r1.err" 2>/dev/null))"
[ -x "$ES_B/r2" ] && [ -z "$(es_vcsrev "$ES_B/r2")" ] \
    && pass "row 2: an enth checkout outside any git repo carries no vcs.revision" \
    || fail "row 2: got '$(es_vcsrev "$ES_B/r2")' ($(tail -1 "$ES_TMP/r2.err" 2>/dev/null))"
case "$(head -1 "$ES_OUT/.enth/checkout")" in
    *'"format":"enth-checkout","version":1,'*) pass "row 3: the checkout is a real #9 one (.enth/checkout format enth-checkout version 1), and row 2 was built from it" ;;
    *) fail "row 3: .enth/checkout's first line is not enth-checkout v1: $(head -c 120 "$ES_OUT/.enth/checkout")" ;;
esac
[ -n "$ES_BASE" ] && [ "$ES_BASE" = "$ES_BASE_CO" ] \
    && pass "enth_base reads the base Change id the checkout reported ($ES_BASE)" \
    || fail "enth_base read '$ES_BASE', the checkout reported '$ES_BASE_CO'"
[ "$(es_vcsrev "$ES_B/r4")" = "$ES_OUTER_SHA" ] \
    && pass "row 4 (the hazard, reproduced): a nested enth checkout is stamped with the OUTER repo's HEAD" \
    || fail "row 4: got '$(es_vcsrev "$ES_B/r4")', want the outer HEAD $ES_OUTER_SHA — §10's premise no longer holds; re-read the design"
[ "$(es_vcsrev "$ES_B/r5")" = "$ES_OUTER_SHA" ] \
    && pass "row 5: GIT_CEILING_DIRECTORIES does not stop it (outer HEAD again)" \
    || fail "row 5: got '$(es_vcsrev "$ES_B/r5")', want the outer HEAD"
[ -x "$ES_B/r6" ] && [ -z "$(es_vcsrev "$ES_B/r6")" ] \
    && pass "row 6: -buildvcs=false leaves vcs.revision absent — the lever build.sh uses" \
    || fail "row 6: got '$(es_vcsrev "$ES_B/r6")'"

# --- row 7: build.sh in a nested enth checkout of pogo ----------------------
if [ "$ES_PBUILT" = yes ]; then
    pass "row 7: ./build.sh --skip-tests --install succeeded in a nested enth checkout of pogo"
    grep -q 'install verified: .*enth Change' "$ES_TMP/pbuild.log" \
        && pass "row 7: verify_installed_stamps verified the enth stamps" \
        || fail "row 7: verify_installed_stamps did not report an enth verification: $(grep 'build.sh:' "$ES_TMP/pbuild.log" | tail -2)"
    for es_name in pogo pogod; do
        for es_bin in "$ES_PBIN/$es_name" "$ES_PGOBIN/$es_name"; do
            es_out="$("$es_bin" version --json 2>/dev/null)"
            es_c="$(printf '%s' "$es_out" | es_field commit)"
            es_v="$(printf '%s' "$es_out" | es_field vcs)"
            es_s="$(printf '%s' "$es_out" | es_field source)"
            es_br="$(printf '%s' "$es_out" | es_field branch)"
            es_r="$(es_vcsrev "$es_bin")"
            if [ "$es_c" = "$ES_PBASE" ] && [ "$es_v" = enth ] && [ "$es_s" = ldflags ] && [ -z "$es_r" ]; then
                pass "row 7: $es_bin reports commit=$(printf '%s' "$ES_PBASE" | cut -c1-12)… vcs=enth source=ldflags branch=$es_br, no vcs.revision"
            else
                fail "row 7: $es_bin reports commit=$es_c vcs=$es_v source=$es_s, vcs.revision='$es_r'; want $ES_PBASE, enth, ldflags, absent"
            fi
        done
    done
    case "$("$ES_PBIN/pogo" --version 2>/dev/null)" in
        *"vcs=enth"*) pass "row 7: the human line says vcs=enth, so the id is not mistaken for a git sha" ;;
        *) fail "row 7: pogo --version = '$("$ES_PBIN/pogo" --version 2>&1)' — no vcs=enth" ;;
    esac
else
    fail "row 7: ./build.sh --install failed in the nested enth checkout: $(tail -5 "$ES_TMP/pbuild.log")"
fi
if [ "$ES_PCTRL" = red ] && grep -q 'verification FAILED' "$ES_TMP/pctrl.log"; then
    pass "row 7 POSITIVE CONTROL: verify_installed_stamps FAILS when told to expect a different Change id"
else
    fail "row 7 POSITIVE CONTROL: verify_installed_stamps did not fail on a wrong expected id ($ES_PCTRL) — its green above proves nothing"
fi

# --- row 8: the stamp functions directly ------------------------------------
[ "$(stamp_vcs "$ES_PIN")" = enth ] \
    && pass "row 8: stamp_vcs calls a nested enth checkout enth, though git rev-parse HEAD there succeeds ($(git -C "$ES_PIN" rev-parse --short HEAD 2>/dev/null))" \
    || fail "row 8: stamp_vcs($ES_PIN) = $(stamp_vcs "$ES_PIN")"
[ "$(stamp_vcs "$ES_OUT")" = enth ] && pass "row 8: stamp_vcs calls an enth checkout outside git enth" || fail "row 8: stamp_vcs($ES_OUT) = $(stamp_vcs "$ES_OUT")"
[ "$(stamp_vcs "$ES_REPO")" = git ] && pass "row 8 (git control): stamp_vcs calls this pogo tree git" || fail "row 8: stamp_vcs($ES_REPO) = $(stamp_vcs "$ES_REPO")"
# A git repo ROOTED at the dir wins even when it holds a .enth/checkout.
mkdir -p "$ES_SRC/.enth" && cp "$ES_OUT/.enth/checkout" "$ES_SRC/.enth/checkout"
[ "$(stamp_vcs "$ES_SRC")" = git ] \
    && pass "row 8: a git repo rooted at the dir stays git even with a .enth/checkout in it" \
    || fail "row 8: stamp_vcs on a git root with .enth/checkout = $(stamp_vcs "$ES_SRC")"
rm -rf "$ES_SRC/.enth"

es_flags="$(enth_ldflags "$ES_OUT" 2>"$ES_TMP/f.err")"
case "$es_flags" in
    *".Commit=$ES_BASE "*".Dirty=false "*".VCS=enth") pass "row 8: a clean checkout stamps Dirty=false (enth status: ops 0)" ;;
    *) fail "row 8: clean checkout flags: '$es_flags' ($(cat "$ES_TMP/f.err"))" ;;
esac
case "$es_flags" in *".Branch="*) fail "row 8: enth_ldflags stamped a Branch — .enth/checkout records no ref to read one from" ;;
    *) pass "row 8: no Branch is stamped (.enth/checkout records no ref); the binary says branch=unknown" ;; esac
echo "// edited" >> "$ES_OUT/main.go"
case "$(enth_ldflags "$ES_OUT" 2>/dev/null)" in
    *".Dirty=true "*) pass "row 8: the SAME checkout, edited, stamps Dirty=true — the detector discriminates" ;;
    *) fail "row 8: an edited checkout did not stamp Dirty=true: $(enth_ldflags "$ES_OUT" 2>&1)" ;;
esac
case "$(PATH=/usr/bin:/bin enth_ldflags "$ES_IN" 2>&1)" in
    *"stamping Dirty=true"*".Dirty=true "*) pass "row 8: with no enth to ask, the tree is stamped Dirty=true and the log says why" ;;
    *) fail "row 8: no enth on PATH: $(PATH=/usr/bin:/bin enth_ldflags "$ES_IN" 2>&1)" ;;
esac

echo
if grep -q '^FAIL:' "$ES_RESULTS"; then
    echo "enth_stamp_test.sh: FAILURES"
    grep '^FAIL:' "$ES_RESULTS"
    exit 1
fi
echo "enth_stamp_test.sh: all $(grep -c '^PASS:' "$ES_RESULTS") assertions passed"
