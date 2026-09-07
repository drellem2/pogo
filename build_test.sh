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

# build.sh sources scripts/lib/gate-profile.sh for its per-step profile
# (mg-eed9) and does NOT degrade to a no-op if the file is missing — an
# unprofiled gate that says nothing about being unprofiled is the state this
# ticket exists to leave. So the fixture carries the real library rather than a
# stub: the alternative is testing build.sh against a profiler that isn't the
# one it ships with.
mkdir -p "${fixture}/scripts/lib"
cp "${SCRIPT_DIR}/scripts/lib/gate-profile.sh" "${fixture}/scripts/lib/gate-profile.sh"

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

# --- Test 8: the gate emits a per-step profile, including when it FAILS ---
# The failing half is the load-bearing one (mg-eed9). A profile that only
# prints on success is absent from every run anyone would want it for: a gate
# that failed at step 12 of 19 is precisely where "which step was the time in"
# gets asked, and `set -e` means that run never reaches the bottom of the
# script. The report is armed on EXIT for that reason, and this asserts it.
echo ""
echo "Test 8: build.sh emits a per-step profile on success and on failure"
rm -rf "${fixture}/bin"
PROFILE_OK="$(cd "$fixture" && ./build.sh --skip-tests 2>&1)"
if printf '%s\n' "$PROFILE_OK" | grep -q 'GATE STEP PROFILE'; then
  pass "a successful build printed the profile table"
else
  fail "a successful build printed no profile table"
fi
if printf '%s\n' "$PROFILE_OK" | grep -q 'go build \./cmd/\.\.\.'; then
  pass "the profile names the compile step"
else
  fail "the profile does not name the compile step"
fi

rm -rf "${fixture}/bin"
printf '%s' "$bad_main" > "${fixture}/cmd/hello/main.go"
PROFILE_FAIL="$(cd "$fixture" && ./build.sh --skip-tests 2>&1)" && FAILED_RC=0 || FAILED_RC=$?
printf '%s' "$good_main" > "${fixture}/cmd/hello/main.go"
if [ "$FAILED_RC" -ne 0 ]; then
  pass "the failing build still exited non-zero with profiling armed"
else
  fail "profiling swallowed the compile failure (exit 0)"
fi
if printf '%s\n' "$PROFILE_FAIL" | grep -q 'GATE STEP PROFILE'; then
  pass "the FAILING build printed the profile table"
else
  fail "the failing build printed no profile table — the run that needs it most"
fi
if printf '%s\n' "$PROFILE_FAIL" | grep -q '\[FAILED\]'; then
  pass "the failing step is marked [FAILED] in the table"
else
  fail "the failing step is not marked in the table"
fi

# --- Test 9: POGO_GATE_PROFILE=0 disables the table without disabling the gate
echo ""
echo "Test 9: POGO_GATE_PROFILE=0 suppresses the table and still builds"
rm -rf "${fixture}/bin"
PROFILE_OFF="$(cd "$fixture" && POGO_GATE_PROFILE=0 ./build.sh --skip-tests 2>&1)"
if printf '%s\n' "$PROFILE_OFF" | grep -q 'GATE STEP PROFILE'; then
  fail "POGO_GATE_PROFILE=0 still printed the table"
else
  pass "POGO_GATE_PROFILE=0 suppressed the table"
fi
if [ -x "${fixture}/bin/hello" ]; then
  pass "POGO_GATE_PROFILE=0 still ran the steps and built the binary"
else
  fail "POGO_GATE_PROFILE=0 stopped the build from running"
fi

