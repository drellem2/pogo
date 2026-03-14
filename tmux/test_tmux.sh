#!/usr/bin/env bash
# test_tmux.sh — tests for pogo tmux integration scripts
#
# Tests:
#   1. pogo-status.sh status bar segment rendering (all status branches)
#   2. pogo.tmux keybinding and hook registration (via tmux mock)
#   3. pogo daemon health check behavior in status script
#
# Usage: ./test_tmux.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MOCK_DIR=""
PASS=0
FAIL=0
ERRORS=""

setup() {
    MOCK_DIR="$(mktemp -d)"
    # Create mock bin directory and add to PATH
    mkdir -p "$MOCK_DIR/bin"
    export PATH="$MOCK_DIR/bin:$PATH"
}

teardown() {
    [ -n "$MOCK_DIR" ] && rm -rf "$MOCK_DIR"
}

fail() {
    FAIL=$((FAIL + 1))
    ERRORS="${ERRORS}\n  FAIL: $1"
    echo "  FAIL: $1"
}

pass() {
    PASS=$((PASS + 1))
    echo "  PASS: $1"
}

assert_eq() {
    local expected="$1" actual="$2" msg="$3"
    if [ "$expected" = "$actual" ]; then
        pass "$msg"
    else
        fail "$msg (expected '$expected', got '$actual')"
    fi
}

assert_contains() {
    local haystack="$1" needle="$2" msg="$3"
    if echo "$haystack" | grep -qF -- "$needle"; then
        pass "$msg"
    else
        fail "$msg (expected '$haystack' to contain '$needle')"
    fi
}

assert_empty() {
    local actual="$1" msg="$2"
    if [ -z "$actual" ]; then
        pass "$msg"
    else
        fail "$msg (expected empty, got '$actual')"
    fi
}

# Create a mock script that outputs a canned response
create_mock() {
    local name="$1" body="$2"
    cat > "$MOCK_DIR/bin/$name" <<SCRIPT
#!/usr/bin/env bash
$body
SCRIPT
    chmod +x "$MOCK_DIR/bin/$name"
}

# ============================================================
# Test Suite 1: pogo-status.sh — status bar segment rendering
# ============================================================

test_status_no_args() {
    echo "Test: status script exits silently with no arguments"
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" 2>/dev/null)" || true
    assert_empty "$out" "no output when no pane_path"
}

test_status_empty_path() {
    echo "Test: status script exits silently with empty path"
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "" 2>/dev/null)" || true
    assert_empty "$out" "no output when empty pane_path"
}

test_status_visit_fails() {
    echo "Test: status script exits silently when pogo visit fails"
    create_mock "pogo" 'exit 1'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/some/path" 2>/dev/null)" || true
    assert_empty "$out" "no output when pogo visit fails"
}

test_status_visit_returns_empty() {
    echo "Test: status script exits silently when pogo visit returns empty"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo ""
    exit 0
fi
exit 1
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/some/path" 2>/dev/null)" || true
    assert_empty "$out" "no output when pogo visit returns empty"
}

test_status_ready() {
    echo "Test: status shows checkmark for ready projects"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    echo "ready        /home/user/my-project (42 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project ✓" "$out" "ready status shows checkmark"
}

test_status_indexing() {
    echo "Test: status shows spinner for indexing projects"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    echo "indexing     /home/user/my-project (42 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project ⟳" "$out" "indexing status shows spinner"
}

test_status_stale() {
    echo "Test: status shows bang for stale projects"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    echo "stale        /home/user/my-project (42 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project !" "$out" "stale status shows bang"
}

test_status_unindexed() {
    echo "Test: status shows question mark for unindexed projects"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    echo "unindexed    /home/user/my-project (0 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project ?" "$out" "unindexed status shows question mark"
}

test_status_unknown() {
    echo "Test: status shows bare name for unknown status"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    echo "somethingelse /home/user/my-project (42 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project" "$out" "unknown status shows bare name"
}

test_status_no_status_output() {
    echo "Test: status shows bare name when pogo status has no matching line"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    echo "ready        /home/user/other-project (10 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project" "$out" "bare name when project not in status output"
}

test_status_pogo_status_fails() {
    echo "Test: status shows bare name when pogo status command fails"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    exit 1
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project/src")"
    assert_eq "my-project" "$out" "bare name when pogo status fails (daemon down)"
}

# ============================================================
# Test Suite 2: pogo.tmux — keybinding and hook registration
# ============================================================

