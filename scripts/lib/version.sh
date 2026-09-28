#!/bin/bash
# =============================================================================
# READ / WRITE THE VERSION DECLARATION IN internal/version/version.go
# =============================================================================
#
# Source this file; do not execute it. Used by bump-version.sh and
# check-version.sh, which used to each carry their own copy of
#
#     grep 'Version = ' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/'
#
# That is a TEXT grep, so any other line containing the string matched too. At
# the v0.11.0 cut a comment quoting the pattern verbatim (mg-3141) made it
# return three lines, and bump-version.sh aborted with "sed: unterminated
# substitute pattern" (mg-3225). check-version.sh had the same defect and
# nobody noticed, because its CI job was `if: false` (mg-cb8dc).
#
# These functions match the DECLARATION — a line that begins with
# `var Version = "` (or `const Version = "`) — so a comment, which begins with
# `//`, cannot match however it is worded; and they REFUSE unless exactly one
# line matches rather than hand a caller a multi-line answer to misuse.
# =============================================================================

# The declaration, anchored at both ends. Comments cannot match: after optional
# leading whitespace the line must start with `var` or `const`, not `//`. Blanks
# are a literal space-or-tab bracket rather than [[:space:]] so the pattern means
# the same thing to grep -E and to BSD awk, gawk and mawk.
VERSION_DECL_RE=$'^[ \t]*(var|const)[ \t]+Version[ \t]*=[ \t]*"[^"]*"[ \t]*$'

# read_version <version.go>
# Prints the quoted value of the single Version declaration. Returns 1 with a
# message on stderr if the declaration is missing or appears more than once.
read_version() {
    local file=$1
    local lines count
    lines="$(grep -E "$VERSION_DECL_RE" "$file" || true)"
    if [ -z "$lines" ]; then
        count=0
    else
        count="$(printf '%s\n' "$lines" | wc -l | tr -d ' ')"
    fi
    if [ "$count" != 1 ]; then
        echo "Error: expected exactly one 'var Version = \"...\"' (or const) declaration in $file, found $count" >&2
        [ "$count" -gt 0 ] && printf '%s\n' "$lines" | sed 's/^/  /' >&2
        return 1
    fi
    printf '%s\n' "$lines" | sed -E 's/^[^"]*"([^"]*)".*$/\1/'
}

# write_version <version.go> <new-version>
# Rewrites the value of the single Version declaration and nothing else in the
# file — a comment quoting the old value is left alone. Refuses (returns 1)
# under the same conditions as read_version.
write_version() {
    local file=$1 new=$2 tmp
    read_version "$file" >/dev/null || return 1
    tmp="$(mktemp "${file}.XXXXXX")" || return 1
    if ! awk -v re="$VERSION_DECL_RE" -v new="$new" '
        $0 ~ re { sub(/"[^"]*"/, "\"" new "\"") }
        { print }
    ' "$file" > "$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    # cat rather than mv: mktemp creates 0600, and version.go keeps its mode.
    cat "$tmp" > "$file"
    rm -f "$tmp"
}
