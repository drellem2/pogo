echo "Step 2: Testing..."
export GOPATH=$(cd .. && pwd)
export POGO_PLUGIN_PATH=$(cd ./bin/plugin && pwd)
echo "Setting GOPATH to " $GOPATH
echo "Setting POGO_PLUGIN_PATH to " $POGO_PLUGIN_PATH
echo "Making test directories"
mkdir -p _testdata/a-service/.git
mkdir -p _testdata/b-service/.git

echo "Testing pogo" && \
go test github.com/drellem2/pogo/internal/driver && \
go test github.com/drellem2/pogo/internal/plugins/search && \
go test github.com/drellem2/pogo/internal/project
    
