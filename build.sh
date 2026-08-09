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
gate_step "go build ./cmd/..." go build -o "${build_dir%/}/" ./cmd/... || exit 1

if [ "$do_install" = true ]; then
  echo "Step 4: Installing binaries into GOBIN..."
  gate_step "go install ./cmd/..." go install ./cmd/... || exit 1
fi
