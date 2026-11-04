#!/bin/bash
# =============================================================================
# Tests for scripts/release-local.sh (mg-c0665).
# =============================================================================
#
# A throwaway repo with a bare temp origin, and stub gh / goreleaser on the
# RELEASE_* overrides, so no real tag is pushed and nothing reaches GitHub.
# Every refusal is paired with a case that passes the same check, so a refusal
# that fired for the wrong reason (or always) is caught: the happy paths are
# the positive control for every REFUSED below.
# =============================================================================

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/release-local.sh"

# git runs in a fixture; keep it (and anything else) off the developer's $HOME.
source "$HERE/pogo-sandbox"
pogo_sandbox_create releaselocal
trap pogo_sandbox_down EXIT
pogo_sandbox_isolate
T="$POGO_SANDBOX_DIR"

PASS=0
FAIL=0
pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

# --- stubs ---------------------------------------------------------------------
# gh: behaviour chosen by $STUB_GH_MODE (absent | exists | error); logs argv.
mkdir -p "$T/bin"
cat > "$T/bin/gh" <<'EOF'
#!/bin/bash
echo "gh $*" >> "$STUB_LOG"
if [ "$1" = run ]; then
    # release.yml runs for the tag: $STUB_RUNS is the status lines to print.
    [ "${STUB_RUNS_MODE:-ok}" = error ] && { echo 'HTTP 502' >&2; exit 1; }
    printf '%s' "${STUB_RUNS:-}"; exit 0
fi
case "$STUB_GH_MODE" in
    exists) echo '{"tagName":"x","url":"https://example/x"}'; exit 0 ;;
    error)  echo 'HTTP 401: Bad credentials' >&2; exit 1 ;;
    absent)
        # After a publish, the confirming view must succeed.
        if [ -f "$STUB_PUBLISHED" ]; then echo "pogo_x.tar.gz"; exit 0; fi
        echo 'release not found' >&2; exit 1 ;;
esac
EOF
cat > "$T/bin/goreleaser" <<'EOF'
#!/bin/bash
echo "goreleaser $* CURRENT_TAG=${GORELEASER_CURRENT_TAG:-} TOKEN=$([ -n "${GITHUB_TOKEN:-}" ] && echo set || echo unset)" >> "$STUB_LOG"
case " $* " in *" --snapshot "*) ;; *) touch "$STUB_PUBLISHED" ;; esac
EOF
chmod +x "$T/bin/gh" "$T/bin/goreleaser"

export RELEASE_GH="$T/bin/gh" RELEASE_GORELEASER="$T/bin/goreleaser" RELEASE_REPO=test/test
export STUB_LOG="$T/log" STUB_PUBLISHED="$T/published"

# --- fixture repo --------------------------------------------------------------
git init -q --bare "$T/origin.git"
git init -q "$T/repo"
R="$T/repo"
(
    cd "$R"
    printf 'module example.com/x\n\ngo 1.25\n' > go.mod
    git add go.mod && git commit -qm one
    git remote add origin "$T/origin.git"
    git tag -a v1.2.3 -m v1.2.3
    git push -q origin HEAD:refs/heads/main refs/tags/v1.2.3
)

# run <name> <expect: ok|refused> <stderr/stdout substring> [script args...]
run() {
    local name=$1 expect=$2 want=$3; shift 3
    local out rc
    : > "$STUB_LOG"; rm -f "$STUB_PUBLISHED"
    set +e
    out="$(cd "$R" && GH_TOKEN="${TEST_TOKEN-x}" GITHUB_TOKEN= "$SCRIPT" "$@" 2>&1)"
    rc=$?
    set -e
    if [ "$expect" = ok ] && [ "$rc" -ne 0 ]; then fail "$name: expected success, rc=$rc: $out"; return; fi
    if [ "$expect" = refused ] && [ "$rc" -eq 0 ]; then fail "$name: expected refusal, rc=0: $out"; return; fi
    if [ "$expect" = refused ] && [[ "$out" != *"REFUSED"* ]]; then fail "$name: no REFUSED line: $out"; return; fi
    if [[ "$out" != *"$want"* ]]; then fail "$name: output lacks '$want': $out"; return; fi
    if [ "$expect" = refused ] && grep -q '^goreleaser' "$STUB_LOG"; then fail "$name: goreleaser ran despite refusal"; return; fi
    pass "$name"
}

