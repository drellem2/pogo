#!/bin/bash
# mg-scope-guard.sh — refuse edits outside a work item's declared scope (mg-f1d5).
#
# pogo's polecat prompt says "Stay scoped. Only work on your assigned task."
# Nothing enforced it. Every agent runs `claude --dangerously-skip-permissions`
# (docs/polecat-permissions.md), so an edit anywhere on the machine is one tool
# call away and a wandering polecat is caught at review time or not at all.
#
# This is the fence. A work item declares its editable surface in its body:
#
#     scope: cmd/pogod/** internal/agent/** docs/*.md
#
# and this script, wired as a Claude Code PreToolUse hook, refuses Edit, Write,
# MultiEdit and NotebookEdit anywhere else.
#
# OPT-IN AT EVERY STEP. No item in force, or an item that declares no scope,
# means the guard allows everything — an agent that never opted in is never
# blocked, and neither is an item nobody scoped. It is wired into no agent by
# default; see docs/mg-scope.md.
#
# THE UNCOVERED HALF, STATED RATHER THAN IMPLIED: writes through Bash are out of
# reach. A hook sees the command line, not what the command will touch, so
# `sh -c 'echo x > /etc/passwd'` is undecidable from here and is not decided.
# This stops the accident, not the determined agent.
#
# Two modes:
#
#   mg-scope-guard.sh                 hook mode: PreToolUse JSON on stdin.
#                                     Claude's contract — exit 0 proceeds, 2 blocks.
#   mg-scope-guard.sh PATH...         check-scope mode: exit 0 in scope,
#                                     9 out of scope, 11 escape. macguffin's
#                                     shared vocabulary, so another tool can ask.
#
# Which item is in force is worked out, never declared per call:
#   $MG_SCOPE_ITEM, else a .mg-scope file at the worktree root, else none.
#
# Environment (all optional; the last three exist so the tests never touch the
# developer's live store):
#   MG_SCOPE_ITEM   the work item id in force
#   MG_SCOPE_ROOT   the worktree root; default `git rev-parse --show-toplevel`
#   MG_SCOPE_MG     the mg binary to ask; default `mg`
#   MG_SCOPE_DEBUG  set to anything to trace decisions on stderr

set -u

# Exit codes. Hook mode collapses every refusal to 2, because that is the only
# refusal Claude understands; check-scope mode keeps them apart, because a
# containment failure and a routine refusal are different facts.
readonly EX_OK=0
readonly EX_USAGE=1
readonly EX_SCOPE=9
readonly EX_ESCAPE=11
readonly EX_BLOCK=2

HOOK_MODE=0
[ "$#" -eq 0 ] && HOOK_MODE=1

debug() { [ -n "${MG_SCOPE_DEBUG:-}" ] && echo "mg-scope-guard: $*" >&2; return 0; }

# refuse reports a decision and exits. In hook mode every refusal is a 2; in
# check-scope mode the caller gets the code that says which refusal it was.
refuse() {
    local code="$1"; shift
    echo "mg-scope-guard: $*" >&2
    if [ "$HOOK_MODE" -eq 1 ]; then exit "$EX_BLOCK"; fi
    exit "$code"
}

# --- Which item is in force -------------------------------------------------
#
# Absence is the common case and must be cheap: an unscoped agent pays one
# variable lookup and one stat, then proceeds.

ROOT="${MG_SCOPE_ROOT:-}"
if [ -z "$ROOT" ]; then
    ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || ROOT=""
fi

ITEM="${MG_SCOPE_ITEM:-}"
if [ -z "$ITEM" ] && [ -n "$ROOT" ] && [ -r "$ROOT/.mg-scope" ]; then
    # First non-empty, non-comment line. A file with a stray blank first line is
    # a file somebody edited, not a file that means "no item".
    ITEM="$(grep -v '^[[:space:]]*\(#.*\)\?$' "$ROOT/.mg-scope" 2>/dev/null | head -1 | tr -d '[:space:]')"
fi

if [ -z "$ITEM" ]; then
    debug "no item in force; allowing"
    exit "$EX_OK"
fi

# Past here the operator has opted in, so a guard that cannot decide REFUSES
# rather than waving things through. An opted-in agent that is silently
# unenforced is the failure this script exists to remove, and it is invisible;
# a loud refusal naming its own fix is not.

if [ -z "$ROOT" ]; then
    refuse "$EX_USAGE" "item $ITEM is in force but there is no worktree root here." \
        "Run inside the git worktree, or set MG_SCOPE_ROOT."
fi

MG="${MG_SCOPE_MG:-mg}"
if ! command -v "$MG" >/dev/null 2>&1; then
    refuse "$EX_USAGE" "item $ITEM is in force but '$MG' is not on PATH." \
        "Install macguffin, set MG_SCOPE_MG, or unset MG_SCOPE_ITEM to opt out."
fi

# --- What the item allows ---------------------------------------------------
#
# Scope lives in the body as `scope:` carrier lines, which is macguffin's
# existing convention (`workflow:`, `stage:`, `gh:`). Several lines accumulate;
# each carries whitespace-separated patterns. Declaring the surface where the
# work is described keeps one source of truth, and `mg show` is how anything
# reads it.

