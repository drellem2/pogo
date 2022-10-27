export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
find search -name "*.go" | xargs go build -o bin/plugin/pogo-plugin-search  &&  \
go build -o bin/pogo cmd/main.go
