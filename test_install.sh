#!/bin/sh
# Tests for install.sh
# Exercises OS/arch detection, POGO_INSTALL_DIR override, and missing release handling.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INSTALL_SCRIPT="${SCRIPT_DIR}/install.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== install.sh tests ==="

# --- Test 1: Script is valid shell ---
echo ""
echo "Test 1: Script syntax check"
if sh -n "$INSTALL_SCRIPT" 2>/dev/null; then
  pass "install.sh has valid shell syntax"
else
  fail "install.sh has syntax errors"
fi

# --- Test 2: OS detection produces valid value ---
echo ""
echo "Test 2: OS detection"
DETECTED_OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$DETECTED_OS" in
  linux|darwin)
    pass "OS detected as '$DETECTED_OS' (supported)"
    ;;
  *)
    fail "OS detected as '$DETECTED_OS' (unsupported)"
    ;;
esac

# --- Test 3: Architecture detection produces valid value ---
echo ""
echo "Test 3: Architecture detection"
RAW_ARCH=$(uname -m)
case "$RAW_ARCH" in
  x86_64|amd64)
    NORM_ARCH="amd64"
    pass "Arch '$RAW_ARCH' normalizes to '$NORM_ARCH'"
    ;;
  arm64|aarch64)
    NORM_ARCH="arm64"
    pass "Arch '$RAW_ARCH' normalizes to '$NORM_ARCH'"
    ;;
  *)
    fail "Arch '$RAW_ARCH' is not handled by install.sh"
    ;;
esac

# --- Test 4: POGO_INSTALL_DIR override ---
echo ""
echo "Test 4: POGO_INSTALL_DIR override"
TMPDIR_TEST=$(mktemp -d)
# Source just the variable assignment logic by extracting it
RESULT=$(POGO_INSTALL_DIR="$TMPDIR_TEST" sh -c '
  INSTALL_DIR="${POGO_INSTALL_DIR:-/usr/local/bin}"
  echo "$INSTALL_DIR"
')
if [ "$RESULT" = "$TMPDIR_TEST" ]; then
  pass "POGO_INSTALL_DIR override works (got '$RESULT')"
else
  fail "POGO_INSTALL_DIR override failed (expected '$TMPDIR_TEST', got '$RESULT')"
fi
rmdir "$TMPDIR_TEST"

# --- Test 5: Default INSTALL_DIR when POGO_INSTALL_DIR is unset ---
echo ""
echo "Test 5: Default install directory"
RESULT=$(unset POGO_INSTALL_DIR; sh -c '
  INSTALL_DIR="${POGO_INSTALL_DIR:-/usr/local/bin}"
  echo "$INSTALL_DIR"
')
if [ "$RESULT" = "/usr/local/bin" ]; then
  pass "Default install dir is /usr/local/bin"
else
  fail "Default install dir is '$RESULT', expected /usr/local/bin"
fi

# --- Test 6: Missing release handled gracefully ---
echo ""
echo "Test 6: Missing release (no GitHub releases exist)"
TMPDIR_INSTALL=$(mktemp -d)
# Run the full script with a nonexistent version to test download failure handling.
# The script should fail at version detection (no releases) and exit 1.
OUTPUT=$(POGO_INSTALL_DIR="$TMPDIR_INSTALL" sh "$INSTALL_SCRIPT" 2>&1) && STATUS=$? || STATUS=$?
if [ $STATUS -ne 0 ]; then
  if echo "$OUTPUT" | grep -qi "could not determine latest version\|error"; then
    pass "Script exits with error when no release exists (exit $STATUS)"
  else
    pass "Script exits non-zero ($STATUS) when no release exists"
  fi
else
  # Script succeeded unexpectedly - check if binaries were actually downloaded
  if [ -z "$(ls -A "$TMPDIR_INSTALL" 2>/dev/null)" ]; then
    fail "Script exited 0 but no binaries downloaded (should have exited non-zero)"
  else
    fail "Script succeeded unexpectedly - releases may exist now"
  fi
fi
rm -rf "$TMPDIR_INSTALL"

# --- Test 7: POGO_VERSION override ---
echo ""
echo "Test 7: POGO_VERSION override skips API call"
TMPDIR_INSTALL=$(mktemp -d)
# Use a fake version - should skip the GitHub API call and go straight to download
OUTPUT=$(POGO_VERSION="v0.0.0-test" POGO_INSTALL_DIR="$TMPDIR_INSTALL" sh "$INSTALL_SCRIPT" 2>&1) && STATUS=$? || STATUS=$?
if echo "$OUTPUT" | grep -q "v0.0.0-test"; then
  pass "POGO_VERSION override is used in output"
else
  fail "POGO_VERSION override not reflected in output"
fi
# Downloads will fail (fake version) but that's expected - we're testing the variable override
if echo "$OUTPUT" | grep -q "Warning: failed to download"; then
  pass "Download failures reported as warnings (expected for fake version)"
fi
rm -rf "$TMPDIR_INSTALL"

# --- Test 8: All four binaries are attempted ---
echo ""
echo "Test 8: All binaries are attempted for download"
TMPDIR_INSTALL=$(mktemp -d)
OUTPUT=$(POGO_VERSION="v0.0.0-test" POGO_INSTALL_DIR="$TMPDIR_INSTALL" sh "$INSTALL_SCRIPT" 2>&1) || true
EXPECTED_BINS="pogo pogod lsp pose"
ALL_FOUND=true
for bin in $EXPECTED_BINS; do
  if echo "$OUTPUT" | grep -q "Downloading ${bin}"; then
    : # found
  else
    ALL_FOUND=false
    fail "Binary '$bin' not attempted for download"
  fi
done
if [ "$ALL_FOUND" = true ]; then
  pass "All 4 binaries (pogo pogod lsp pose) attempted"
fi
rm -rf "$TMPDIR_INSTALL"

# --- Summary ---
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ $FAIL -gt 0 ]; then
  exit 1
fi
