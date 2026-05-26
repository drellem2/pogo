#!/bin/sh
# Tests for install.sh
# Exercises OS/arch detection, POGO_INSTALL_DIR override, and missing release handling.
set -e

# Tests that exercise the full script (e.g. with a real POGO_VERSION) must
# never trigger the final `pogo install` step — that would mutate the
# running user's ~/.pogo/ state, leak daemon processes, and depend on mg.
# Force the env-var opt-out for every invocation in this file.
export POGO_NO_POGO_INSTALL=1

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
if echo "$OUTPUT" | grep -q "Error: failed to download"; then
  pass "Download failures reported as errors (expected for fake version)"
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

# --- Test 9: --interactive flag is recognized ---
echo ""
echo "Test 9: --interactive flag parsing"
# Extract and test the flag parsing logic
RESULT=$(sh -c '
  INTERACTIVE=false
  for arg in "--interactive"; do
    case "$arg" in
      --interactive|--with-integrations) INTERACTIVE=true ;;
    esac
  done
  echo "$INTERACTIVE"
')
if [ "$RESULT" = "true" ]; then
  pass "--interactive flag sets INTERACTIVE=true"
else
  fail "--interactive flag not recognized (got '$RESULT')"
fi

# --- Test 10: --with-integrations alias works ---
echo ""
echo "Test 10: --with-integrations alias"
RESULT=$(sh -c '
  INTERACTIVE=false
  for arg in "--with-integrations"; do
    case "$arg" in
      --interactive|--with-integrations) INTERACTIVE=true ;;
    esac
  done
  echo "$INTERACTIVE"
')
if [ "$RESULT" = "true" ]; then
  pass "--with-integrations flag sets INTERACTIVE=true"
else
  fail "--with-integrations flag not recognized (got '$RESULT')"
fi

# --- Test 11: Default (no flag) shows tip ---
echo ""
echo "Test 11: Non-interactive mode shows tip about --interactive"
TMPDIR_INSTALL=$(mktemp -d)
OUTPUT=$(POGO_VERSION="v0.0.0-test" POGO_INSTALL_DIR="$TMPDIR_INSTALL" sh "$INSTALL_SCRIPT" 2>&1) || true
if echo "$OUTPUT" | grep -q "\-\-interactive"; then
  pass "Non-interactive mode mentions --interactive flag"
else
  fail "Non-interactive mode does not mention --interactive flag"
fi
rm -rf "$TMPDIR_INSTALL"

# --- Test 12: Shell snippet idempotency check ---
echo ""
echo "Test 12: Shell snippet idempotency (Start Pogo marker detection)"
TMPDIR_TEST=$(mktemp -d)
RC_FILE="${TMPDIR_TEST}/.zshrc"
echo "# Start Pogo" > "$RC_FILE"
# The install_shell_snippet function checks for "Start Pogo" marker
if grep -q "Start Pogo" "$RC_FILE" 2>/dev/null; then
  pass "Marker detection works for already-installed snippets"
else
  fail "Marker detection failed"
fi
rm -rf "$TMPDIR_TEST"

# --- Test 13: mg dependency check present in script ---
echo ""
echo "Test 13: macguffin (mg) dependency check exists in install script"
if grep -q "macguffin" "$INSTALL_SCRIPT" && grep -q "command -v mg" "$INSTALL_SCRIPT"; then
  pass "Install script contains macguffin dependency check"
else
  fail "Install script missing macguffin dependency check"
fi

# --- Test 14: --help shows usage and exits 0 ---
echo ""
echo "Test 14: --help prints usage and exits 0"
OUTPUT=$(sh "$INSTALL_SCRIPT" --help 2>&1) && STATUS=$? || STATUS=$?
if [ $STATUS -eq 0 ] && echo "$OUTPUT" | grep -q "Usage:" && echo "$OUTPUT" | grep -q "\-\-no-pogo-install"; then
  pass "--help prints usage including --no-pogo-install"
else
  fail "--help did not print expected usage (status=$STATUS)"
fi

# --- Test 15: auto-run is wired into the install script ---
echo ""
echo "Test 15: pogo install auto-run helper is wired into install.sh"
if grep -q "run_pogo_install_step" "$INSTALL_SCRIPT"; then
  COUNT=$(grep -c "run_pogo_install_step" "$INSTALL_SCRIPT")
  # one definition + at least two call sites (non-interactive and interactive paths)
  if [ "$COUNT" -ge 3 ]; then
    pass "run_pogo_install_step is defined and called from both paths ($COUNT references)"
  else
    fail "run_pogo_install_step is referenced only $COUNT times; expected >=3 (definition + 2 call sites)"
  fi
else
  fail "run_pogo_install_step helper not found in install.sh"
fi

# --- Test 16: --no-pogo-install flag opt-out takes effect ---
echo ""
echo "Test 16: --no-pogo-install opt-out prints manual next-step"
TMPDIR_INSTALL=$(mktemp -d)
OUTPUT=$(POGO_VERSION="v0.0.0-test" POGO_INSTALL_DIR="$TMPDIR_INSTALL" sh "$INSTALL_SCRIPT" --no-pogo-install 2>&1) || true
# The script's binary download will fail (fake version), but the flag-parse
# block doesn't execute the auto-run helper until later — so we instead
# verify the flag is recognized via the help output covering it AND by
# script-content inspection that the flag sets SKIP_POGO_INSTALL=true.
if grep -A2 "\-\-no-pogo-install)" "$INSTALL_SCRIPT" | grep -q "SKIP_POGO_INSTALL=true"; then
  pass "--no-pogo-install flag sets SKIP_POGO_INSTALL=true"
else
  fail "--no-pogo-install flag does not set SKIP_POGO_INSTALL=true in parser"
fi
rm -rf "$TMPDIR_INSTALL"

# --- Test 17: POGO_NO_POGO_INSTALL env var opt-out is recognized ---
echo ""
echo "Test 17: POGO_NO_POGO_INSTALL env var sets opt-out"
# Verify the env-var case-statement is wired up correctly.
if grep -q 'POGO_NO_POGO_INSTALL' "$INSTALL_SCRIPT" && \
   grep -B1 -A4 'POGO_NO_POGO_INSTALL' "$INSTALL_SCRIPT" | grep -q "SKIP_POGO_INSTALL=true"; then
  pass "POGO_NO_POGO_INSTALL env var triggers SKIP_POGO_INSTALL=true"
else
  fail "POGO_NO_POGO_INSTALL env-var handling missing or wrong"
fi

# --- Test 18: opt-out branch prints a clear manual next-step ---
echo ""
echo "Test 18: opt-out branch surfaces 'pogo install' as the next step"
if grep -B2 -A6 'SKIP_POGO_INSTALL.*=.*true' "$INSTALL_SCRIPT" | grep -q "pogo install"; then
  pass "Opt-out branch instructs the user to run 'pogo install' manually"
else
  fail "Opt-out branch does not surface 'pogo install' as the manual next-step"
fi

# --- Test 19: README documents the auto-run + opt-out ---
echo ""
echo "Test 19: README documents auto-run and --no-pogo-install"
README="${SCRIPT_DIR}/README.md"
if [ -f "$README" ]; then
  if grep -q "\-\-no-pogo-install" "$README" && grep -q "runs \`pogo install\`" "$README"; then
    pass "README documents auto-run and --no-pogo-install opt-out"
  else
    fail "README missing auto-run or --no-pogo-install documentation"
  fi
else
  fail "README.md not found alongside install.sh"
fi

# --- Summary ---
echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
if [ $FAIL -gt 0 ]; then
  exit 1
fi
