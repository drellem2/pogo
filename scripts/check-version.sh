#!/bin/bash
set -e

# =============================================================================
# VERSION CONSISTENCY CHECK
# =============================================================================
#
# Ensures the version in internal/version/version.go has not already been
# released (i.e., no existing git tag matches the current version).
# This prevents accidentally shipping a release with a duplicate version.
#
# Used in CI to catch forgotten version bumps.
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

VERSION_FILE="$REPO_ROOT/internal/version/version.go"

if [ ! -f "$VERSION_FILE" ]; then
    echo "Error: $VERSION_FILE not found"
    exit 1
fi

# Extract version from version.go
CURRENT_VERSION=$(grep 'Version = ' "$VERSION_FILE" | sed 's/.*"\(.*\)".*/\1/')

if [ -z "$CURRENT_VERSION" ]; then
    echo "Error: Could not parse version from $VERSION_FILE"
    exit 1
fi

echo "Current version: $CURRENT_VERSION"

# Check if a git tag already exists for this version
TAG="v$CURRENT_VERSION"
if git tag -l "$TAG" | grep -q "^$TAG$"; then
    echo "Error: Tag $TAG already exists. Bump the version before releasing."
    echo ""
    echo "Run: ./scripts/bump-version.sh X.Y.Z"
    exit 1
fi

echo "OK: No existing tag for $TAG"
