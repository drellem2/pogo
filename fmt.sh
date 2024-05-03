echo "Step 1: Formatting..."
export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
go fmt github.com/drellem2/pogo/...
