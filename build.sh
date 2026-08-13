#!/bin/bash
# build.sh — format, test, and compile every binary under cmd/.
#
# Binaries land in ./bin (gitignored); build.sh does NOT `go install` into
# GOBIN. build.sh is executed in polecat worktrees and as the refinery's
# quality gate, so a `go install` here silently overwrites the host's live
# ~/go/bin/pogod with an unreviewed branch build — a later pogod restart would
# then launch whatever branch happened to compile last (mg-b630).
#
# Usage:
#   ./build.sh                # fmt + test + build into ./bin
#   ./build.sh --skip-tests   # fmt + build
#   ./build.sh --install      # also `go install ./cmd/...` into GOBIN (opt-in)
#
# Environment:
#   POGO_BUILD_DIR   Output directory for the built binaries. Default: ./bin
set -e

skip_tests=false
do_install=false

usage() {
  cat <<'EOF'
Usage:
  ./build.sh                # fmt + test + build into ./bin
  ./build.sh --skip-tests   # fmt + build
  ./build.sh --install      # also `go install ./cmd/...` into GOBIN (opt-in)

Environment:
  POGO_BUILD_DIR   Output directory for the built binaries. Default: ./bin
EOF
}

for arg in "$@"; do
  case "$arg" in
    --skip-tests)
      skip_tests=true
      ;;
    --install)
      do_install=true
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "build.sh: unknown flag: $arg" >&2
      usage >&2
      exit 2
      ;;
  esac
done

build_dir="${POGO_BUILD_DIR:-./bin}"

# ---------------------------------------------------------------------------
# THE BUILD STAMP (mg-3141)
#
# Until this existed, internal/version's Commit/Branch fields were set by
# goreleaser and by nothing else — and goreleaser is the RELEASE path, not the
# path that produces the binaries this fleet runs. Both paths that do (here, and
# scripts/pogo-self-deploy) were plain `go build` with no -ldflags, so every
# local binary reported commit:"" branch:"" and could not say what it contained.
# Four separate "is this fix live?" questions on 2026-08-13 were answered by
# file mtimes and by inference because of it.
#
# WHY NOT RELY ON Go's AUTOMATIC vcs.revision, which `go build` records for
# free and which pogo-self-deploy's installed_rev already reads: it walks UP
# from the build directory to find a repo. A polecat worktree lives under
# ~/.pogo, which is itself a git repo — so a build there stamps ~/.pogo's HEAD,
# a SHA that does not exist in the pogo repo at all. Measured, not feared. The
# automatic stamp is not merely missing the branch; here it can name a foreign
# repository with complete confidence. These flags name the repo we chose.
#
# Kept deliberately duplicated in scripts/pogo-self-deploy rather than factored
# into scripts/lib/: that script is standalone by architectural ruling (mg-6afa
# — the thing that redeploys pogod must not grow dependencies), so the guard
# against the two copies drifting is scripts/stamp_test.sh, which BUILDS via
# each path and RUNS the binary. A wrong -X symbol path is a silent no-op that
# looks correct in the build command and fails only at runtime, so reading the
# flag string would not have caught it.
# ---------------------------------------------------------------------------
version_ldflags() {
  local repo="${1:-.}"
  local pkg="github.com/drellem2/pogo/internal/version"
  local commit branch dirty
  commit="$(git -C "$repo" rev-parse HEAD 2>/dev/null || true)"
  # No commit means no repo (a tarball, an exported tree). Emit nothing rather
  # than stamping a placeholder: an -X Commit=unknown would report source
  # "ldflags", claiming a build script vouched for a revision it never read.
  if [ -z "$commit" ]; then
    echo ""
    return 0
  fi
  branch="$(git -C "$repo" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
  [ -n "$branch" ] || branch="unknown"
  if [ -n "$(git -C "$repo" status --porcelain 2>/dev/null)" ]; then dirty=true; else dirty=false; fi
  printf -- '-X %s.Commit=%s -X %s.Build=%s -X %s.Branch=%s -X %s.Dirty=%s' \
    "$pkg" "$commit" "$pkg" "${commit:0:7}" "$pkg" "$branch" "$pkg" "$dirty"
}

# POGO_BUILD_NO_STAMP=1 builds WITHOUT the flags. It exists for the positive
# control in scripts/stamp_test.sh: a stamping check that has never been seen
# going red is not known to work.
if [ "${POGO_BUILD_NO_STAMP:-}" = "1" ]; then
  ldflags=""
  echo "build.sh: POGO_BUILD_NO_STAMP=1 — building UNSTAMPED (binaries will report source=none or a Go vcs stamp)"
else
  # The CWD, not $(dirname $0). Everything else in this file compiles what is
  # under the cwd — ./fmt.sh, ./test.sh, `go build ./cmd/...` — so deriving the
  # stamp from the script's own directory would let the two come apart and
  # produce a binary carrying a confident revision from a tree it was not built
  # from. That is this ticket's defect wearing a fix's clothes.
  ldflags="$(version_ldflags .)"
  if [ -z "$ldflags" ]; then
    # Announce the degraded state instead of silently producing the artifact
    # this ticket was filed about. Both repos could always produce an unstamped
    # binary; only one of them said so.
    echo "build.sh: no git revision available — binaries will be UNSTAMPED"
  fi
fi

# Per-step timing (mg-eed9). build.sh is the refinery's gate, so the three
# phases it owns — fmt, test, compile — are gate cost and were unattributed:
# every discussion of "the build is slow" reasoned about test.sh alone because
# that was the only part anyone had a name for. This outer profile is coarse —
# three rows — and test.sh prints its own per-suite profile nested inside row 2.
BUILD_SH_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$BUILD_SH_DIR/scripts/lib/gate-profile.sh"
gate_profile_begin "build.sh"
trap 'gate_profile_report' EXIT

# The step labels below are the profile's row names, and fmt.sh/test.sh print
# their own "Step N:" banners — so these are named for what the row IS rather
# than restating a banner the reader is about to see anyway.
echo "Starting build"
gate_step "fmt.sh (go fmt ./...)" ./fmt.sh || exit 1

if [ "$skip_tests" = false ]; then
  gate_step "test.sh (its own per-step profile is nested inside this row)" ./test.sh || exit 1
fi

echo "Step 3: Building binaries into ${build_dir}..."
mkdir -p "$build_dir"
gate_step "go build ./cmd/..." go build -ldflags "$ldflags" -o "${build_dir%/}/" ./cmd/... || exit 1

if [ "$do_install" = true ]; then
  echo "Step 4: Installing binaries into GOBIN..."
  gate_step "go install ./cmd/..." go install -ldflags "$ldflags" ./cmd/... || exit 1
fi
