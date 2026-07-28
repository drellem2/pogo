#!/bin/bash
# Tests for scripts/mg-scope-guard.sh (mg-f1d5).
#
# The contract under test, in the order it decides:
#
#   1. OPT-IN. No item in force => allow. An item that declares no scope =>
#      allow. This is the case that must never regress: the guard is wired into
#      no agent by default, and a guard that blocked an agent nobody opted in
#      for would be removed from every fleet within the hour.
#   2. Past the opt-in it REFUSES rather than waving through. A missing `mg`, an
#      unreadable item, no worktree root — all loud. An opted-in agent that is
#      silently unenforced is the exact failure this script removes, and it is
#      invisible; a refusal naming its own fix is not.
#   3. `**` crosses directory separators and `*` does not. This is the one piece
#      of real logic here and the one a `[[ str == pat ]]` shortcut gets wrong:
#      bash's `*` swallows slashes, so `docs/*` would silently be `docs/**` and
#      every narrow scope would be a wide one.
#   4. An escape (outside the worktree) is exit 11, not exit 9. A break-out and
#      a typo are different facts and must not be filed together.
#   5. Hook mode speaks Claude's contract — 0 proceeds, 2 blocks — and collapses
#      every refusal to 2, because 2 is the only refusal Claude understands.
#      Non-writing tools are never consulted.
#
# Every case runs the real script against a fixture worktree in a temp dir, with
# MG_SCOPE_MG pointed at a stub `mg`. The developer's live ~/.macguffin is never
# read and never written.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GUARD="${SCRIPT_DIR}/mg-scope-guard.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== mg-scope-guard.sh tests ==="

# --- Fixture ---------------------------------------------------------------

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
WT="$T/wt"
mkdir -p "$WT/cmd/pogod" "$WT/docs" "$WT/internal/agent" "$T/bin"

# A stub mg. Three items: one scoped, one scoped over two carrier lines, one
# with no scope at all. Anything else is not found.
cat > "$T/bin/mg" <<'STUB'
#!/bin/bash
[ "$1" = "show" ] || { echo "stub mg: unexpected verb $1" >&2; exit 1; }
case "$2" in
  mg-scoped)
    echo "ID: mg-scoped"
    echo "scope: cmd/pogod/** docs/*.md"
    echo ""
    echo "body prose"
    ;;
  mg-two-lines)
    echo "ID: mg-two-lines"
    echo "scope: cmd/pogod/**"
    echo "  scope:   internal/agent/**   docs/README.md  "
    ;;
  mg-exact)
    echo "ID: mg-exact"
    echo "scope: docs/CONFIGURATION.md internal/agent"
    ;;
  mg-none)
    echo "ID: mg-none"
    echo "no scope declared here"
    ;;
  *) echo "work item not found: $2" >&2; exit 2 ;;
esac
STUB
chmod +x "$T/bin/mg"

export MG_SCOPE_ROOT="$WT"
export MG_SCOPE_MG="$T/bin/mg"

# run <item> <args...> -> prints exit code. An empty item means none in force.
run() {
    local item="$1"; shift
    local code=0
    if [ -z "$item" ]; then
        ( cd "$WT" && MG_SCOPE_ITEM="" bash "$GUARD" "$@" ) >/dev/null 2>&1 || code=$?
    else
        ( cd "$WT" && MG_SCOPE_ITEM="$item" bash "$GUARD" "$@" ) >/dev/null 2>&1 || code=$?
    fi
    echo "$code"
}

# hook <item> <json> -> prints exit code.
hook() {
    local item="$1" json="$2" code=0
    printf '%s' "$json" | ( cd "$WT" && MG_SCOPE_ITEM="$item" bash "$GUARD" ) >/dev/null 2>&1 || code=$?
    echo "$code"
}

expect() {
    local want="$1" got="$2" what="$3"
    if [ "$got" = "$want" ]; then pass "$what (exit $got)"; else fail "$what: want exit $want, got $got"; fi
}

# --- Test 1: syntax --------------------------------------------------------
echo ""
echo "Test 1: Script syntax check"
if bash -n "$GUARD" 2>/dev/null; then
    pass "mg-scope-guard.sh has valid bash syntax"
