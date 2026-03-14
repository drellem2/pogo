################################################################################
################### Start Pogo #################################################
################################################################################

## Below are some environment variables you should modify
## 1. Add the pogo binaries to your path.
set -gx POGO_BINARY_PATH (realpath ~/dev/pogo/bin 2>/dev/null; or echo ~/dev/pogo/bin)
## 2. Set POGO_HOME to the location where the dotfiles will live.
set -gx POGO_HOME (realpath ~ 2>/dev/null; or echo ~)

## These shouldn't require modification
set -gx POGO_PLUGIN_PATH "$POGO_BINARY_PATH/plugin"
fish_add_path --prepend $POGO_BINARY_PATH

## Auto-register projects on directory change
function __pogo_on_pwd --on-variable PWD
    pogo visit (realpath $PWD) > "$POGO_HOME/.pogo-cli-log.txt" 2>&1
end

## sp - quick project switcher via fzf
abbr -a sp 'cd (lsp | fzf)'

################################################################################
################### End Pogo ###################################################
################################################################################
