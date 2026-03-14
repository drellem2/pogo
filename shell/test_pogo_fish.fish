#!/usr/bin/env fish
# Tests for pogo fish shell integration (shell/pogo.fish)
#
# Run: fish shell/test_pogo_fish.fish
#
# Uses a simple TAP-like output format. No external dependencies required.

set -g test_count 0
set -g test_pass 0
set -g test_fail 0

function ok -a description
    set test_count (math $test_count + 1)
    set test_pass (math $test_pass + 1)
    echo "ok $test_count - $description"
end

function not_ok -a description
    set test_count (math $test_count + 1)
    set test_fail (math $test_fail + 1)
    echo "not ok $test_count - $description"
end

function assert_equal -a got expected description
    if test "$got" = "$expected"
        ok "$description"
    else
        not_ok "$description"
        echo "  # got:      '$got'"
        echo "  # expected: '$expected'"
    end
end

function assert_defined -a varname description
    if set -q $varname
        ok "$description"
    else
        not_ok "$description"
        echo "  # variable '$varname' is not defined"
    end
end

function assert_contains -a haystack needle description
    if string match -q "*$needle*" -- "$haystack"
        ok "$description"
    else
        not_ok "$description"
        echo "  # '$haystack' does not contain '$needle'"
    end
end

function assert_function_exists -a funcname description
    if functions -q $funcname
        ok "$description"
    else
        not_ok "$description"
        echo "  # function '$funcname' does not exist"
    end
end

# --- Setup ---

# Create a temp directory for test isolation
set -g test_tmpdir (mktemp -d)

# Create a mock pogo binary that logs its invocations
set -g mock_bin_dir "$test_tmpdir/bin"
mkdir -p $mock_bin_dir
set -g mock_log "$test_tmpdir/pogo_calls.log"

echo '#!/usr/bin/env fish
echo $argv >> '$mock_log'
' > "$mock_bin_dir/pogo"
chmod +x "$mock_bin_dir/pogo"

# Create a mock lsp binary
echo '#!/usr/bin/env fish
echo "/tmp/project-a"
echo "/tmp/project-b"
' > "$mock_bin_dir/lsp"
chmod +x "$mock_bin_dir/lsp"

# Override env vars before sourcing pogo.fish
set -gx POGO_BINARY_PATH $mock_bin_dir
set -gx POGO_HOME $test_tmpdir

# Prepend mock bin to PATH so our mock pogo is found
set -gx PATH $mock_bin_dir $PATH

# Source the fish integration
source (status dirname)/pogo.fish

echo "TAP version 13"
echo "# Testing pogo fish shell integration"

# --- Test 1: Environment variables ---

echo "# --- Environment Variables ---"

assert_defined POGO_BINARY_PATH "POGO_BINARY_PATH is set"
assert_defined POGO_HOME "POGO_HOME is set"
assert_defined POGO_PLUGIN_PATH "POGO_PLUGIN_PATH is set"

assert_equal "$POGO_PLUGIN_PATH" "$POGO_BINARY_PATH/plugin" \
    "POGO_PLUGIN_PATH is POGO_BINARY_PATH/plugin"

assert_contains "$PATH" "$POGO_BINARY_PATH" \
    "PATH contains POGO_BINARY_PATH"

# --- Test 2: __pogo_on_pwd function ---

echo "# --- PWD Hook ---"

assert_function_exists __pogo_on_pwd \
    "__pogo_on_pwd function exists"

# Clear any prior log from sourcing
echo -n > $mock_log

# Trigger the PWD hook by changing directory
set -g old_pwd $PWD
set test_dir "$test_tmpdir/test_project"
mkdir -p $test_dir
cd $test_dir

# Give the hook a moment (it fires on PWD variable change)
# Read the mock log to see if pogo visit was called
set -l log_content (cat $mock_log 2>/dev/null; or echo "")
set -l resolved_test_dir (realpath $test_dir)

assert_contains "$log_content" "visit" \
    "__pogo_on_pwd calls 'pogo visit' on directory change"

assert_contains "$log_content" "$resolved_test_dir" \
    "__pogo_on_pwd passes realpath of new PWD to pogo visit"

# Verify output goes to log file, not stdout
set -l cli_log "$POGO_HOME/.pogo-cli-log.txt"
if test -f $cli_log
    ok "pogo visit output redirected to .pogo-cli-log.txt"
else
    not_ok "pogo visit output redirected to .pogo-cli-log.txt"
    echo "  # expected file: $cli_log"
end

# Change to another directory and verify hook fires again
echo -n > $mock_log
set test_dir2 "$test_tmpdir/test_project2"
mkdir -p $test_dir2
cd $test_dir2

set -l log_content2 (cat $mock_log 2>/dev/null; or echo "")
assert_contains "$log_content2" "visit" \
    "__pogo_on_pwd fires again on subsequent directory change"

# Return to original directory
cd $old_pwd

# --- Test 3: sp abbreviation ---

echo "# --- Project Switcher ---"

# Check that sp abbreviation is defined
set -l abbr_list (abbr --show 2>/dev/null; or abbr -s 2>/dev/null; or echo "")
if string match -q '*sp*' -- "$abbr_list"
    ok "sp abbreviation is defined"
else
    not_ok "sp abbreviation is defined"
    echo "  # abbreviations: $abbr_list"
end

# Verify sp expands to use lsp and fzf
if string match -q '*lsp*fzf*' -- "$abbr_list"
    ok "sp abbreviation uses lsp piped to fzf"
else
    not_ok "sp abbreviation uses lsp piped to fzf"
    echo "  # abbreviations: $abbr_list"
end

# --- Test 4: Verify pogo visit uses realpath ---

echo "# --- Realpath Handling ---"

# Create a symlink and cd to it
set -l link_target "$test_tmpdir/real_dir"
set -l link_path "$test_tmpdir/symlink_dir"
mkdir -p $link_target
ln -sf $link_target $link_path

echo -n > $mock_log
cd $link_path

set -l log_content3 (cat $mock_log 2>/dev/null; or echo "")
set -l resolved_target (realpath $link_target)
assert_contains "$log_content3" "$resolved_target" \
    "pogo visit receives resolved realpath, not symlink path"

cd $old_pwd

# --- Cleanup ---

rm -rf $test_tmpdir

# --- Summary ---

echo ""
echo "1..$test_count"
echo "# passed: $test_pass"
echo "# failed: $test_fail"

if test $test_fail -gt 0
    echo "# FAIL"
    exit 1
else
    echo "# ALL TESTS PASSED"
    exit 0
end
