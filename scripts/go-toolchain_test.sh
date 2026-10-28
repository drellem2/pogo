#!/usr/bin/env bash
# Controls on scripts/lib/go-toolchain.sh (drellem2/pogo#234, mg-bc32b): how
# build.sh, fmt.sh and test.sh find go when it is not on PATH.
#
# Every case runs with PATH stripped to /usr/bin:/bin, which holds no go on
# darwin or on a stock linux, and with POGO_GO_PROBE pointing at fake candidate
# directories, so nothing here depends on where this host installed go. The fake
# go prints its arguments and the PATH it was started with, then exits 0.
#
#   (a) POSITIVE CONTROL. A bare `go` under the stripped PATH fails first, so
#       the later success is the library's doing. Then the real fmt.sh finds
#       the fake go through the probe list, runs it, does not put the probed
#       directory itself on PATH, keeps the caller's EXIT trap, and leaves no
#       private directory behind.
#   (b) Nothing found: exits 127 and names the PATH it saw and every candidate.
#   (c) POGO_GO set to a non-executable file (and to a directory) is refused
#       with exit 2, even though a usable go sits in the probe list. It is not
#       a fall-through.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
LIB="$REPO_ROOT/scripts/lib/go-toolchain.sh"

# The cases below already run under `env -i` with HOME pointed at a fixture,
# but the suite as a whole goes through the packaged isolation like every other
# (internal/testsandbox/adoption_test.go), and its fixtures live in the
# sandbox root, which pogo_sandbox_down removes.
# shellcheck source=/dev/null
source "$HERE/pogo-sandbox"
pogo_sandbox_create go-toolchain
pogo_sandbox_isolate
trap 'pogo_sandbox_down' EXIT INT TERM HUP
T="$POGO_SANDBOX_DIR/t"
mkdir -p "$T"

PASS=0
FAIL=0
pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

STRIPPED_PATH="/usr/bin:/bin"

# A fake go, and a gofmt beside it, in a candidate directory.
mkdir -p "$T/fake" "$T/empty1" "$T/empty2" "$T/work" "$T/tmp"
cat >"$T/fake/go" <<'EOF'
#!/bin/sh
echo "fake-go args: $*"
echo "fake-go PATH: $PATH"
EOF
cp "$T/fake/go" "$T/fake/gofmt"
chmod +x "$T/fake/go" "$T/fake/gofmt"

# run_clean <cmd...>: run with a minimal environment. TMPDIR is a private dir so
# the leftover check below counts only what this run made.
run_clean() {
    (cd "$T/work" && env -i HOME="$T" PATH="$STRIPPED_PATH" TMPDIR="$T/tmp" "$@")
}

leftovers() { find "$T/tmp" -mindepth 1 -maxdepth 1 -name 'pogo-go.*' | wc -l | tr -d ' '; }

# --- (a) positive control ---------------------------------------------------
echo "Test (a): PATH stripped, go found through a fake candidate dir"

if out="$(run_clean /bin/bash -c 'go version' 2>&1)"; then
    fail "control: a bare go ran under PATH=$STRIPPED_PATH, so this test cannot show the library found it: $out"
else
    pass "control: a bare go is not found under PATH=$STRIPPED_PATH"
fi

out="$(run_clean POGO_GO_PROBE="$T/empty1:$T/fake" /bin/bash "$REPO_ROOT/fmt.sh" 2>&1)"
status=$?
if [ "$status" -eq 0 ] && printf '%s\n' "$out" | grep -q '^fake-go args: fmt \./\.\.\.$'; then
    pass "fmt.sh ran the probed go (go fmt ./...)"
else
    fail "fmt.sh did not run the probed go (status $status): $out"
fi
if printf '%s\n' "$out" | grep -q "go-toolchain: using $T/fake/go "; then
    pass "the chosen go is printed"
else
    fail "the chosen go is not printed: $out"
fi
seen_path="$(printf '%s\n' "$out" | sed -n 's/^fake-go PATH: //p')"
case ":$seen_path:" in
    *":$T/fake:"*) fail "the probed directory itself was put on PATH: $seen_path" ;;
    *) pass "the probed directory is not on PATH" ;;
esac
first="${seen_path%%:*}"
case "$first" in
    "$T/tmp/pogo-go."*) pass "PATH starts with the private symlink dir ($first)" ;;
    *) fail "PATH does not start with a private pogo-go.* dir: $seen_path" ;;
esac
if [ "$(leftovers)" = "0" ]; then
    pass "the private dir is removed on exit"
else
    fail "pogo-go.* left behind: $(ls "$T/tmp")"
fi

# The library chains onto the caller's EXIT trap (build.sh and test.sh rely on
# it to keep their profile report) and exposes gofmt as well.
out="$(run_clean POGO_GO_PROBE="$T/fake" LIB="$LIB" /bin/bash -c '
    trap "echo CALLER-TRAP-RAN" EXIT
    . "$LIB"
    go_toolchain_resolve || exit $?
    gofmt -l .
    echo "dir=$GO_TOOLCHAIN_DIR"
' 2>&1)"
status=$?
if [ "$status" -eq 0 ] && printf '%s\n' "$out" | grep -q '^CALLER-TRAP-RAN$'; then
    pass "the caller's EXIT trap still runs"
