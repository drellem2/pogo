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

# The live control for the mg-de08 mail-check post-check (mg-c02d). Stands up a
# sandboxed pogod and drives the ASSEMBLED verify path — the unit test above
# only proves the pure classifier can fail. Costs ~40s (pogod holds its
# mail-check reap for 30s after boot); that is the price of the only assertion
# that shows the redeploy post-check can go RED at all, and mg-f206's unattended
# nightly redeploy rests on it.
echo "Testing pogo-self-deploy live mail-check control"
bash scripts/pogo-self-deploy_live_test.sh

echo "Testing build.sh"
bash build_test.sh