echo "release-local.sh:"

export STUB_GH_MODE=absent

# Positive controls first: with everything in order, both modes run goreleaser.
run "dry run on the tag runs a snapshot" ok "--dry-run done" --dry-run v1.2.3
grep -q '^goreleaser release --snapshot --clean' "$STUB_LOG" && pass "dry run invoked goreleaser --snapshot" || fail "dry run did not invoke a snapshot: $(cat "$STUB_LOG")"

run "publish on the tag runs goreleaser" ok "published" v1.2.3
grep -q '^goreleaser release --clean CURRENT_TAG=v1.2.3 TOKEN=set' "$STUB_LOG" \
    && pass "publish pinned GORELEASER_CURRENT_TAG and passed GH_TOKEN as GITHUB_TOKEN" \
    || fail "publish argv/env wrong: $(cat "$STUB_LOG")"
grep -q -- '--snapshot' "$STUB_LOG" && fail "publish ran a snapshot" || pass "publish did not run a snapshot"

# Refusals.
run "non-semver tag" refused "is not a semver tag" 1.2.3
TEST_TOKEN= run "empty token" refused "neither GH_TOKEN nor GITHUB_TOKEN" v1.2.3
run "tag missing locally" refused "does not exist locally" v9.9.9

STUB_GH_MODE=exists run "release already exists" refused "already exists" v1.2.3
STUB_GH_MODE=error run "gh fails for another reason" refused "cannot tell whether a release" v1.2.3

STUB_RUNS=$'completed\n' run "a completed release.yml run does not block (control)" ok "--dry-run done" --dry-run v1.2.3
STUB_RUNS=$'in_progress\n' run "release.yml run in progress" refused "is not finished" v1.2.3
STUB_RUNS=$'completed\nqueued\n' run "release.yml run queued" refused "is not finished" v1.2.3
STUB_RUNS_MODE=error run "cannot list release.yml runs" refused "cannot list release.yml runs" v1.2.3

# HEAD moves off the tag.
(cd "$R" && echo two > f && git add f && git commit -qm two)
run "HEAD not on the tag" refused "HEAD is not on v1.2.3" v1.2.3
(cd "$R" && git checkout -q v1.2.3)
run "back on the tag (control)" ok "--dry-run done" --dry-run v1.2.3

# Dirty tree.
(cd "$R" && echo dirt > untracked)
run "dirty work tree" refused "work tree is dirty" v1.2.3
rm -f "$R/untracked"

# Tag not pushed.
(cd "$R" && git tag -a v1.2.4 -m v1.2.4)
run "tag not on origin" refused "is not on origin" v1.2.4

# Origin's tag names a different commit than the local tag of the same name.
(
    cd "$R"
    git checkout -q --detach v1.2.3
    git commit -q --allow-empty -m remote-side
    git tag -a v1.2.5 -m remote-one
    git push -q origin refs/tags/v1.2.5
    git tag -d v1.2.5 >/dev/null
    git checkout -q --detach v1.2.3
    git tag -a v1.2.5 -m local-one
)
run "origin's tag names another commit" refused "the local tag names" v1.2.5

# replace directive.
(cd "$R" && git checkout -q --detach v1.2.3 && printf 'replace a => ../a\n' >> go.mod && git commit -qam rep \
    && git tag -a v1.3.0 -m v1.3.0 && git push -q origin refs/tags/v1.3.0)
run "go.mod replace directive" refused "replace directives" v1.3.0

echo
echo "release-local_test: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
