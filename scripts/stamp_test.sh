#!/usr/bin/env bash
# stamp_test.sh — a locally-built pogo binary must be able to say what it
# contains (mg-3141).
#
# WHY THIS IS A SHELL TEST AND NOT A GO TEST. The thing under test is not
# internal/version — that is unit-tested in resolve_test.go, and it passes
# whether or not any build path actually sets the flags. What is under test is
# the two BUILD PATHS this fleet runs: build.sh and scripts/pogo-self-deploy.
# Both were plain `go build` with no -ldflags while goreleaser (the release path,
# which produces none of the binaries on this box) had them, and that gap is the
# entire defect.
#
# IT RUNS THE BINARY. A `-X` naming a symbol path that does not exist links
# cleanly, emits no warning, and produces exactly the empty-commit state the
# ticket was filed about. So reading the ldflags string out of a script proves
# nothing; every assertion here builds an artifact and asks it.
#
# THE POSITIVE CONTROL IS TEST 5 AND IT IS THE LOAD-BEARING ONE. A stamping
# check that has only ever been seen going green is not known to work. Test 5
# builds with the flags deliberately withheld and requires the SAME assertion
# function to report RED.
#
# EVERY VARIABLE HERE IS ST_-PREFIXED, AND THAT IS NOT STYLE. This file sources
# scripts/pogo-self-deploy to reach its version_ldflags, and that script assigns
# REPO= at top level. The first draft used a bare $REPO: sourcing blanked it,
# every `cd "$REPO"` silently became a no-op (bash `cd ""` succeeds), every
# `git -C ""` silently fell back to the cwd — and because the cwd happened to be
# the repo root, eleven assertions passed while testing something other than
# what they named. Only the arm that needed $REPO as a real path (the clone)
# noticed. The prefix is what keeps that from recurring.

# THE PHASE ORDER IS FORCED BY Go, NOT CHOSEN. Every build below must run under
# the developer's REAL HOME: Go resolves GOMODCACHE off $HOME, and compiling
# after the sandbox override re-downloads the whole module cache into the
# throwaway root — minutes of network, and a cache Go marks read-only that then
# defeats teardown. scripts/pogo-sandbox says so at pogo_sandbox_create and the
# sigint suite is built the same way. So:
#
#   PHASE 1  build every artifact           (real HOME, no env var moved)
#   PHASE 2  pogo_sandbox_isolate           (HOME/XDG/POGO_HOME/MG_ROOT pinned)
#   PHASE 3  RUN the binaries and assert    (nothing can reach the live tree)
#
# That split is not merely a workaround: phase 3 is the half that executes
# freshly built binaries, and it is the half that must not be able to touch the
# live ~/.pogo — so the constraint and the isolation want the same boundary.

set -u

# Sourced FIRST, so anything they clobber is clobbered before this file's own
# state exists. pogo-self-deploy's main() will not run — BASH_SOURCE != $0.
# shellcheck source=/dev/null
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pogo-self-deploy"
# shellcheck source=/dev/null
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pogo-sandbox"

ST_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ST_REPO="$(cd "$ST_HERE/.." && pwd)"