# --- Test 10: build.sh PRESERVES a step's exit status, and does not flatten it
#
# The defect (mg-b1df): every step read `|| exit 1`, so build.sh reported 1 for
# every failure it ever saw. One of the statuses that flattened carries the only
# machine-readable evidence a step was KILLED rather than red — a POSIX shell
# reports a child that died of signal N as 128+N, so a SIGTERM'd `go test`
# reaches build.sh as 143.
#
# Measured, on mr-dafhg22tjv1hjkm2144g / polecat-t2127 / 2026-09-07T20:06Z: bash
# printed `Terminated: 15`, scripts/tmpdir-leak-guard.sh captured 143 and said
# so in its own report, test.sh's `set -e` propagated 143, and build.sh turned
# it into 1. The refinery was handed `exit status 1`, which is what a failing
# test suite looks like, and recorded the merge DEFECT — "a fix is warranted" —
# against a run that was never allowed to finish asserting anything. No
# classifier downstream could have done better; the evidence was already gone.
#
# This test pins the STATUS, and nothing about how the refinery reads it. The
# refinery still classifies a gate that exits 143 as a DEFECT, deliberately —
# mg-0502 ruled that the exit number cannot distinguish a relayed kill from a
# chosen status, and pm-pogo upheld that against this ticket. build.sh
# reporting an accurate status is correct regardless of what any consumer
# concludes from it, which is why this contract is testable on its own.
#
# Test 10b is the load-bearing half. 143 alone passes against a build.sh that
# hardcodes 143, and 1 alone passes against the DEFECT this replaces, so
# neither number tests the contract on its own: what is under test is that the
# status is CARRIED, whatever it is.
echo ""
echo "Test 10: build.sh preserves each step's exit status"
rm -rf "${fixture}/bin"
printf '%s' "$good_main" > "${fixture}/cmd/hello/main.go"

# 10a: the relayed-kill status the incident produced.
printf '#!/bin/bash\nexit 143\n' > "${fixture}/test.sh"
chmod +x "${fixture}/test.sh"
(cd "$fixture" && ./build.sh >/dev/null 2>&1) && RELAY_RC=0 || RELAY_RC=$?
if [ "$RELAY_RC" -eq 143 ]; then
  pass "a step that exits 143 (128+15, SIGTERM relay) reaches the caller as 143"
else
  fail "build.sh reported ${RELAY_RC} for a step that exited 143 — the kill evidence is destroyed here, and the refinery cannot classify what it is not told"
fi

# 10b: an unrelated status, so 10a cannot pass against a hardcoded 143.
printf '#!/bin/bash\nexit 7\n' > "${fixture}/test.sh"
chmod +x "${fixture}/test.sh"
(cd "$fixture" && ./build.sh >/dev/null 2>&1) && OTHER_RC=0 || OTHER_RC=$?
if [ "$OTHER_RC" -eq 7 ]; then
  pass "a step that exits 7 reaches the caller as 7 — the status is carried, not special-cased"
else
  fail "build.sh reported ${OTHER_RC} for a step that exited 7"
fi

# 10c: a REAL kill, not a script that types the number. This is the only case
# that exercises the shell's own relay convention rather than our belief about
# it, and it is the shape the incident actually had: the step's own child dies
# of the signal and the step relays it under `set -e`.
printf '#!/bin/bash\nset -e\nbash -c '"'"'kill -TERM $$'"'"'\necho REACHED-END\n' > "${fixture}/test.sh"
chmod +x "${fixture}/test.sh"
(cd "$fixture" && ./build.sh >/dev/null 2>&1) && KILL_RC=0 || KILL_RC=$?
if [ "$KILL_RC" -eq 143 ]; then
  pass "a step whose child is REALLY SIGTERMed reaches the caller as 143"
else
  fail "a real SIGTERM relay reached the caller as ${KILL_RC}, not 143"
fi

# 10d: the positive control for 10a-c. A passing step must still be 0 — a
# build.sh that returned the wrong status for everything would satisfy all
# three cases above only if they were the only cases.
printf '#!/bin/bash\ntouch ran-tests\n' > "${fixture}/test.sh"
chmod +x "${fixture}/test.sh"
rm -rf "${fixture}/bin"
if (cd "$fixture" && ./build.sh >/dev/null 2>&1); then
  pass "control: a build whose steps all pass still exits 0"
else
  fail "control: a passing build no longer exits 0"
fi

echo ""
echo "=== Results: ${PASS} passed, ${FAIL} failed ==="
[ "$FAIL" -eq 0 ]
