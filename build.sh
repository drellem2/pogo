echo "Starting build"
./fmt.sh && ./test.sh && \
echo "Step 3: Building binaries..." && \
go install ./cmd/...