ST_RESULTS=$(mktemp)
# The private root, vetted before anything is written into it. No environment
# variable moves yet — see the phase note above.
pogo_sandbox_create stamp
ST_TMP="$POGO_SANDBOX_DIR"
cleanup() {
    # pogo_sandbox_down chmods the root writable before removing it: any `go`
    # call under a sandbox HOME materialises Go's module cache there and Go
    # marks it 0444 by design (mg-e91e).
    pogo_sandbox_down
    rm -f "$ST_RESULTS"
}
trap cleanup EXIT
pass() { echo "PASS: $1"; echo "PASS: $1" >> "$ST_RESULTS"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$ST_RESULTS"; }

ST_SHA="$(git -C "$ST_REPO" rev-parse HEAD 2>/dev/null || true)"
ST_BRANCH="$(git -C "$ST_REPO" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
if [ -z "$ST_SHA" ]; then
    echo "SETUP FAILURE: $ST_REPO has no git HEAD; this suite has nothing to compare against" >&2
    exit 1
fi

# jq is not assumed — the ticket's acceptance line uses it, but this suite must
# run wherever the gate runs. Same tolerant extractor pogo-self-deploy's
# stamped_rev uses: both CLIs print INDENTED JSON, so json_str's compact
# `"k":"v"` shape is wrong here. BRE only; this is BSD sed on macOS.
st_field() { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -1; }
st_bool()  { sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\([tf][a-z]*\).*/\1/p" | head -1; }

# st_assert_stamped — THE assertion, used by every arm INCLUDING the positive
# control, so "red" and "green" are produced by the same code.
#   $1 label   $2 binary path   $3 expected commit   $4 expected branch
st_assert_stamped() {
    local label="$1" bin="$2" want_sha="$3" want_branch="$4"
    local out
    out="$("$bin" version --json 2>/dev/null || true)"
    if [ -z "$out" ]; then
        echo "  $label: \`$(basename "$bin") version --json\` produced nothing"
        return 1
    fi
    local got_sha got_branch got_source
    got_sha="$(printf '%s' "$out" | st_field commit)"
    got_branch="$(printf '%s' "$out" | st_field branch)"
    got_source="$(printf '%s' "$out" | st_field source)"
    if [ -z "$got_sha" ]; then
        echo "  $label: commit is ABSENT or empty — the exact state this ticket exists to end"
        return 1
    fi
    if [ "$got_sha" != "$want_sha" ]; then
        echo "  $label: commit=$got_sha (source=$got_source), want $want_sha"
        return 1
    fi
    if [ "$got_source" != "ldflags" ]; then
        echo "  $label: source=$got_source, want ldflags — a buildinfo stamp names whatever repo the build dir walked up into, not the one the script chose"
        return 1
    fi
    if [ -n "$want_branch" ] && [ "$got_branch" != "$want_branch" ]; then
        echo "  $label: branch=$got_branch, want $want_branch"
        return 1
    fi
    return 0
}

# ===========================================================================
# PHASE 1 — BUILD EVERYTHING, under the developer's real HOME.
#
# Nothing is asserted here beyond "the build ran": every claim about what a
# binary SAYS is phase 3, after the isolation is up. Each build records its own
# outcome so phase 3 can report a build failure as a build failure rather than
# as a missing stamp.
# ===========================================================================

# 1. scripts/pogo-self-deploy — the path that installs what the fleet runs.
# version_ldflags is called directly; nothing touches GOBIN, launchd or a daemon.
ST_SD_FLAGS="$(version_ldflags "$ST_REPO")"
ST_SD_DIR="$ST_TMP/self-deploy"; mkdir -p "$ST_SD_DIR"
ST_SD_BUILT=no
if [ -n "$ST_SD_FLAGS" ]; then
    ( cd "$ST_REPO" && go build -ldflags "$ST_SD_FLAGS" -o "$ST_SD_DIR/" ./cmd/pogo ./cmd/pogod ) 2>"$ST_TMP/sd.err" && ST_SD_BUILT=yes
fi

# 2. build.sh — the developer/gate path. Invoked for real (--skip-tests, so it
# does not recurse into the suite that is running this file) with POGO_BUILD_DIR
# redirected, so ./bin is untouched and nothing is installed.
ST_BS_DIR="$ST_TMP/build-sh"; mkdir -p "$ST_BS_DIR"
ST_BS_BUILT=no
( cd "$ST_REPO" && POGO_GATE_PROFILE=0 POGO_BUILD_DIR="$ST_BS_DIR" ./build.sh --skip-tests ) >"$ST_TMP/bs.log" 2>&1 && ST_BS_BUILT=yes

# 3. THE POSITIVE CONTROL's artifact: the same script with the flags withheld.
ST_NS_DIR="$ST_TMP/no-stamp"; mkdir -p "$ST_NS_DIR"
ST_NS_BUILT=no
( cd "$ST_REPO" && POGO_GATE_PROFILE=0 POGO_BUILD_NO_STAMP=1 POGO_BUILD_DIR="$ST_NS_DIR" ./build.sh --skip-tests ) >"$ST_TMP/ns.log" 2>&1 && ST_NS_BUILT=yes

# 4. The dirty/clean pair, built from THIS tree with the Dirty flag forced both
# ways — so the result does not depend on whether the working tree happens to be
# clean when the gate runs. It is dirty in a polecat mid-change and clean at
# merge, and an arm that silently tests only one of those passes for the wrong
# reason half the time.
ST_DD="$ST_TMP/dirtybin"; mkdir -p "$ST_DD/yes" "$ST_DD/no"
ST_DD_BUILT=no
ST_FLAGS_DIRTY="$(printf '%s' "$ST_SD_FLAGS" | sed 's/\.Dirty=false/.Dirty=true/')"
ST_FLAGS_CLEAN="$(printf '%s' "$ST_SD_FLAGS" | sed 's/\.Dirty=true/.Dirty=false/')"
if [ -n "$ST_SD_FLAGS" ] \
   && ( cd "$ST_REPO" && go build -ldflags "$ST_FLAGS_DIRTY" -o "$ST_DD/yes/" ./cmd/pogo ) 2>"$ST_TMP/dirty.err" \
   && ( cd "$ST_REPO" && go build -ldflags "$ST_FLAGS_CLEAN" -o "$ST_DD/no/" ./cmd/pogo ) 2>>"$ST_TMP/dirty.err"; then
    ST_DD_BUILT=yes
fi

# 5. The git fixture for the dirty-tree DETECTOR arm. A throwaway clone, so
# nothing in this suite can dirty the tree under test. Cloned here rather than
# in phase 3 because `git clone` reads the developer's git config.
ST_DIRTY_REPO="$ST_TMP/dirtyrepo"
ST_CLONED=no
git clone --quiet --shared "$ST_REPO" "$ST_DIRTY_REPO" 2>"$ST_TMP/clone.err" && ST_CLONED=yes
ST_DIRTY_CLEAN_FLAGS=""
ST_DIRTY_DIRTY_FLAGS=""
if [ "$ST_CLONED" = yes ]; then
    ST_DIRTY_CLEAN_FLAGS="$(version_ldflags "$ST_DIRTY_REPO")"
    echo "// stamp_test scratch" >> "$ST_DIRTY_REPO/internal/version/version.go"
    ST_DIRTY_DIRTY_FLAGS="$(version_ldflags "$ST_DIRTY_REPO")"
fi

# ===========================================================================
# PHASE 2 — the isolation. From here nothing can reach the developer's live
# ~/.pogo, ~/.macguffin or $HOME, which matters because phase 3 EXECUTES four
# freshly built pogo binaries.
# ===========================================================================
pogo_sandbox_isolate

# ===========================================================================
# PHASE 3 — RUN the binaries and assert.
# ===========================================================================

# --- pogo-self-deploy's path -----------------------------------------------
if [ -n "$ST_SD_FLAGS" ]; then
    pass "pogo-self-deploy: version_ldflags yields flags for a real repo"
else
    fail "pogo-self-deploy: version_ldflags yielded nothing for $ST_REPO"
fi
if [ "$ST_SD_BUILT" = yes ]; then
    for st_name in pogo pogod; do
        if st_assert_stamped "self-deploy/$st_name" "$ST_SD_DIR/$st_name" "$ST_SHA" "$ST_BRANCH"; then
            pass "pogo-self-deploy: the built $st_name REPORTS commit $ST_SHA when asked"
        else
            fail "pogo-self-deploy: the built $st_name cannot say what it contains"
        fi
    done
else
    fail "pogo-self-deploy: build with version_ldflags failed ($(tail -2 "$ST_TMP/sd.err" 2>/dev/null))"
fi

# --- build.sh's path -------------------------------------------------------
if [ "$ST_BS_BUILT" = yes ]; then
    for st_name in pogo pogod; do
        if st_assert_stamped "build.sh/$st_name" "$ST_BS_DIR/$st_name" "$ST_SHA" "$ST_BRANCH"; then
            pass "build.sh: the built $st_name REPORTS commit $ST_SHA when asked"
        else
            fail "build.sh: the built $st_name cannot say what it contains"
        fi
    done
else
    fail "build.sh --skip-tests failed ($(tail -3 "$ST_TMP/bs.log" 2>/dev/null))"
fi

# --- THE POSITIVE CONTROL --------------------------------------------------
# Same assertion function, flags deliberately withheld. Until this has been
# observed going red, nothing above is known to be checking anything.
#
# Note what it prints when it goes red on this box: source=buildinfo with a SHA
# the pogo repo does not contain. That is Go's automatic vcs stamp having walked
# up out of the polecat worktree into ~/.pogo. It is the measured reason the
# assertion requires source=ldflags and does not settle for a non-empty commit.
if [ "$ST_NS_BUILT" = yes ]; then
    ST_CTRL_OUT="$(st_assert_stamped "control/pogo" "$ST_NS_DIR/pogo" "$ST_SHA" "$ST_BRANCH" 2>&1)" && ST_CTRL_RC=0 || ST_CTRL_RC=$?
    if [ "$ST_CTRL_RC" -ne 0 ]; then
        pass "POSITIVE CONTROL: an unstamped build is reported RED by the same assertion ($ST_CTRL_OUT)"
    else
        fail "POSITIVE CONTROL: an UNSTAMPED binary passed st_assert_stamped — every green above is meaningless"
    fi
    # And it must not answer with the empty string. "unknown" is greppable; ""
    # is indistinguishable from a bug in whatever is reading it.
    ST_NS_COMMIT="$(printf '%s' "$("$ST_NS_DIR/pogo" version --json 2>/dev/null)" | st_field commit)"
    if [ -n "$ST_NS_COMMIT" ]; then
        pass "an unstamped binary still answers with a VALUE ($ST_NS_COMMIT), never the empty string"
    else
        fail "an unstamped binary reports commit=\"\" — the original defect"
    fi
else
    fail "build.sh --skip-tests with POGO_BUILD_NO_STAMP=1 failed ($(tail -3 "$ST_TMP/ns.log" 2>/dev/null))"
fi

# --- the acceptance line itself, run for real ------------------------------
# The liveness question answered from the binary, with no mtime and no inference.
if [ -x "$ST_BS_DIR/pogo" ]; then
    ST_LIVE_SHA="$(printf '%s' "$("$ST_BS_DIR/pogo" version --json 2>/dev/null)" | st_field commit)"
    if git -C "$ST_REPO" merge-base --is-ancestor "$ST_LIVE_SHA" HEAD 2>/dev/null; then
        pass "acceptance: \`git merge-base --is-ancestor <fix> \$(pogo version --json | .commit)\` resolves against the real repo"
    else
        fail "acceptance: the reported commit $ST_LIVE_SHA is not resolvable in $ST_REPO"
    fi
    # The negative half: a revision this repo does not contain must NOT answer
    # the ancestry question yes, or the acceptance line is a rubber stamp.
    if git -C "$ST_REPO" merge-base --is-ancestor "0000000000000000000000000000000000000000" HEAD 2>/dev/null; then
        fail "acceptance: merge-base --is-ancestor accepted an all-zero SHA — the check cannot discriminate"
    else
        pass "acceptance: an unknown revision does NOT answer the ancestry question"
    fi
fi

# --- both artifacts answer the SAME question the same way ------------------
# The 05:35Z narrowing on the ticket: the deploy log's revision record covers
# pogod only, and the CLI was the artifact that bit us. A probe that works on one
# and not the other leaves that asymmetry in place one layer down.
if [ -x "$ST_BS_DIR/pogo" ] && [ -x "$ST_BS_DIR/pogod" ]; then
    ST_P_SHA="$(printf '%s' "$("$ST_BS_DIR/pogo" version --json 2>/dev/null)" | st_field commit)"
    ST_D_SHA="$(printf '%s' "$("$ST_BS_DIR/pogod" version --json 2>/dev/null)" | st_field commit)"
    if [ -n "$ST_P_SHA" ] && [ "$ST_P_SHA" = "$ST_D_SHA" ]; then
        pass "pogo and pogod answer \`version --json\` identically ($(printf '%s' "$ST_P_SHA" | cut -c1-8)) — one spelling for both artifacts"
    else
        fail "pogo says '$ST_P_SHA' and pogod says '$ST_D_SHA' to the same question"
    fi
    # pogod's flag spelling must agree with its bare-arg spelling, or a caller
    # picking the "wrong" one gets a different answer.
    ST_D_FLAG_SHA="$(printf '%s' "$("$ST_BS_DIR/pogod" -version -json 2>/dev/null)" | st_field commit)"
    if [ "$ST_D_FLAG_SHA" = "$ST_D_SHA" ]; then
        pass "pogod -version -json and pogod version --json agree"
    else
        fail "pogod -version -json says '$ST_D_FLAG_SHA', bare-arg says '$ST_D_SHA'"
    fi

    # `--version` on EVERY binary. scripts/launchd/pogo-deploy.sh's
    # partial-install recovery text tells an operator to "ask each binary its
    # revision before doing anything else: `pogod --version`, `pogo --version`"
    # — and before mg-3141 neither could: pogo printed a bare semver, pogod did
    # not define the flag at all. A documented remedy that cannot do what it says
    # is worse than none, so it is asserted here rather than left as prose.
    for st_name in pogo pogod lsp pose; do
        [ -x "$ST_BS_DIR/$st_name" ] || continue
        case "$("$ST_BS_DIR/$st_name" --version 2>/dev/null)" in
            "$st_name "*"source=ldflags"*)
                pass "$st_name --version names its own revision (pogo-deploy.sh's recovery text points operators here)" ;;
            *)
                fail "$st_name --version = '$("$ST_BS_DIR/$st_name" --version 2>&1 | head -1)' — it cannot say what it contains" ;;
        esac
    done
