export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go fmt github.com/marginalia-gaming/pogo/internal
