unameOut="$(uname -s)"
case "${unameOut}" in
    Linux*)     windows=false;;
    Darwin*)    windows=false;;
    CYGWIN*)    windows=true;;
    MINGW*)     windows=true;;
    MSYS_NT*)   windows=true;;
    *)          windows=false
esac

pogodout=bin/pogod
pogoout=bin/pogo
lspout=bin/lsp
poseout=bin/pose
ppsout=bin/plugin/pogo-plugin-search

if $windows 
then
    pogodout="$pogodout".exe
    pogoout="$pogoout".exe
    lspout="$lspout".exe
    poseout="$poseout".exe
    ppsout="$ppsout".exe
fi

echo "Starting build"
./fmt.sh && ./test.sh && \
export GOPATH=$(cd .. && pwd) && \
echo "Setting GOPATH to " $GOPATH && \
echo "Step 3: Building binaries..." && \
find search -name "[a-zA-Z]*.go" | xargs go build -o "$ppsout"  &&  \
go build -o "$pogodout" cmd/main_pogod.go && \
go build -o "$pogoout" cmd/main_pogo.go && \
go build -o "$lspout" cmd/main_lsp.go && \
go build -o "$poseout" cmd/main_pose.go    
