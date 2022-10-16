export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go build -o search/search search/search_impl.go &&  \
go run cmd/main.go
