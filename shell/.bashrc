################################################################################
################### Start Pogo #################################################
################################################################################
## Expands a path
dir_resolve()
{
    cd "$1" 2>/dev/null || return $?
    pwd -P
    cd - > /dev/null 2>&1
}

## Below are some environment variables you should modify
## 1. Add the pogo binaries to your path.
export POGO_BINARY_PATH=$(dir_resolve ~/dev/pogo/bin)
## 2. Set POGO_HOME to the location where the dotfiles will live.
export POGO_HOME=$(dir_resolve ~)

## These shouldn't require modification
export POGO_PLUGIN_PATH="$POGO_BINARY_PATH/plugin"
export PATH="$POGO_BINARY_PATH:$PATH"

## Track directory changes via PROMPT_COMMAND
__pogo_last_dir=""
__pogo_chpwd() {
    local cur
    cur="$(pwd -P)"
    if [ "$cur" != "$__pogo_last_dir" ]; then
        __pogo_last_dir="$cur"
        pogo visit "$cur" > "$POGO_HOME/.pogo-cli-log.txt" 2>&1
    fi
}

# Append to PROMPT_COMMAND so we don't clobber existing hooks
if [ -z "$PROMPT_COMMAND" ]; then
    PROMPT_COMMAND="__pogo_chpwd"
else
    PROMPT_COMMAND="__pogo_chpwd;${PROMPT_COMMAND}"
fi

alias sp='cd "$(lsp | fzf)"'

################################################################################
################### End Pogo ###################################################
################################################################################
