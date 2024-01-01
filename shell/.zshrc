################################################################################
################### Start Pogo #################################################
################################################################################
## Expands a path
dir_resolve()
{
    cd "$1" 2>/dev/null || return $?  # cd to desired directory; if fail, quell any error messages but return exit status
    echo "`pwd -P`" # output full, link-resolved path
}

## Below are some environment variables you should modify
## 1. Add the pogo binaries to your path.
export POGO_BINARY_PATH=$(dir_resolve ~/dev/pogo/bin)
## 2. Set POGO_HOME to the location where the dotfiles will live.
export POGO_HOME=$(dir_resolve ~)

## These shouldn't require modification
export POGO_PLUGIN_PATH="$POGO_BINARY_PATH/plugin"
export PATH="$POGO_BINARY_PATH:$PATH"
chpwd() {
    pogo visit $(pwd | xargs realpath) > "$POGO_HOME/.pogo-cli-log.txt" 2>&1
}

alias sp="cd \"\$(lsp | fzf)\""

################################################################################
################### End Pogo ###################################################
################################################################################
