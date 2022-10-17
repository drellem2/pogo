export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go fmt github.com/marginalia-gaming/pogo/cmd
go fmt github.com/marginalia-gaming/pogo/internal/driver
go fmt github.com/marginalia-gaming/pogo/internal/project
