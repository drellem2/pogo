################################################################################
################### Start Pogo #################################################
################################################################################

chpwd() {
    pogo visit $(pwd | realpath) > ~/.pogo-cli-log.txt 2>&1
}

################################################################################
################### End Pogo ###################################################
################################################################################