BODY="$("$MG" show "$ITEM" 2>&1)" || \
    refuse "$EX_USAGE" "cannot read work item $ITEM: $(echo "$BODY" | head -1)"

PATTERNS=()
while IFS= read -r line; do
    [ -n "$line" ] && PATTERNS+=("$line")
done < <(printf '%s\n' "$BODY" \
    | sed -n 's/^[[:space:]]*scope:[[:space:]]*//p' \
    | tr -s '[:space:]' '\n')

if [ "${#PATTERNS[@]}" -eq 0 ]; then
    debug "item $ITEM declares no scope; allowing"
    exit "$EX_OK"
fi
debug "item $ITEM scope: ${PATTERNS[*]}"

# --- Path arithmetic --------------------------------------------------------

# normalise resolves . and .. lexically. Lexically, not with realpath: a Write
# names a file that does not exist yet, and `realpath -e` on it fails — which
# would turn every new-file creation into an unmeasurable path and, past the
# opt-in above, a refusal.
#
# Built by string suffix rather than by array index because macOS ships bash
# 3.2, where `out[-1]` is a literal subscript and not the last element.
normalise() {
    local path="$1" out="" part
    [ "${path#/}" = "$path" ] && path="$PWD/$path"
    local IFS='/'
    for part in $path; do
        case "$part" in
            ''|.) ;;
            ..) out="${out%/*}" ;;
            *) out="$out/$part" ;;
        esac
    done
    printf '%s' "${out:-/}"
}

# glob_match answers a pattern against a worktree-relative path.
#
# `**` crosses directory separators and `*` does not, which is the distinction
# `[[ str == pat ]]` does not make — bash's `*` swallows slashes, so `docs/*`
# there would match `docs/a/b/c.md` and a narrow pattern would silently be a
# wide one. So the pattern is compiled to a regex instead.
glob_match() {
    local pat="$1" path="$2" re="" i ch
    for (( i=0; i<${#pat}; i++ )); do
        ch="${pat:i:1}"
        case "$ch" in
            '*')
                if [ "${pat:i+1:1}" = '*' ]; then
                    re="$re.*"; ((i++))
                else
                    re="$re[^/]*"
                fi
                ;;
            '?') re="$re[^/]" ;;
            '.'|'^'|'$'|'('|')'|'['|']'|'{'|'}'|'+'|'|'|'\\') re="$re\\$ch" ;;
            *) re="$re$ch" ;;
        esac
    done
    [[ "$path" =~ ^${re}$ ]]
}

# in_scope: exact file, directory prefix, or glob — the three forms Macmuffin's
# spec names, and the three a person actually types.
in_scope() {
    local path="$1" pat
    for pat in "${PATTERNS[@]}"; do
        pat="${pat%/}"
        [ "$path" = "$pat" ] && return 0
        [ "${path#"$pat"/}" != "$path" ] && return 0
        glob_match "$pat" "$path" && return 0
    done
    return 1
}

# check decides one path and refuses on the first that fails.
check() {
    local raw="$1" abs rel
    abs="$(normalise "$raw")"
    local rootn; rootn="$(normalise "$ROOT")"

    # An escape is a containment failure and is reported as one. A path outside
    # the worktree was never in scope, but calling it "out of scope" would file
    # a break-out beside a typo.
    if [ "$abs" != "$rootn" ] && [ "${abs#"$rootn"/}" = "$abs" ]; then
        refuse "$EX_ESCAPE" "$abs is outside the worktree ($rootn); item $ITEM"
    fi

    rel="${abs#"$rootn"/}"
    [ "$abs" = "$rootn" ] && rel="."

    if ! in_scope "$rel"; then
        refuse "$EX_SCOPE" "$rel is not in the scope of $ITEM (${PATTERNS[*]}).
Widen it with: mg edit $ITEM  (add a 'scope:' line to the body), or work inside the scope."
    fi
    debug "$rel in scope"
}

# --- Hook mode --------------------------------------------------------------

if [ "$HOOK_MODE" -eq 1 ]; then
    if ! command -v jq >/dev/null 2>&1; then
        refuse "$EX_USAGE" "item $ITEM is in force but jq is not on PATH; the hook cannot read its input." \
            "Install jq, or unset MG_SCOPE_ITEM to opt out."
    fi

    INPUT="$(cat)"
    TOOL="$(printf '%s' "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)"

    case "$TOOL" in
        Edit|Write|MultiEdit|NotebookEdit) ;;
        *) debug "tool ${TOOL:-<none>} does not write; allowing"; exit "$EX_OK" ;;
    esac

    # NotebookEdit names notebook_path; the rest name file_path. Asking for both
    # and taking whichever is present costs one jq call and survives a tool that
    # renames its field.
    while IFS= read -r target; do
        [ -z "$target" ] && continue
        check "$target"
    done < <(printf '%s' "$INPUT" | jq -r '[.tool_input.file_path?, .tool_input.notebook_path?] | .[] | select(. != null and . != "")' 2>/dev/null)

    exit "$EX_OK"
fi

# --- check-scope mode -------------------------------------------------------

for arg in "$@"; do
    check "$arg"
done
exit "$EX_OK"
