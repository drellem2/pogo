#!/bin/bash
set -e

# =============================================================================
# VERSION CONSISTENCY CHECK
# =============================================================================
#
# Checks internal/version/version.go against the release tags:
#
#   1. It holds EXACTLY ONE `var Version = "X.Y.Z"` declaration, and X.Y.Z is
#      strict semver. The version is read by matching the declaration, not by a
#      text grep for 'Version = ', which a comment quoting that string once
#      turned into a three-line answer (mg-3225). See scripts/lib/version.sh.
#
#   2. X.Y.Z is NOT BEHIND the newest vA.B.C release tag. A release is bumped
#      on a release branch, tagged on the merged sha, and then back-ported to
#      main (docs/release-process.md). A main whose version.go reads older
#      than a published tag is a back-port that never landed, and every
#      unstamped build off it reports a version that was already superseded.
#
#   --require-untagged additionally refuses when vX.Y.Z already exists as a
#   tag. That is the check this script USED to run unconditionally, and it is
#   why CI had it disabled (`if: false`, mg-2cc8): on main, version.go equals
#   the latest release from the moment that release is tagged until the next
#   cut, so "no tag exists for this version" fails on every push in between.
#   It is correct only just BEFORE a cut, so it is opt-in.
#
# A check that never runs reads as protection and gives none (mg-cb8dc), so
# this runs in CI on every push: see the version-check job in
# .github/workflows/ci.yml. scripts/check-version_test.sh is its test.
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
source "$SCRIPT_DIR/lib/version.sh"

REQUIRE_UNTAGGED=false
for arg in "$@"; do
    case "$arg" in
        --require-untagged) REQUIRE_UNTAGGED=true ;;
        *) echo "Usage: $0 [--require-untagged]" >&2; exit 2 ;;
    esac
done

VERSION_FILE="$REPO_ROOT/internal/version/version.go"

if [ ! -f "$VERSION_FILE" ]; then
    echo "Error: $VERSION_FILE not found"
    exit 1
fi

CURRENT_VERSION="$(read_version "$VERSION_FILE")" || exit 1

if ! [[ $CURRENT_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Error: version '$CURRENT_VERSION' in $VERSION_FILE is not MAJOR.MINOR.PATCH"
    exit 1
fi

echo "Current version: $CURRENT_VERSION"

# Newest strict-semver release tag, by version order rather than by date.
LATEST_TAG="$(git -C "$REPO_ROOT" tag -l 'v*' \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
    | sed 's/^v//' \
    | sort -t. -k1,1n -k2,2n -k3,3n \
    | tail -1)"

if [ -z "$LATEST_TAG" ]; then
    # Not an error: a shallow clone fetches no tags. Say so rather than pass
    # silently — the CI job checks out with fetch-depth: 0 for this reason.
    echo "Warning: no vX.Y.Z tags visible; cannot compare against releases"
else
    NEWER="$(printf '%s\n%s\n' "$CURRENT_VERSION" "$LATEST_TAG" \
        | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)"
    if [ "$NEWER" != "$CURRENT_VERSION" ]; then
        echo "Error: version.go says $CURRENT_VERSION but v$LATEST_TAG is already released."
        echo "The release bump was not back-ported to this branch (docs/release-process.md)."
        exit 1
    fi
    echo "OK: not behind the newest release tag v$LATEST_TAG"
fi

if [ "$REQUIRE_UNTAGGED" = true ]; then
    TAG="v$CURRENT_VERSION"
    if git -C "$REPO_ROOT" tag -l "$TAG" | grep -qx "$TAG"; then
        echo "Error: Tag $TAG already exists. Bump the version before releasing."
        echo ""
        echo "Run: ./scripts/bump-version.sh X.Y.Z"
        exit 1
    fi
    echo "OK: No existing tag for $TAG"
fi
