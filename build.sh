echo "Starting build"
./fmt.sh && ./test.sh && \
export GOPATH=$(cd .. && pwd) && \
echo "Setting GOPATH to " $GOPATH && \
echo "Step 3: Building binaries..." && \
GO111MODULE=on go install github.com/drellem2/pogo/cmd/...