else
    fail "the caller's EXIT trap was lost (status $status): $out"
fi
if printf '%s\n' "$out" | grep -q '^fake-go args: -l \.$'; then
    pass "gofmt beside the probed go is exposed too"
else
    fail "gofmt was not exposed: $out"
fi
if [ "$(leftovers)" = "0" ]; then
    pass "the private dir is removed when the caller had its own EXIT trap"
else
    fail "pogo-go.* left behind with a caller trap: $(ls "$T/tmp")"
fi

# The caller's EXIT handler must see the script's own status, not the rm's:
# test.sh's gate_exit_report reads $? to say THIS RUN FAILED. Control first:
# with go on PATH (no chaining) the handler sees 1, so the probe arm's reading
# is about the chaining and not about how the handler reads $?.
for arm in path probe; do
    if [ "$arm" = path ]; then
        env_path="$T/fake:$STRIPPED_PATH"; probe="$T/empty1"
    else
        env_path="$STRIPPED_PATH"; probe="$T/fake"
    fi
    out="$(cd "$T/work" && env -i HOME="$T" PATH="$env_path" TMPDIR="$T/tmp" POGO_GO_PROBE="$probe" LIB="$LIB" /bin/bash -c '
        report() { local s=$?; echo "HANDLER-SAW=$s"; }
        trap report EXIT
        . "$LIB"
        go_toolchain_resolve >/dev/null || exit $?
        false
        exit 1
    ' 2>&1)"
    status=$?
    if [ "$status" -eq 1 ] && printf '%s\n' "$out" | grep -q '^HANDLER-SAW=1$'; then
        pass "$arm: the caller's EXIT handler sees the failing status (1), and the script exits 1"
    else
        fail "$arm: the caller's EXIT handler or exit status lost the failure (status $status): $out"
    fi
done

# A TERM while the caller waits still removes the dir.
run_clean POGO_GO_PROBE="$T/fake" LIB="$LIB" /bin/bash -c '
    . "$LIB"
    go_toolchain_resolve >/dev/null || exit $?
    echo "$GO_TOOLCHAIN_DIR" > "$HOME/dir"
    kill -TERM $$
    sleep 5
' >/dev/null 2>&1
status=$?
if [ "$status" -eq 143 ] && [ "$(leftovers)" = "0" ]; then
    pass "SIGTERM exits 143 and removes the private dir"
else
    fail "after SIGTERM: status $status, leftovers $(leftovers)"
fi

# --- (b) nothing found ------------------------------------------------------
echo ""
echo "Test (b): nothing found fails loud and names the candidates"
out="$(run_clean POGO_GO_PROBE="$T/empty1:$T/empty2" /bin/bash "$REPO_ROOT/fmt.sh" 2>&1)"
status=$?
if [ "$status" -eq 127 ]; then
    pass "exits 127"
else
    fail "exit status $status, want 127: $out"
fi
if printf '%s\n' "$out" | grep -q "$T/empty1/go" && printf '%s\n' "$out" | grep -q "$T/empty2/go"; then
    pass "names every candidate it tried"
else
    fail "does not name the candidates: $out"
fi
if printf '%s\n' "$out" | grep -q "PATH=$STRIPPED_PATH"; then
    pass "prints the PATH it saw"
else
    fail "does not print the PATH it saw: $out"
fi
if printf '%s\n' "$out" | grep -q '^Step 1: Formatting'; then
    fail "fmt.sh went on to format after the lookup failed: $out"
else
    pass "fmt.sh stops before running go"
fi

# --- (c) POGO_GO not executable ---------------------------------------------
echo ""
echo "Test (c): a non-executable POGO_GO is refused, not fallen through"
printf '#!/bin/sh\necho should-not-run\n' >"$T/notexec"
chmod -x "$T/notexec"
for bad in "$T/notexec" "$T/fake"; do
    out="$(run_clean POGO_GO="$bad" POGO_GO_PROBE="$T/fake" /bin/bash "$REPO_ROOT/fmt.sh" 2>&1)"
    status=$?
    if [ "$status" -eq 2 ] && printf '%s\n' "$out" | grep -q "POGO_GO=$bad is not an executable file"; then
        pass "POGO_GO=$bad is refused with exit 2"
    else
        fail "POGO_GO=$bad: status $status, want 2 and a refusal: $out"
    fi
    if printf '%s\n' "$out" | grep -q '^fake-go'; then
        fail "POGO_GO=$bad fell through to the probe list: $out"
    else
        pass "POGO_GO=$bad does not fall through to the probe list"
    fi
done
# Control for (c): an executable POGO_GO is used.
out="$(run_clean POGO_GO="$T/fake/go" POGO_GO_PROBE="$T/empty1" /bin/bash "$REPO_ROOT/fmt.sh" 2>&1)"
status=$?
if [ "$status" -eq 0 ] && printf '%s\n' "$out" | grep -q "using $T/fake/go (from POGO_GO)"; then
    pass "control: an executable POGO_GO is used"
else
    fail "control: an executable POGO_GO was not used (status $status): $out"
fi
if [ "$(leftovers)" = "0" ]; then
    pass "no pogo-go.* left behind by any case"
else
    fail "pogo-go.* left behind: $(ls "$T/tmp")"
fi

echo ""
echo "=== $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
