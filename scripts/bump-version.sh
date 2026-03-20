#!/bin/bash
set -e

# =============================================================================
# VERSION BUMP SCRIPT FOR POGO
# =============================================================================
#
# This script handles version bumping for Pogo releases.
# It updates the version constant in internal/version/version.go.
#
# QUICK START:
#   ./scripts/bump-version.sh X.Y.Z --commit --tag --push
#
# WHAT IT UPDATES:
#   - internal/version/version.go  - CLI version constant
#   - CHANGELOG.md                 - Creates release entry from [Unreleased]
#
# =============================================================================

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib/common.sh"

# Usage message
usage() {
    echo "Usage: $0 <version> [--commit] [--tag] [--push]"
    echo ""
    echo "Bump version across all Pogo components."
    echo ""
    echo "Arguments:"
    echo "  <version>        Semantic version (e.g., 0.2.0, 1.0.0)"
    echo "  --commit         Automatically create a git commit"
    echo "  --tag            Create annotated git tag (requires --commit)"
    echo "  --push           Push commit and tag to origin (requires --tag)"
    echo ""
    echo "Examples:"
    echo "  $0 0.2.0                        # Update versions and show diff"
    echo "  $0 0.2.0 --commit               # Update versions and commit"
    echo "  $0 0.2.0 --commit --tag         # Update, commit, and tag"
    echo "  $0 0.2.0 --commit --tag --push  # Full release preparation"
    echo ""
    echo "Recommended release command:"
    echo "  $0 X.Y.Z --commit --tag --push"
    exit 1
}

# Validate semantic versioning
validate_version() {
    local version=$1
    if ! [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}Error: Invalid version format '$version'${NC}"
        echo "Expected semantic version format: MAJOR.MINOR.PATCH (e.g., 0.2.0)"
        exit 1
    fi
}

# Get current version from version.go
get_current_version() {
    grep 'Version = ' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/'
}

# Update CHANGELOG.md: move [Unreleased] to [version]
update_changelog() {
    local version=$1
    local date=$(date +%Y-%m-%d)

    if [ ! -f "CHANGELOG.md" ]; then
        echo -e "${YELLOW}Warning: CHANGELOG.md not found, skipping${NC}"
        return
    fi

    # Check if there's an [Unreleased] section
    if ! grep -q "## \[Unreleased\]" CHANGELOG.md; then
        echo -e "${YELLOW}Warning: No [Unreleased] section in CHANGELOG.md${NC}"
        echo -e "${YELLOW}You may need to manually update CHANGELOG.md${NC}"
        return
    fi

    sed_i "s/## \[Unreleased\]/## [Unreleased]\n\n## [$version] - $date/" CHANGELOG.md
}

# Main script
main() {
    if [ $# -lt 1 ]; then
        usage
    fi

    NEW_VERSION=$1
    AUTO_COMMIT=false
    AUTO_TAG=false
    AUTO_PUSH=false

    # Parse flags
    shift
    while [ $# -gt 0 ]; do
        case "$1" in
            --commit)
                AUTO_COMMIT=true
                ;;
            --tag)
                AUTO_TAG=true
                ;;
            --push)
                AUTO_PUSH=true
                ;;
            *)
                echo -e "${RED}Error: Unknown option '$1'${NC}"
                usage
                ;;
        esac
        shift
    done

    # Validate flag dependencies
    if [ "$AUTO_TAG" = true ] && [ "$AUTO_COMMIT" = false ]; then
        echo -e "${RED}Error: --tag requires --commit${NC}"
        exit 1
    fi
    if [ "$AUTO_PUSH" = true ] && [ "$AUTO_TAG" = false ]; then
        echo -e "${RED}Error: --push requires --tag${NC}"
        exit 1
    fi

    # Validate version format
    validate_version "$NEW_VERSION"

    # Check if we're in the repo root
    if [ ! -f "internal/version/version.go" ]; then
        echo -e "${RED}Error: Must run from repository root${NC}"
        exit 1
    fi

    # Get current version
    CURRENT_VERSION=$(get_current_version)

    echo -e "${YELLOW}Bumping version: $CURRENT_VERSION → $NEW_VERSION${NC}"
    echo ""

    # Check for uncommitted changes
    if ! git diff-index --quiet HEAD --; then
        echo -e "${YELLOW}Warning: You have uncommitted changes${NC}"
        if [ "$AUTO_COMMIT" = true ]; then
            echo -e "${RED}Error: Cannot auto-commit with existing uncommitted changes${NC}"
            exit 1
        fi
        read -p "Continue anyway? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi

    echo "Updating version files..."

    # 1. Update internal/version/version.go
    echo "  • internal/version/version.go"
    update_file "internal/version/version.go" \
        "Version = \"$CURRENT_VERSION\"" \
        "Version = \"$NEW_VERSION\""

    # 2. Update CHANGELOG.md
    echo "  • CHANGELOG.md"
    update_changelog "$NEW_VERSION"

    echo ""
    echo -e "${GREEN}✓ Version updated to $NEW_VERSION${NC}"
    echo ""

    # Show diff
    echo "Changed files:"
    git diff --stat
    echo ""

    # Verify version matches
    echo "Verifying version consistency..."
    VERSION_GO=$(grep 'Version = ' internal/version/version.go | sed 's/.*"\(.*\)".*/\1/')

    if [ "$VERSION_GO" = "$NEW_VERSION" ]; then
        echo -e "${GREEN}✓ Version matches: $NEW_VERSION${NC}"
    else
        echo -e "${RED}✗ Version mismatch detected!${NC}"
        echo "  version.go: $VERSION_GO"
        exit 1
    fi

    echo ""

    # Auto-commit if requested
    if [ "$AUTO_COMMIT" = true ]; then
        echo "Creating git commit..."

        git add internal/version/version.go

        if [ -f "CHANGELOG.md" ]; then
            git add CHANGELOG.md
        fi

        git commit -m "chore: Bump version to $NEW_VERSION

Updated version:
- pogo CLI: $CURRENT_VERSION → $NEW_VERSION

Generated by scripts/bump-version.sh"

        echo -e "${GREEN}✓ Commit created${NC}"
        echo ""

        # Auto-tag if requested
        if [ "$AUTO_TAG" = true ]; then
            echo "Creating git tag v$NEW_VERSION..."
            git tag -a "v$NEW_VERSION" -m "Release v$NEW_VERSION"
            echo -e "${GREEN}✓ Tag created${NC}"
            echo ""
        fi

        # Auto-push if requested
        if [ "$AUTO_PUSH" = true ]; then
            echo "Pushing to origin..."
            git push origin main
            git push origin "v$NEW_VERSION"
            echo -e "${GREEN}✓ Pushed to origin${NC}"
            echo ""
            echo -e "${GREEN}Release v$NEW_VERSION initiated!${NC}"
            echo "GitHub Actions will build artifacts when the tag is pushed."
        elif [ "$AUTO_TAG" = true ]; then
            echo "Next steps:"
            echo "  git push origin main"
            echo "  git push origin v$NEW_VERSION"
        else
            echo "Next steps:"
            echo "  git push origin main"
            echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION'"
            echo "  git push origin v$NEW_VERSION"
        fi
    else
        echo "Review the changes above."
        echo ""
        echo "To commit and release:"
        echo "  $0 $NEW_VERSION --commit --tag --push"
    fi
}

main "$@"
