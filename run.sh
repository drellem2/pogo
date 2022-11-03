export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
find search -name "[a-zA-Z]*.go" | xargs go build -o bin/plugin/pogo-plugin-search  &&  \
go run cmd/main.go