else
    fail "mg-scope-guard.sh has syntax errors"
fi

# --- Test 2: opt-in --------------------------------------------------------
# The guard is inert until somebody asks for it, twice over: no item, and an
# item with nothing declared. Both allow a path that is nowhere near any scope.
echo ""
echo "Test 2: opt-in — no item, and an item with no scope, allow everything"
expect 0 "$(run "" internal/agent/agent.go)"        "no item in force allows an unscoped path"
expect 0 "$(run mg-none internal/agent/agent.go)"   "an item declaring no scope allows an unscoped path"

# --- Test 3: matching ------------------------------------------------------
echo ""
echo "Test 3: in-scope paths are allowed"
expect 0 "$(run mg-scoped cmd/pogod/main.go)"           "** matches a nested path"
expect 0 "$(run mg-scoped docs/event-log.md)"           "* matches a file in the named directory"
expect 0 "$(run mg-scoped cmd/pogod/a.go docs/b.md)"    "several paths, all in scope"
expect 0 "$(run mg-scoped ./cmd/pogod/main.go)"         "a ./-prefixed path normalises"
expect 0 "$(run mg-scoped "$WT/cmd/pogod/main.go")"     "an absolute path inside the worktree"
expect 0 "$(run mg-scoped cmd/pogod/nested/deep/x.go)"  "** crosses several directories"

echo ""
echo "Test 4: out-of-scope paths are refused with exit 9"
expect 9 "$(run mg-scoped internal/agent/agent.go)"  "a path under no pattern"
expect 9 "$(run mg-scoped cmd/pogo/main.go)"         "a sibling of a scoped directory"
expect 9 "$(run mg-scoped cmd/pogod/a.go internal/agent/b.go)" "one bad path in a good list refuses the call"

# The distinction that a `[[ str == pat ]]` shortcut loses. `docs/*.md` names
# the files in docs/ and not the tree under it; if this passes as in-scope then
# every `*` in every scope has silently become `**`.
echo ""
echo "Test 5: * does not cross a directory separator"
expect 9 "$(run mg-scoped docs/sub/a.md)"  "docs/*.md does not match docs/sub/a.md"

echo ""
echo "Test 6: exact-file and directory-prefix forms"
expect 0 "$(run mg-exact docs/CONFIGURATION.md)"       "an exact filename matches itself"
expect 9 "$(run mg-exact docs/customizing.md)"         "an exact filename matches nothing else"
expect 0 "$(run mg-exact internal/agent/agent.go)"     "a bare directory covers what is under it"
expect 0 "$(run mg-exact internal/agent)"              "a bare directory covers itself"
expect 9 "$(run mg-exact internal/agentx/a.go)"        "a directory prefix stops at the separator"

echo ""
echo "Test 7: several scope: carrier lines accumulate"
expect 0 "$(run mg-two-lines cmd/pogod/main.go)"        "the first line still counts"
expect 0 "$(run mg-two-lines internal/agent/agent.go)"  "the second line counts too"
expect 0 "$(run mg-two-lines docs/README.md)"           "an indented carrier line is read"
expect 9 "$(run mg-two-lines docs/other.md)"            "and nothing else is admitted"

# --- Test 8: escape --------------------------------------------------------
# 11, not 9. Reporting a break-out as a routine scope refusal files a
# containment failure beside a typo, and the two want different responses.
echo ""
echo "Test 8: a path outside the worktree is an escape (exit 11), not a scope refusal"
expect 11 "$(run mg-scoped ../outside.go)"       "a relative path climbing out"
expect 11 "$(run mg-scoped /etc/passwd)"         "an absolute path elsewhere"
expect 11 "$(run mg-scoped cmd/pogod/../../../x)" "a path that climbs out through .."

# --- Test 9: fail loud past the opt-in -------------------------------------
echo ""
echo "Test 9: past the opt-in, a guard that cannot decide refuses"
expect 1 "$(run mg-nonexistent cmd/pogod/main.go)" "an item mg cannot read is refused, not allowed"

