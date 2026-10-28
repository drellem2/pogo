#!/bin/bash
# Find go even when it is not on PATH, e.g. under the refinery gate (gh#234).
FMT_SH_DIR="$(cd "$(dirname "$0")" && pwd)"
# Checked before sourcing: bash 3.2 under set -e and an EXIT trap exits 0 when
# a sourced file is missing, so a lost library would read as a green run.
[ -r "$FMT_SH_DIR/scripts/lib/go-toolchain.sh" ] || { echo "fmt.sh: missing scripts/lib/go-toolchain.sh" >&2; exit 1; }
. "$FMT_SH_DIR/scripts/lib/go-toolchain.sh"
go_toolchain_resolve || exit $?

echo "Step 1: Formatting..."
go fmt ./...