fi

# --- the dirty marker: the DETECTOR ----------------------------------------
if [ "$ST_CLONED" = yes ]; then
    case "$ST_DIRTY_CLEAN_FLAGS" in
        *".Dirty=false"*) pass "a clean tree stamps Dirty=false" ;;
        *) fail "a clean clone did not stamp Dirty=false ($ST_DIRTY_CLEAN_FLAGS)" ;;
    esac
    case "$ST_DIRTY_DIRTY_FLAGS" in
        *".Dirty=true"*) pass "the SAME tree, dirtied, stamps Dirty=true — the detector discriminates" ;;
        *) fail "a dirty tree did not stamp Dirty=true ($ST_DIRTY_DIRTY_FLAGS)" ;;
    esac
else
    fail "could not clone $ST_REPO for the dirty-tree arm ($(tail -2 "$ST_TMP/clone.err" 2>/dev/null))"
fi

# --- the dirty marker: what the BINARY says --------------------------------
# A revision without this qualifier is a claim about the repo, not about the
# binary: the same SHA describes a clean build and a build with arbitrary local
# edits on top.
if [ "$ST_DD_BUILT" = yes ]; then
    if [ "$("$ST_DD/yes/pogo" version --json 2>/dev/null | st_bool dirty)" = "true" ]; then
        pass "a binary stamped from a dirty tree SAYS SO in --json"
    else
        fail "a binary stamped Dirty=true reports dirty=false"
    fi
    if [ "$("$ST_DD/no/pogo" version --json 2>/dev/null | st_bool dirty)" = "false" ]; then
        pass "the clean-stamped counterpart reports dirty=false — the field is not hard-wired"
    else
        fail "a binary stamped Dirty=false reports dirty=true"
    fi
    # The human line is the one that gets quoted into a mail, so the marker has
    # to reach it and not live only in the JSON nobody pastes.
    case "$("$ST_DD/yes/pogo" version 2>/dev/null)" in
        *-dirty*) pass "the human line carries the -dirty suffix" ;;
        *) fail "the human line omits -dirty: $("$ST_DD/yes/pogo" version 2>/dev/null)" ;;
    esac
    case "$("$ST_DD/no/pogo" version 2>/dev/null)" in
        *-dirty*) fail "the clean binary's human line claims -dirty: $("$ST_DD/no/pogo" version 2>/dev/null)" ;;
        *) pass "the clean binary's human line does NOT claim -dirty" ;;
    esac