code=0
( cd "$WT" && MG_SCOPE_ITEM=mg-scoped MG_SCOPE_MG="$T/bin/definitely-not-here" \
    bash "$GUARD" cmd/pogod/main.go ) >/dev/null 2>&1 || code=$?
expect 1 "$code" "a missing mg binary is refused, not allowed"

code=0
( cd "$T" && MG_SCOPE_ITEM=mg-scoped MG_SCOPE_ROOT="" \
    bash "$GUARD" some.go ) >/dev/null 2>&1 || code=$?
expect 1 "$code" "an item in force with no worktree root is refused"

# The refusal has to name the way forward, or an agent that hits it has no move
# but to mail somebody.
msg="$( ( cd "$WT" && MG_SCOPE_ITEM=mg-scoped bash "$GUARD" internal/agent/a.go ) 2>&1 || true )"
if echo "$msg" | grep -q 'mg edit mg-scoped'; then
    pass "a scope refusal names the command that widens the scope"
else
    fail "a scope refusal does not name the fix: $msg"
fi

# --- Test 10: .mg-scope worktree binding -----------------------------------
echo ""
echo "Test 10: the worktree binding is used when MG_SCOPE_ITEM is unset"
printf '# which item this worktree is for\nmg-scoped\n' > "$WT/.mg-scope"
expect 0 "$(run "" cmd/pogod/main.go)"          ".mg-scope binds the item (in scope)"
expect 9 "$(run "" internal/agent/agent.go)"    ".mg-scope binds the item (out of scope)"
expect 0 "$(run mg-none internal/agent/a.go)"   "MG_SCOPE_ITEM overrides the file"
rm -f "$WT/.mg-scope"
expect 0 "$(run "" internal/agent/agent.go)"    "removing the file returns the guard to inert"

# --- Test 11: hook mode ----------------------------------------------------
echo ""
echo "Test 11: hook mode speaks Claude's contract (0 proceeds, 2 blocks)"
if command -v jq >/dev/null 2>&1; then
    expect 0 "$(hook mg-scoped '{"tool_name":"Edit","tool_input":{"file_path":"'"$WT"'/cmd/pogod/main.go"}}')" \
        "an Edit in scope proceeds"
    expect 2 "$(hook mg-scoped '{"tool_name":"Edit","tool_input":{"file_path":"'"$WT"'/internal/agent/a.go"}}')" \
        "an Edit out of scope blocks"
    expect 2 "$(hook mg-scoped '{"tool_name":"Write","tool_input":{"file_path":"'"$WT"'/internal/agent/new.go"}}')" \
        "a Write of a file that does not exist yet is still measured"
    expect 2 "$(hook mg-scoped '{"tool_name":"MultiEdit","tool_input":{"file_path":"'"$WT"'/internal/agent/a.go"}}')" \
        "MultiEdit is gated"
    expect 2 "$(hook mg-scoped '{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"'"$WT"'/internal/agent/n.ipynb"}}')" \
        "NotebookEdit's notebook_path is gated"
    expect 2 "$(hook mg-scoped '{"tool_name":"Edit","tool_input":{"file_path":"/etc/passwd"}}')" \
        "an escape blocks too, collapsed to Claude's 2"

    # Reading is not writing. A guard that blocked Read would stop an agent
    # understanding the code it is scoped to change.
    expect 0 "$(hook mg-scoped '{"tool_name":"Read","tool_input":{"file_path":"/etc/passwd"}}')" \
        "Read is never consulted"
    expect 0 "$(hook mg-scoped '{"tool_name":"Bash","tool_input":{"command":"rm -rf /"}}')" \
        "Bash is out of reach and is not pretended otherwise"
    expect 0 "$(hook mg-none '{"tool_name":"Edit","tool_input":{"file_path":"/etc/passwd"}}')" \
        "an item with no scope blocks nothing in hook mode either"
    expect 0 "$(hook "" '{"tool_name":"Edit","tool_input":{"file_path":"/etc/passwd"}}')" \
        "no item in force blocks nothing in hook mode either"
else
    echo "  SKIP: jq not on PATH; hook-mode cases not run"
fi

# --- Summary ---------------------------------------------------------------
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
