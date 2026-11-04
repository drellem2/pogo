#!/bin/bash
# =============================================================================
# release-local.sh — publish a GitHub Release for an ALREADY-PUSHED tag by
# running goreleaser ON THIS BOX, without GitHub Actions (mg-c0665).
# =============================================================================
#
#   git checkout vX.Y.Z                 # HEAD must be exactly the tag's commit
#   scripts/release-local.sh vX.Y.Z     # publish
#   scripts/release-local.sh --dry-run vX.Y.Z   # every check, then a snapshot
#                                               # build; publishes nothing
#
# WHY: .github/workflows/release.yml runs goreleaser on `ubuntu-latest` when a
# v* tag is pushed, and that was the only hard dependency releases had on
# Actions. On 2026-11-04 the drellem2 account's Actions jobs began to be refused
# (billing). This script is the same goreleaser config, run here, publishing
# through the GitHub API with $GH_TOKEN. Every build is CGO_ENABLED=0, so the
# linux and darwin binaries cross-compile from darwin. release.yml is kept; it
# is no longer REQUIRED (docs/release-process.md).
#
# IT REFUSES, and publishes nothing, when:
#   - the argument is not vX.Y.Z[-suffix]
#   - the work tree is dirty (goreleaser would build something no commit names)
#   - the tag does not exist locally, or is not on origin, or origin's tag
#     names a different commit (the tag must already be pushed — this script
#     never pushes one)
#   - HEAD is not exactly the tag's commit (`git rev-parse "vX.Y.Z^{}"`)
#   - go.mod carries a replace directive (same guard as release.yml)
#   - a GitHub Release for the tag ALREADY EXISTS (no double publish), or
#     gh cannot say whether one does (an unknown answer is not "absent")
#   - a release.yml run for the tag is still queued or in progress (Actions
#     may still run on a public repo; two publishers must not race)
#   - no token is available (GH_TOKEN or GITHUB_TOKEN)
#
# NOT REPRODUCED: SLSA provenance (go-ossf-slsa3-publish.yml) needs Actions
# OIDC to sign, so a locally published release ships without it. The release
# notes header in .goreleaser.yaml says so.
#
# Overridable for tests and for the macguffin copy:
#   RELEASE_REPO        owner/name on GitHub        (default drellem2/pogo)
#   RELEASE_REMOTE      git remote holding the tag  (default origin)
#   RELEASE_GH          gh binary                   (default gh)
#   RELEASE_GORELEASER  goreleaser binary           (default goreleaser)
# =============================================================================

set -euo pipefail

REPO="${RELEASE_REPO:-drellem2/pogo}"
REMOTE="${RELEASE_REMOTE:-origin}"
GH="${RELEASE_GH:-gh}"
GORELEASER="${RELEASE_GORELEASER:-goreleaser}"

refuse() { echo "release-local: REFUSED: $*" >&2; exit 1; }
say() { echo "release-local: $*"; }

DRY_RUN=0
TAG=""
for arg in "$@"; do
    case "$arg" in
        --dry-run) DRY_RUN=1 ;;
        -h|--help) sed -n '2,45p' "$0"; exit 0 ;;
        -*) refuse "unknown flag: $arg" ;;
        *) [ -z "$TAG" ] || refuse "more than one tag given ($TAG, $arg)"; TAG="$arg" ;;
    esac
done
[ -n "$TAG" ] || refuse "usage: $0 [--dry-run] vX.Y.Z"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] \
    || refuse "'$TAG' is not a semver tag vX.Y.Z[-suffix]"

ROOT="$(git rev-parse --show-toplevel)" || refuse "not inside a git work tree"
cd "$ROOT"

# --- token ------------------------------------------------------------------
# goreleaser reads GITHUB_TOKEN; gh reads GH_TOKEN. Existence only — never the value.
if [ -z "${GITHUB_TOKEN:-}" ] && [ -n "${GH_TOKEN:-}" ]; then
    export GITHUB_TOKEN="$GH_TOKEN"
fi
[ -n "${GITHUB_TOKEN:-}" ] || refuse "neither GH_TOKEN nor GITHUB_TOKEN is set"

# --- clean tree ---------------------------------------------------------------
dirty="$(git status --porcelain)" || refuse "git status failed"
[ -z "$dirty" ] || refuse "work tree is dirty:
$dirty"

