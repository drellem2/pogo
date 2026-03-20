#!/bin/bash

skip_tests=false

for arg in "$@"; do
  case "$arg" in
    --skip-tests)
      skip_tests=true
      ;;
  esac
done

echo "Starting build"
./fmt.sh || exit 1

if [ "$skip_tests" = false ]; then
  ./test.sh || exit 1
fi

echo "Step 3: Building binaries..." && \
go install ./cmd/...
