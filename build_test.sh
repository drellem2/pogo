#!/bin/bash
# Tests for build.sh
#
# The contract under test (mg-b630): build.sh compiles into a scratch build dir
# and never writes GOBIN unless --install is passed, while still failing the
# build on a compile error. Every case runs the real build.sh against a
# synthetic single-binary Go module in a temp dir, with GOBIN redirected to a
# temp dir, so the host's ~/go/bin is never touched by this test either.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BUILD_SCRIPT="${SCRIPT_DIR}/build.sh"
PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1" >&2; }

echo "=== build.sh tests ==="

# --- Test 1: Script is valid shell ---
echo ""
echo "Test 1: Script syntax check"
if bash -n "$BUILD_SCRIPT" 2>/dev/null; then
  pass "build.sh has valid bash syntax"
else
  fail "build.sh has syntax errors"
fi

# --- Fixture: a throwaway Go module that mimics the repo's shape ---
# fmt.sh / test.sh are stubbed so the tests exercise build.sh alone and stay
# fast; each stub drops a marker file so we can assert whether it ran.
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fixture="${tmpdir}/module"
gobin="${tmpdir}/gobin"
mkdir -p "${fixture}/cmd/hello" "$gobin"

cp "$BUILD_SCRIPT" "${fixture}/build.sh"
printf '#!/bin/bash\ntouch ran-fmt\n' > "${fixture}/fmt.sh"
printf '#!/bin/bash\ntouch ran-tests\n' > "${fixture}/test.sh"
chmod +x "${fixture}/build.sh" "${fixture}/fmt.sh" "${fixture}/test.sh"

printf 'module example.com/hello\n\ngo 1.25.0\n' > "${fixture}/go.mod"
good_main='package main

func main() { println("hello") }
'
bad_main='package main

func main() { this is not go }
'
printf '%s' "$good_main" > "${fixture}/cmd/hello/main.go"

# GOBIN governs where `go install` writes. Point it at a temp dir we can assert
# on, and export it for every build.sh invocation below.
export GOBIN="$gobin"

gobin_empty() {
  [ -z "$(ls -A "$gobin")" ]
}

# --- Test 2: default build compiles into ./bin, not GOBIN ---
echo ""
echo "Test 2: Default build writes ./bin and leaves GOBIN untouched"
if (cd "$fixture" && ./build.sh >/dev/null 2>&1); then
  if [ -x "${fixture}/bin/hello" ]; then
    pass "build.sh compiled cmd/hello into ./bin"
  else
    fail "build.sh did not produce ./bin/hello"
  fi
  if gobin_empty; then
    pass "build.sh left GOBIN empty (no go install)"
  else
    fail "build.sh wrote into GOBIN: $(ls -A "$gobin")"
  fi
  if [ -f "${fixture}/ran-tests" ]; then
    pass "build.sh ran the test step"
  else
    fail "build.sh skipped the test step"
  fi
else
  fail "build.sh failed on a module that compiles cleanly"
fi

# --- Test 3: --skip-tests skips test.sh but still builds ---
echo ""
echo "Test 3: --skip-tests skips the test step"
rm -rf "${fixture}/bin" "${fixture}/ran-tests"
if (cd "$fixture" && ./build.sh --skip-tests >/dev/null 2>&1); then
  if [ ! -f "${fixture}/ran-tests" ] && [ -x "${fixture}/bin/hello" ]; then
    pass "--skip-tests skipped test.sh and still built the binary"
  else
    fail "--skip-tests did not behave as expected"
  fi
else
  fail "build.sh --skip-tests failed on a clean module"
fi

# --- Test 4: POGO_BUILD_DIR redirects the output dir ---
echo ""
echo "Test 4: POGO_BUILD_DIR overrides the build directory"
rm -rf "${fixture}/bin"
if (cd "$fixture" && POGO_BUILD_DIR="${tmpdir}/out" ./build.sh --skip-tests >/dev/null 2>&1); then
  if [ -x "${tmpdir}/out/hello" ] && [ ! -d "${fixture}/bin" ]; then
    pass "POGO_BUILD_DIR redirected the binaries"
  else
    fail "POGO_BUILD_DIR was not honored"
  fi
else
  fail "build.sh failed with POGO_BUILD_DIR set"
fi

# --- Test 5: a compile error still fails the build ---
# This is the regression that matters: dropping `go install` must not weaken
# build.sh as a quality gate.
echo ""
echo "Test 5: A compile error fails the build"
rm -rf "${fixture}/bin"
printf '%s' "$bad_main" > "${fixture}/cmd/hello/main.go"
if (cd "$fixture" && ./build.sh --skip-tests >/dev/null 2>&1); then
  fail "build.sh exited 0 despite a compile error"
else
  pass "build.sh exited non-zero on a compile error"
fi
if gobin_empty; then
  pass "A failed build left GOBIN empty"
else
  fail "A failed build wrote into GOBIN: $(ls -A "$gobin")"
fi
printf '%s' "$good_main" > "${fixture}/cmd/hello/main.go"

# --- Test 6: --install is the opt-in that populates GOBIN ---
echo ""
echo "Test 6: --install populates GOBIN"
if (cd "$fixture" && ./build.sh --skip-tests --install >/dev/null 2>&1); then
  if [ -x "${gobin}/hello" ]; then
    pass "--install placed the binary in GOBIN"
  else
    fail "--install did not populate GOBIN"
  fi
else
  fail "build.sh --skip-tests --install failed on a clean module"
fi
rm -f "${gobin}/hello"

# --- Test 7: unknown flags are rejected rather than silently ignored ---
echo ""
echo "Test 7: Unknown flags are rejected"
if (cd "$fixture" && ./build.sh --no-such-flag >/dev/null 2>&1); then
  fail "build.sh accepted an unknown flag"
else
  pass "build.sh rejected an unknown flag"
fi

echo ""
echo "=== Results: ${PASS} passed, ${FAIL} failed ==="
[ "$FAIL" -eq 0 ]
