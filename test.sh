echo "Step 2: Testing..."
echo "Making test directories"
mkdir -p _testdata/a-service/.git
mkdir -p _testdata/b-service/.git

echo "Testing Go packages" && \
go test ./...

echo "Testing neovim plugin" && \
bash nvim/test_nvim.sh
