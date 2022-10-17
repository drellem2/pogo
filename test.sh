export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go build -o search/search search/search_impl.go &&  \
go test github.com/marginalia-gaming/pogo/internal/project -coverprofile=coverage-internal.out