else
    fail "could not build the dirty/clean pair ($(tail -2 "$ST_TMP/dirty.err" 2>/dev/null))"
fi

# --- the DEPLOY'S OWN CHECK, run for real ----------------------------------
# stamped_rev is what refuses a bad install in do_build; nothing else exercises
# it. POGO_GOBIN redirects installed_bin at the stamped binaries built above, so
# no GOBIN is touched.
#
# It also pins the thing that makes stamped_rev safe rather than merely correct:
# it must probe pogod with the FLAG form. A pre-mg-3141 pogod given the bare
# `version` argument ignores it and STARTS THE DAEMON — measured — which during a
# redeploy (pogod lock free) is a second daemon and a check that never returns.
# Asserted here by requiring an answer from both binaries: if the spellings were
# ever swapped, pogo's arm goes silent and this goes red.
if [ "$ST_BS_BUILT" = yes ]; then
    for st_name in pogo pogod; do
        ST_SR="$(POGO_GOBIN="$ST_BS_DIR" stamped_rev "$st_name")"
        if [ "$ST_SR" = "$ST_SHA" ]; then
            pass "deploy check: stamped_rev $st_name returns $ST_SHA (the refusal the post-install loop rests on)"
        else
            fail "deploy check: stamped_rev $st_name returned '$ST_SR', want $ST_SHA"
        fi
    done
    # The negative half: stamped_rev must report a MISSING binary as such, not
    # as a revision and not as a hang.
    ST_SR_MISSING="$(POGO_GOBIN="$ST_TMP/nothing-here" stamped_rev pogod)"
    if [ "$ST_SR_MISSING" = "$REV_MISSING" ]; then
        pass "deploy check: stamped_rev reports an absent binary as $REV_MISSING"
    else
        fail "deploy check: stamped_rev on an absent binary returned '$ST_SR_MISSING', want $REV_MISSING"
    fi
    # And an UNSTAMPED one must not be reported as main.
    if [ "$ST_NS_BUILT" = yes ]; then
        ST_SR_NS="$(POGO_GOBIN="$ST_NS_DIR" stamped_rev pogo)"
        case "$ST_SR_NS" in
            "$ST_SHA") fail "deploy check: stamped_rev accepted an UNSTAMPED binary as $ST_SHA" ;;
            *) pass "deploy check: stamped_rev does not report an unstamped binary as main ($ST_SR_NS)" ;;
        esac
    fi
fi

# ---------------------------------------------------------------------------
echo
if grep -q '^FAIL:' "$ST_RESULTS"; then
    echo "stamp_test.sh: FAILURES"
    grep '^FAIL:' "$ST_RESULTS"
    exit 1
fi
echo "stamp_test.sh: all $(grep -c '^PASS:' "$ST_RESULTS") assertions passed"
