// Package logging resolves the process-wide level for pogo's hclog loggers.
//
// Before gh#111 every logger in the daemon hardcoded hclog.Info and pogod
// exposed only -bind and -port, so an operator had no way to turn the volume
// down — or, more usefully, up. POGO_LOG_LEVEL is that control.
//
// It is an environment variable rather than a config key because it is read
// once at logger construction and so needs no reload semantics. A config key
// that can be edited while the daemon runs implies the edit will be picked up,
// and honoring that needs reload machinery this does not have; that key is
// tracked separately (mg-44d6) and is not this.
//
// Being an env var is not free: under launchd nothing inherits the invoking
// shell's environment, so a launchd-managed pogod only sees this if it is
// declared in the job's plist. scripts/launchd/com.pogo.daemon.plist ships a
// commented-out key for that, and docs/customizing.md says so.
//
// This does not reach every logger in the daemon. internal/driver builds its
// plugin loggers at hclog.Debug independently, so POGO_LOG_LEVEL does not
// quiet them.
package logging

import (
	"os"

	"github.com/hashicorp/go-hclog"
)

// EnvLogLevel names the environment variable that sets the level of every
// logger pogo constructs through this package.
const EnvLogLevel = "POGO_LOG_LEVEL"

// DefaultLevel is the level used when POGO_LOG_LEVEL says nothing usable. It
// is the level every logger hardcoded before this variable existed, so an
// unset environment behaves exactly as it did.
const DefaultLevel = hclog.Info

// Level returns the level named by POGO_LOG_LEVEL, or DefaultLevel when the
// variable is unset, empty, or not a name hclog recognizes.
func Level() hclog.Level {
	return LevelFrom(os.Getenv(EnvLogLevel))
}

// LevelFrom parses a level name the way hclog does — case-insensitively, with
// surrounding whitespace ignored — and falls back to DefaultLevel for anything
// it does not recognize.
//
// An unparseable value falls back rather than failing the process. A typo in
// POGO_LOG_LEVEL should cost the operator the level they asked for, not the
// daemon: refusing to start over a log setting turns a cosmetic mistake into
// an outage, and exiting is not even reliably visible under launchd.
func LevelFrom(s string) hclog.Level {
	// hclog.LevelFromString returns NoLevel for anything it cannot parse,
	// including the empty string. NoLevel is not a threshold — a logger built
	// with it emits nothing through the level-filtered entry points — so it
	// must never be passed through as if it were the operator's choice.
	if lvl := hclog.LevelFromString(s); lvl != hclog.NoLevel {
		return lvl
	}
	return DefaultLevel
}