test_tmux_registers_hooks_and_keys() {
    echo "Test: pogo.tmux registers pane-focus-in hook and keybindings"
    local log="$MOCK_DIR/tmux_calls.log"
    create_mock "tmux" '
echo "$@" >> '"$log"'
'
    bash "$SCRIPT_DIR/pogo.tmux"

    # Verify pane-focus-in hook was set
    local hook_call
    hook_call="$(grep "set-hook" "$log" 2>/dev/null || true)"
    assert_contains "$hook_call" "pane-focus-in" "registers pane-focus-in hook"
    assert_contains "$hook_call" "pogo visit" "hook calls pogo visit"

    # Verify prefix+P keybinding (project switcher)
    local bind_p
    bind_p="$(grep "bind-key P" "$log" 2>/dev/null || true)"
    assert_contains "$bind_p" "display-popup" "prefix+P uses display-popup"
    assert_contains "$bind_p" "lsp" "prefix+P invokes lsp"
    assert_contains "$bind_p" "fzf" "prefix+P pipes to fzf"

    # Verify prefix+S keybinding (search)
    local bind_s
    bind_s="$(grep "bind-key S" "$log" 2>/dev/null || true)"
    assert_contains "$bind_s" "display-popup" "prefix+S uses display-popup"
    assert_contains "$bind_s" "pose" "prefix+S invokes pose"
}

test_tmux_popup_dimensions() {
    echo "Test: pogo.tmux popup dimensions are correct"
    local log="$MOCK_DIR/tmux_calls.log"
    create_mock "tmux" '
echo "$@" >> '"$log"'
'
    bash "$SCRIPT_DIR/pogo.tmux"

    local bind_p bind_s
    bind_p="$(grep "bind-key P" "$log" 2>/dev/null || true)"
    bind_s="$(grep "bind-key S" "$log" 2>/dev/null || true)"

    # Project switcher: 80% x 60%
    assert_contains "$bind_p" "80%" "project switcher width is 80%"
    assert_contains "$bind_p" "60%" "project switcher height is 60%"

    # Search: 90% x 80%
    assert_contains "$bind_s" "90%" "search popup width is 90%"
    assert_contains "$bind_s" "80%" "search popup height is 80%"
}

test_tmux_popup_exit_flag() {
    echo "Test: pogo.tmux popups use -E flag (close on command exit)"
    local log="$MOCK_DIR/tmux_calls.log"
    create_mock "tmux" '
echo "$@" >> '"$log"'
'
    bash "$SCRIPT_DIR/pogo.tmux"

    local bind_p bind_s
    bind_p="$(grep "bind-key P" "$log" 2>/dev/null || true)"
    bind_s="$(grep "bind-key S" "$log" 2>/dev/null || true)"

    assert_contains "$bind_p" "-E" "project switcher uses -E flag"
    assert_contains "$bind_s" "-E" "search popup uses -E flag"
}

test_tmux_hook_runs_in_background() {
    echo "Test: pane-focus-in hook runs pogo visit in background"
    local log="$MOCK_DIR/tmux_calls.log"
    create_mock "tmux" '
echo "$@" >> '"$log"'
'
    bash "$SCRIPT_DIR/pogo.tmux"

    local hook_call
    hook_call="$(grep "set-hook" "$log" 2>/dev/null || true)"
    assert_contains "$hook_call" "&" "pogo visit runs in background (trailing &)"
}

# ============================================================
# Test Suite 3: Daemon health check behavior
# ============================================================

test_health_daemon_not_running() {
    echo "Test: status script handles daemon not running (pogo visit works, status fails)"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/my-project"
    exit 0
elif [ "$1" = "status" ]; then
    # Daemon not running — status command fails
    exit 1
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/my-project")"
    assert_eq "my-project" "$out" "shows bare name when daemon is down"
}

test_health_project_not_registered() {
    echo "Test: status script handles unregistered project"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    # Project not recognized
    exit 1
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/tmp/not-a-project" 2>/dev/null)" || true
    assert_empty "$out" "no output for unregistered project"
}

test_health_multiple_projects_in_status() {
    echo "Test: status script picks correct project from multi-project status output"
    create_mock "pogo" '
if [ "$1" = "visit" ]; then
    echo "/home/user/project-b"
    exit 0
elif [ "$1" = "status" ]; then
    echo "ready        /home/user/project-a (10 files)"
    echo "indexing     /home/user/project-b (5 files)"
    echo "stale        /home/user/project-c (20 files)"
    exit 0
fi
'
    local out
    out="$(bash "$SCRIPT_DIR/pogo-status.sh" "/home/user/project-b/src")"
    assert_eq "project-b ⟳" "$out" "picks correct project from multi-project status"
}

# ============================================================
# Run all tests
# ============================================================

main() {
    echo "=== pogo tmux integration tests ==="
    echo ""

    setup

    echo "--- Status bar rendering ---"
    test_status_no_args
    test_status_empty_path
    test_status_visit_fails
    test_status_visit_returns_empty
    test_status_ready
    test_status_indexing
    test_status_stale
    test_status_unindexed
    test_status_unknown
    test_status_no_status_output
    test_status_pogo_status_fails
    echo ""

    echo "--- Keybinding & hook registration ---"
    test_tmux_registers_hooks_and_keys
    test_tmux_popup_dimensions
    test_tmux_popup_exit_flag
    test_tmux_hook_runs_in_background
    echo ""

    echo "--- Daemon health check ---"
    test_health_daemon_not_running
    test_health_project_not_registered
    test_health_multiple_projects_in_status
    echo ""

    teardown

    echo "=== Results: $PASS passed, $FAIL failed ==="
    if [ "$FAIL" -gt 0 ]; then
        echo -e "\nFailures:$ERRORS"
        exit 1
    fi
}

main
