# Development

Pogo is a Go project. Binaries live in `cmd/`: `cmd/pogo`, `cmd/lsp`, `cmd/pose`, `cmd/pogod` (daemon).

## Build from source

```sh
git clone https://github.com/drellem2/pogo.git && cd pogo && ./build.sh
```

Requires [Go](https://go.dev/dl/) 1.21+.

```sh
./build.sh       # Format, test, build, install
./test.sh        # Run tests only
./fmt.sh         # Format code only
```

Always run `./build.sh` before committing. If it fails, fix the issue before pushing.

## Pre-commit hook

```sh
git config core.hooksPath hooks
```

The hook runs `gofmt -l` and `go build ./...` on every commit.

## End-to-end smoke test

`scripts/test-e2e.sh` exercises the full loop — `pogo init`, `pogod`, mayor
auto-start, polecat spawn, refinery merge, gate-failure rejection, and crew
crash → respawn — against a sandboxed `$HOME`, a non-default port, and a
fake-agent stand-in for `claude`. No API keys required.

```sh
scripts/test-e2e.sh                  # ~30s; per-step PASS/FAIL summary
POGO_E2E_KEEP=1 scripts/test-e2e.sh  # leave the sandbox dir on disk to inspect
POGO_E2E_PORT=20000 scripts/test-e2e.sh
```

The test is also wrapped as a Go test, skipped by default so it doesn't slow
`go test ./...`. To run it through the Go toolchain:

```sh
POGO_RUN_E2E=1 go test ./internal/agent -run TestE2ESmoke -v -timeout 5m
```

Requires `mg` (macguffin) on `$PATH`.
