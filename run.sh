export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go build -o bin/plugin/pogo-plugin-search search/search_impl.go &&  \
go run cmd/main.go
