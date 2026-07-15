#!/bin/bash
# Every step below must fail the whole script. Without set -e (and with the
# old `echo && cmd` chaining), the script's exit status was only the LAST
# command's — a failing `go test` was silently masked and the refinery gate
# merged a branch whose tests panicked (mg-59d5).
set -e

echo "Step 2: Testing..."
echo "Making test directories"
mkdir -p _testdata/a-service/.git
mkdir -p _testdata/b-service/.git

echo "Testing Go packages"
go test ./...

echo "Testing neovim plugin"
bash nvim/test_nvim.sh

echo "Testing bash shell integration"
bash shell/bashrc_test.sh

echo "Testing pogo-self-deploy driver"
bash scripts/pogo-self-deploy_test.sh

echo "Testing build.sh"
bash build_test.sh