# --- the tag: local, pushed, same commit, and HEAD is it ----------------------
TAG_COMMIT="$(git rev-parse -q --verify "refs/tags/${TAG}^{commit}")" \
    || refuse "tag $TAG does not exist locally (git fetch $REMOTE --tags?)"
HEAD_COMMIT="$(git rev-parse HEAD)"
[ "$HEAD_COMMIT" = "$TAG_COMMIT" ] \
    || refuse "HEAD is not on $TAG: HEAD=$HEAD_COMMIT, ${TAG}^{}=$TAG_COMMIT — run: git checkout $TAG"

# ls-remote prints the tag object and, for an annotated tag, the peeled
# "^{}" line naming its commit. A lightweight tag has no peeled line.
remote_refs="$(git ls-remote --tags "$REMOTE" "refs/tags/${TAG}" "refs/tags/${TAG}^{}")" \
    || refuse "git ls-remote $REMOTE failed; cannot tell whether $TAG is pushed"
REMOTE_COMMIT="$(printf '%s\n' "$remote_refs" | awk -v r="refs/tags/${TAG}^{}" '$2==r {print $1}')"
if [ -z "$REMOTE_COMMIT" ]; then
    REMOTE_COMMIT="$(printf '%s\n' "$remote_refs" | awk -v r="refs/tags/${TAG}" '$2==r {print $1}')"
fi
[ -n "$REMOTE_COMMIT" ] || refuse "tag $TAG is not on $REMOTE — this script publishes only an already-pushed tag and never pushes one"
[ "$REMOTE_COMMIT" = "$TAG_COMMIT" ] \
    || refuse "$REMOTE's $TAG names $REMOTE_COMMIT, the local tag names $TAG_COMMIT"

# --- go.mod replace directives (mirrors release.yml) --------------------------
if grep -qE '^replace[[:space:]]' go.mod; then
    refuse "go.mod contains replace directives (they break 'go install ...@latest'):
$(grep -n '^replace' go.mod)"
fi

# --- no double publish ----------------------------------------------------------
# `gh release view` exits non-zero for "release not found" AND for every other
# failure (auth, network, wrong repo). Only the not-found text counts as absent.
set +e
view_out="$("$GH" release view "$TAG" --repo "$REPO" --json tagName,url 2>&1)"
view_rc=$?
set -e
if [ "$view_rc" -eq 0 ]; then
    refuse "a GitHub Release for $TAG already exists in $REPO: $view_out"
fi
case "$view_out" in
    *"release not found"*) say "no release for $TAG in $REPO yet" ;;
    *) refuse "cannot tell whether a release for $TAG exists in $REPO (gh exit $view_rc): $view_out" ;;
esac

# --- no race with release.yml ---------------------------------------------------
# On a public repo Actions may still run release.yml for the pushed tag. A run
# that has not finished could publish while this script does, so refuse until
# it completes; a run that FAILED or was refused leaves no release, and the
# check above already covers one that succeeded.
set +e
runs_out="$("$GH" run list --repo "$REPO" --workflow release.yml --limit 50 \
    --json status,headBranch -q ".[] | select(.headBranch == \"$TAG\") | .status" 2>&1)"
runs_rc=$?
set -e
[ "$runs_rc" -eq 0 ] || refuse "cannot list release.yml runs in $REPO (gh exit $runs_rc): $runs_out"
if printf '%s\n' "$runs_out" | grep -qvE '^(completed)?$'; then
    refuse "a release.yml run for $TAG is not finished (status: $(printf '%s' "$runs_out" | tr '\n' ' ')) — wait for it, then re-check: gh release view $TAG --repo $REPO"
fi

say "checks passed: $TAG = HEAD = $REMOTE/$TAG = $TAG_COMMIT"

if [ "$DRY_RUN" -eq 1 ]; then
    say "--dry-run: snapshot build only, nothing is published"
    "$GORELEASER" release --snapshot --clean
    say "--dry-run done; artifacts in dist/"
    exit 0
fi

# GORELEASER_CURRENT_TAG pins the tag goreleaser releases, so a second tag on
# the same commit cannot be picked instead.
GORELEASER_CURRENT_TAG="$TAG" "$GORELEASER" release --clean

# --- confirm what landed ------------------------------------------------------------
say "published; assets on the release:"
"$GH" release view "$TAG" --repo "$REPO" --json assets -q '.assets[].name'
