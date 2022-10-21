export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
echo "Making test directories"
mkdir -p _testdata/a-service/.git
mkdir -p _testdata/b-service/.git

echo "Building"
find search -name "*.go" | xargs go build -o bin/plugin/pogo-plugin-search  &&  \
go test github.com/marginalia-gaming/pogo/internal/project -coverprofile=coverage-internal-project.out && \
go test github.com/marginalia-gaming/pogo/internal/driver -coverprofile=coverage-internal-driver.out
