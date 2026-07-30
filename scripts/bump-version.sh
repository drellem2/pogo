#!/bin/bash
set -e

# =============================================================================
# VERSION BUMP SCRIPT FOR POGO
# =============================================================================
#
# This script handles version bumping for Pogo releases.
# It updates the version constant in internal/version/version.go.
#
# QUICK START (from a clean `main`):
#   ./scripts/bump-version.sh X.Y.Z --commit --tag --push
#
# OFF `main` (a polecat / any branch the refinery will merge) use --commit ONLY,
# and tag the MERGED sha after the merge lands (mg-cef7). --tag REFUSES off main:
# the refinery re-commits what it merges, so a pre-merge tag dangles off a commit
# no branch contains, and a pushed release tag cannot be unpublished.
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
    echo "Usage: $0 <version> [--commit] [--tag] [--push] [--ack-changelog-gaps]"
    echo ""
    echo "Bump version across all Pogo components."
    echo ""
    echo "Arguments:"
    echo "  <version>        Semantic version (e.g., 0.2.0, 1.0.0)"
    echo "  --commit         Automatically create a git commit"
    echo "  --tag            Create annotated git tag (requires --commit)."
    echo "                   REFUSED off 'main': the refinery re-commits what it"
    echo "                   merges, so a pre-merge tag dangles off a commit no"
    echo "                   branch contains (mg-cef7). Off main, use --commit and"
    echo "                   tag the MERGED sha after the merge lands."
    echo "  --push           Push commit and tag to origin (requires --tag)"
    echo "  --ack-changelog-gaps"
    echo "                   Proceed even though some mg-ids in the release range"
    echo "                   have no changelog entry. The ids are printed either"
    echo "                   way; this flag records that you saw them and chose to"
    echo "                   ship anyway (mg-7904)."
    echo ""
    echo "Examples:"
    echo "  $0 0.2.0                        # Update versions and show diff"
    echo "  $0 0.2.0 --commit               # Update versions and commit"
    echo "  $0 0.2.0 --commit --tag         # Update, commit, and tag (main only)"
    echo "  $0 0.2.0 --commit --tag --push  # Full release preparation (main only)"
    echo ""
    echo "Recommended release command, from a clean 'main':"
    echo "  $0 X.Y.Z --commit --tag --push"
    echo ""
    echo "On any other branch (the refinery will re-commit the merge, so a"
    echo "pre-merge tag would dangle — mg-cef7):"
    echo "  $0 X.Y.Z --commit    # then tag the MERGED sha once the merge lands"
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

