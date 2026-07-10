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

echo "Starting build"
./fmt.sh || exit 1

if [ "$skip_tests" = false ]; then
  ./test.sh || exit 1
fi

echo "Step 3: Building binaries into ${build_dir}..."
mkdir -p "$build_dir"
go build -o "${build_dir%/}/" ./cmd/... || exit 1

if [ "$do_install" = true ]; then
  echo "Step 4: Installing binaries into GOBIN..."
  go install ./cmd/... || exit 1
fi
