export GOPATH=$(cd .. && pwd)
export POGO_PLUGIN_PATH=$(cd ./bin/plugin && pwd)
echo "Setting GOPATH to " $GOPATH
echo "Setting POGO_PLUGIN_PATH to " $POGO_PLUGIN_PATH
find search -name "[a-zA-Z]*.go" | xargs go build -o bin/plugin/pogo-plugin-search  &&  \
go run cmd/main.go
