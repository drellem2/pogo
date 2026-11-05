#!/bin/bash
# =============================================================================
# BUILD AND TEST POGO'S GATE WITH -trimpath (drellem2/pogo#282)
# =============================================================================
#
# Without -trimpath, Go puts each package's ABSOLUTE source directory into the
# build cache's action ID (cmd/go/internal/work/exec.go). Every polecat runs
# this gate in its own worktree at its own path, so every worktree compiled and
# cached a private copy of every pogo package and test binary. Go's cache trim
# drops only entries unused for 5 days, and the polecat working set turns over
# much faster than that, so the cache held tens of GB of identical builds, keyed
# apart only by the path. The 2026-11-04 triage measured it: one tree at two
# paths with a fresh GOCACHE cost +25M for the second path on
# `go build ./cmd/lsp`, and +0.4M with -trimpath.
#
# go_trimpath_enable adds -trimpath to GOFLAGS, so every `go` the calling
# script runs, and every child that inherits the environment (a test that shells
# out to `go build`), records module-relative paths instead. A GOFLAGS the
# caller already set is kept, and -trimpath is not added twice. An explicit
# -trimpath=false in the caller's GOFLAGS is an opt-out and is respected: GOFLAGS
# is left exactly as it was (appending -trimpath after it would win, since the
# last occurrence of a flag does).
#
# What -trimpath changes for code: runtime.Caller and stack traces report
# "github.com/drellem2/pogo/internal/x/y.go" instead of an absolute path. A
# test that derives the repo root from runtime.Caller(0) therefore resolves a
# directory that does not exist, and the guards that did so SKIPPED rather than
# failed. They now use os.Getwd(), which `go test` sets to the package
# directory. Do not reintroduce runtime.Caller for locating source files.
#
# What -trimpath changes for binaries: Go stops recording the -ldflags setting
# in build info (go.dev/issue/52372, cmd/go/internal/load/pkg.go), and
# internal/selfdrift reads that setting to recognise an enth build. So:
#
#   test.sh   calls go_trimpath_enable. The test compile is the bulk of the
#             per-worktree cache growth, and its binaries are thrown away.
#   build.sh  does NOT call it, and sets the flag per command instead: its
#             ./bin `go build` passes -trimpath (per-worktree scratch, ~1M per
#             extra worktree instead of ~60M, mg-21a96), and its `go install`
#             passes -trimpath=false, because the installed binaries are what
#             the nightly deploy ships and internal/selfdrift reads their
#             -ldflags record. An explicit flag overrides GOFLAGS, so neither
#             line depends on what the caller exported. A test that builds a
#             binary standing in for an INSTALLED one passes -trimpath=false.
#
# Scope: pogo's own gate. Other repos' gates are unchanged.
#
# POGO_NO_TRIMPATH=1 turns it off, for measuring the cache growth it prevents.

go_trimpath_enable() {
  if [ -n "${POGO_NO_TRIMPATH:-}" ]; then
    return 0
  fi
  case " ${GOFLAGS:-} " in
    *" -trimpath "* | *" -trimpath=true "* | *" --trimpath "* | *" --trimpath=true "*) ;;
    *" -trimpath=false "* | *" --trimpath=false "*) ;;
    *) GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath" ;;
  esac
  export GOFLAGS
}
