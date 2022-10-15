export GOPATH=$(cd .. && pwd)
echo "Setting GOPATH to " $GOPATH
find . -name main.go | xargs go run
