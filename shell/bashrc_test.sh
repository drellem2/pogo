#!/usr/bin/env bash
# Tests for bash shell integration (shell/.bashrc)
# Verifies: env vars, cd hook, sp alias, PROMPT_COMMAND setup

RESULTS_FILE=$(mktemp)
trap 'rm -f "$RESULTS_FILE"' EXIT

pass() { echo "PASS: $1"; echo "PASS: $1" >> "$RESULTS_FILE"; }
fail() { echo "FAIL: $1"; echo "FAIL: $1" >> "$RESULTS_FILE"; }

# Create isolated test environment
TEST_DIR=$(mktemp -d)

MOCK_BIN="$TEST_DIR/bin"
mkdir -p "$MOCK_BIN" "$MOCK_BIN/plugin"

cat > "$MOCK_BIN/pogo" << 'MOCK'
#!/usr/bin/env bash
echo "$@" >> "${POGO_TEST_LOG:-/dev/null}"
MOCK
chmod +x "$MOCK_BIN/pogo"

cat > "$MOCK_BIN/lsp" << 'MOCK'
#!/usr/bin/env bash
echo "/tmp/project-a"
echo "/tmp/project-b"
MOCK
chmod +x "$MOCK_BIN/lsp"

POGO_TEST_LOG="$TEST_DIR/pogo_calls.log"
export POGO_TEST_LOG
touch "$POGO_TEST_LOG"

SOURCE_FILE="$(cd "$(dirname "$0")" && pwd -P)/.bashrc"

# Helper: source .bashrc with a fake HOME that has the expected directory layout
source_bashrc() {
    export HOME="$TEST_DIR"
    mkdir -p "$TEST_DIR/dev/pogo/bin/plugin"
    source "$SOURCE_FILE" 2>/dev/null || true
}

echo "=== Bash Shell Integration Tests ==="
echo ""

###############################################################################
# Test 1: dir_resolve function
###############################################################################
echo "--- dir_resolve ---"

(
    source_bashrc
    result=$(dir_resolve /tmp)
    if [ "$result" = "/private/tmp" ] || [ "$result" = "/tmp" ]; then
        pass "dir_resolve resolves real path"
    else
        fail "dir_resolve returned '$result', expected /tmp or /private/tmp"
    fi
)

(
    source_bashrc
    if ! dir_resolve /nonexistent/path 2>/dev/null; then
        pass "dir_resolve returns non-zero for invalid path"
    else
        fail "dir_resolve should fail for invalid path"
    fi
)

###############################################################################
# Test 2: Environment variables
###############################################################################
echo "--- Environment Variables ---"

(
    source_bashrc

    if [ -n "${POGO_BINARY_PATH:-}" ]; then
        pass "POGO_BINARY_PATH is set"
    else
        fail "POGO_BINARY_PATH is not set"
    fi

    if [ -n "${POGO_HOME:-}" ]; then
        pass "POGO_HOME is set"
    else
        fail "POGO_HOME is not set"
    fi

    if [ "${POGO_PLUGIN_PATH:-}" = "${POGO_BINARY_PATH}/plugin" ]; then
        pass "POGO_PLUGIN_PATH equals POGO_BINARY_PATH/plugin"
    else
        fail "POGO_PLUGIN_PATH='${POGO_PLUGIN_PATH:-}' (expected '${POGO_BINARY_PATH:-}/plugin')"
    fi

    if echo "$PATH" | tr ':' '\n' | grep -qF "$POGO_BINARY_PATH"; then
        pass "POGO_BINARY_PATH is in PATH"
    else
        fail "POGO_BINARY_PATH not found in PATH"
    fi
)

###############################################################################
# Test 3: PROMPT_COMMAND / cd hook
###############################################################################
echo "--- cd hook (PROMPT_COMMAND) ---"

(
    unset PROMPT_COMMAND
    source_bashrc

    if [[ "${PROMPT_COMMAND:-}" == *"__pogo_chpwd"* ]]; then
        pass "PROMPT_COMMAND contains __pogo_chpwd (fresh)"
    else
        fail "PROMPT_COMMAND='${PROMPT_COMMAND:-}' (expected __pogo_chpwd)"
    fi
)

(
    export PROMPT_COMMAND="existing_hook"
    source_bashrc

    if [[ "$PROMPT_COMMAND" == *"__pogo_chpwd"* ]] && [[ "$PROMPT_COMMAND" == *"existing_hook"* ]]; then
        pass "PROMPT_COMMAND preserves existing hooks"
    else
        fail "PROMPT_COMMAND='$PROMPT_COMMAND' (should contain both)"
    fi
)

(
    source_bashrc
    export PATH="$MOCK_BIN:$PATH"
    export POGO_HOME="$TEST_DIR"

    > "$POGO_TEST_LOG"
    __pogo_last_dir=""
    cd /tmp
    __pogo_chpwd

    if grep -q "visit" "$POGO_TEST_LOG" 2>/dev/null; then
        pass "cd hook calls pogo visit on directory change"
    else
        fail "cd hook did not call pogo visit"
    fi
)

(
    source_bashrc
    export PATH="$MOCK_BIN:$PATH"
    export POGO_HOME="$TEST_DIR"

    cd /tmp
    __pogo_last_dir="$(pwd -P)"
    > "$POGO_TEST_LOG"
    __pogo_chpwd

    if [ ! -s "$POGO_TEST_LOG" ]; then
        pass "cd hook skips when directory unchanged"
    else
        fail "cd hook re-triggered for same directory"
    fi
)

(
    source_bashrc
    export PATH="$MOCK_BIN:$PATH"
    export POGO_HOME="$TEST_DIR"

    cd /tmp
    __pogo_last_dir="$(pwd -P)"
    > "$POGO_TEST_LOG"
    cd /
    __pogo_chpwd

    if [ -s "$POGO_TEST_LOG" ]; then
        pass "cd hook triggers on new directory"
    else
        fail "cd hook did not trigger on new directory"
    fi
)

###############################################################################
# Test 4: sp alias
###############################################################################
echo "--- sp alias ---"

(
    source_bashrc
    shopt -s expand_aliases 2>/dev/null || true

    alias_def=$(alias sp 2>/dev/null || echo "NOT_SET")
    if [[ "$alias_def" == *"lsp"* ]] && [[ "$alias_def" == *"fzf"* ]]; then
        pass "sp alias pipes lsp through fzf"
    else
        fail "sp alias not defined correctly: $alias_def"
    fi

    if [[ "$alias_def" == *"cd"* ]]; then
        pass "sp alias uses cd to change directory"
    else
        fail "sp alias should cd to selected project"
    fi
)

###############################################################################
# Cleanup & Summary
###############################################################################
rm -rf "$TEST_DIR"

echo ""
PASS_COUNT=$(grep -c '^PASS:' "$RESULTS_FILE" 2>/dev/null || true)
FAIL_COUNT=$(grep -c '^FAIL:' "$RESULTS_FILE" 2>/dev/null || true)
PASS_COUNT=${PASS_COUNT:-0}
FAIL_COUNT=${FAIL_COUNT:-0}
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="

if [ "$FAIL_COUNT" -gt 0 ]; then
    echo ""
    echo "Failures:"
    grep '^FAIL:' "$RESULTS_FILE" | sed 's/^/  /'
    exit 1
fi