# Update CHANGELOG.md: move [Unreleased] to [version], and maintain the
# link-reference block.
#
# This used to be one unanchored sed (mg-cef7):
#
#     sed_i "s/## \[Unreleased\]/## [Unreleased]\n\n## [$version] - $date/"
#
# which emitted the heading but NO `[X.Y.Z]:` compare link — so the version
# rendered as literal text in the published changelog — and, because `s///`
# replaces the first match on EVERY matching line, also injected a spurious
# heading into any entry whose prose MENTIONS `## [Unreleased]`. Both failures
# were silent and both recurred on every cut. The logic now lives in
# scripts/roll-changelog.sh, which is anchored, emits the link references, and
# REFUSES rather than producing a half-formed entry.
update_changelog() {
    local version=$1

    if [ ! -f "CHANGELOG.md" ]; then
        echo -e "${YELLOW}Warning: CHANGELOG.md not found, skipping${NC}"
        return
    fi

    "$SCRIPT_DIR/roll-changelog.sh" "$version"
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
    ACK_GAPS=false

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
            --ack-changelog-gaps)
                ACK_GAPS=true
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

    # DANGLING-TAG GATE (mg-cef7).
    #
    # `--tag` tags the LOCAL commit this script just made. That is correct only
    # if that commit is the one that lands on `main`. Off `main` it is not: the
    # branch goes through the refinery, which RE-COMMITS what it merges (v0.7.0's
    # merged commit 4112875 carries committer "pogo refinery"), so the tagged SHA
    # is not the SHA on `main` and the tag dangles off a commit no branch
    # contains. `--push`'s `git push origin main` is wrong off `main` for the same
    # reason. A release tag is externally visible and force-pushing does not
    # unpublish it, so this refuses rather than warns.
    #
    # There is deliberately no override. The correct action off `main` — tag the
    # MERGED SHA after the merge lands — is always available and is printed
    # below; an override's only use would be to produce the broken artifact.
    CURRENT_BRANCH_LABEL="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")"
    if [ "$AUTO_TAG" = true ]; then
        if [ "$CURRENT_BRANCH_LABEL" != "main" ]; then
            echo -e "${RED}Error: refusing --tag on branch '$CURRENT_BRANCH_LABEL' (not main).${NC}" >&2
            echo -e "${RED}  --tag would tag the local pre-merge commit. This branch goes through${NC}" >&2
            echo -e "${RED}  the refinery, which RE-COMMITS what it merges, so the tagged SHA is${NC}" >&2
            echo -e "${RED}  not the SHA that lands on main and the tag would dangle off a commit${NC}" >&2
            echo -e "${RED}  no branch contains. A pushed release tag cannot be unpublished.${NC}" >&2
            echo "" >&2
            echo -e "${YELLOW}  Do this instead — bump here, tag the MERGED commit afterwards:${NC}" >&2
            echo -e "${YELLOW}    ./scripts/bump-version.sh $NEW_VERSION --commit${NC}" >&2
            echo -e "${YELLOW}    # push, submit to the refinery, wait for the merge, then on the${NC}" >&2
            echo -e "${YELLOW}    # MERGED sha (note: the tagging step must be done by something${NC}" >&2
            echo -e "${YELLOW}    # that OUTLIVES the merge — see CONTRIBUTING.md 'Releases'):${NC}" >&2
            echo -e "${YELLOW}    git fetch origin main${NC}" >&2
            echo -e "${YELLOW}    git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION' origin/main${NC}" >&2
            echo -e "${YELLOW}    git push origin v$NEW_VERSION${NC}" >&2
            echo -e "${YELLOW}    git ls-remote --tags origin | grep -q 'v$NEW_VERSION' || echo 'TAG NOT PUBLISHED'${NC}" >&2
            exit 1
        fi
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

    # 0. CHANGELOG COVERAGE (mg-7904).
    #    assemble-changelog.sh's LOUD-EMPTY guard only refuses a changelog with
    #    ZERO entries — a weaker property than CONTRIBUTING's per-change rule.
    #    Under that guard alone a cut ships a changelog describing part of the
    #    release, silently. This reports the ids in the range that nothing
    #    describes and requires an explicit acknowledgement to proceed, so the
    #    number reaches whoever is CUTTING rather than whoever is merging.
    echo "Checking changelog coverage for the release range..."
    COVERAGE_STATUS=0
    "$SCRIPT_DIR/changelog-coverage.sh" || COVERAGE_STATUS=$?
    if [ "$COVERAGE_STATUS" -eq 1 ]; then
        if [ "$ACK_GAPS" = true ]; then
            echo ""
            echo -e "${YELLOW}Proceeding with known changelog gaps (--ack-changelog-gaps).${NC}"
            echo -e "${YELLOW}The release will ship without entries for the ids listed above.${NC}"
        else
            echo ""
            echo -e "${RED}Error: refusing to cut a release with undescribed changes.${NC}" >&2
            echo -e "${RED}  Either write the missing changelog.d/ fragments, or re-run with${NC}" >&2
            echo -e "${RED}  --ack-changelog-gaps to ship anyway.${NC}" >&2
            exit 1
        fi
    elif [ "$COVERAGE_STATUS" -ne 0 ]; then
        echo -e "${RED}Error: changelog coverage check failed (exit $COVERAGE_STATUS)${NC}" >&2
        exit 1
    fi
    echo ""

    echo "Updating version files..."

    # 1. Update internal/version/version.go
    echo "  • internal/version/version.go"
    update_file "internal/version/version.go" \
        "Version = \"$CURRENT_VERSION\"" \
        "Version = \"$NEW_VERSION\""

    # 2. Assemble changelog.d/ fragments into [Unreleased], then roll it to the
    #    new version. Assembly is a HARD gate (mg-d917): if it produces an empty
    #    [Unreleased] it exits non-zero and set -e aborts the release here —
    #    never cut a release with no changelog entries.
    echo "  • changelog.d/ fragments → CHANGELOG.md"
    "$SCRIPT_DIR/assemble-changelog.sh"

    # 3. Update CHANGELOG.md
    echo "  • CHANGELOG.md"
    update_changelog "$NEW_VERSION"

    # 4. PROVE the roll produced a well-formed link-reference block (mg-cef7).
    #    roll-changelog.sh emitting the link is one thing; the file being
    #    CONSISTENT afterwards is another, and the whole class of defect here is
    #    a cut that succeeds and ships a wrong artifact. This compares the
    #    heading SET against the link-reference SET — never the counts, which
    #    misdiagnose (see the header of changelog-links.sh). set -e aborts the
    #    cut on any finding.
    echo "  • verifying changelog link references"
    "$SCRIPT_DIR/changelog-links.sh"

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

        # Stage assembled/removed changelog fragments (mg-d917).
        if [ -d "changelog.d" ]; then
            git add -A changelog.d
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
            # Verify against the REMOTE (mg-cef7). A local tag proves nothing
            # about what was published, and the release workflow triggers on the
            # pushed tag — so an unpublished tag is a silently un-cut release.
            if git ls-remote --tags origin 2>/dev/null | grep -q "refs/tags/v$NEW_VERSION\$"; then
                echo -e "${GREEN}✓ Tag v$NEW_VERSION confirmed on origin${NC}"
                echo ""
                echo -e "${GREEN}Release v$NEW_VERSION initiated!${NC}"
                echo "GitHub Actions will build artifacts when the tag is pushed."
            else
                echo -e "${RED}✗ Tag v$NEW_VERSION is NOT on origin — the release did not trigger.${NC}" >&2
                echo -e "${RED}  Re-run: git push origin v$NEW_VERSION${NC}" >&2
                exit 1
            fi
        elif [ "$AUTO_TAG" = true ]; then
            echo "Next steps:"
            echo "  git push origin main"
            echo "  git push origin v$NEW_VERSION"
            echo "  git ls-remote --tags origin | grep v$NEW_VERSION   # confirm it published"
        elif [ "$CURRENT_BRANCH_LABEL" != "main" ]; then
            # Off main the tag MUST come after the merge, on the merged sha — and
            # the merging worker cannot do it: pogod stops a polecat within ~3s of
            # merge success, so any post-merge step it still intends loses that
            # race (both v0.8.0 cut attempts did). Hand the step to something that
            # outlives the merge.
            echo "Next steps (on branch '$CURRENT_BRANCH_LABEL' — do NOT tag this commit):"
            echo "  git push origin $CURRENT_BRANCH_LABEL"
            echo "  pogo refinery submit $CURRENT_BRANCH_LABEL --repo=<repo> --target=main"
            echo ""
            echo "Then, once the merge has LANDED, tag the MERGED sha. This step"
            echo "outlives the merging worker, so it belongs to a coordinator or a"
            echo "separately-dispatched follow-up — not to whoever ran this script:"
            echo "  git fetch origin main"
            echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION' origin/main"
            echo "  git push origin v$NEW_VERSION"
            echo "  git ls-remote --tags origin | grep v$NEW_VERSION   # confirm it published"
        else
            echo "Next steps:"
            echo "  git push origin main"
            echo "  git tag -a v$NEW_VERSION -m 'Release v$NEW_VERSION'"
            echo "  git push origin v$NEW_VERSION"
            echo "  git ls-remote --tags origin | grep v$NEW_VERSION   # confirm it published"
        fi
    else
        echo "Review the changes above."
        echo ""
        echo "To commit and release:"
        echo "  $0 $NEW_VERSION --commit --tag --push"
    fi
}

main "$@"
