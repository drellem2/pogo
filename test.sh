export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go test github.com/marginalia-gaming/pogo/internal
