echo "Starting build"
./fmt.sh && ./test.sh && \
export GOPATH=$(cd .. && pwd) && \
echo "Setting GOPATH to " $GOPATH && \
echo "Step 3: Building binaries..." && \
find search -name "[a-zA-Z]*.go" | xargs go build -o bin/plugin/pogo-plugin-search  &&  \
go build -o bin/pogod cmd/main_pogod.go && \
go build -o bin/pogo cmd/main_pogo.go && \
go build -o bin/lsp cmd/main_lsp.go    
