echo "Step 1: Formatting..."
export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
find search -name "*.go" | xargs go fmt
go fmt github.com/marginalia-gaming/pogo/cmd
go fmt github.com/marginalia-gaming/pogo/internal/driver
go fmt github.com/marginalia-gaming/pogo/internal/project
