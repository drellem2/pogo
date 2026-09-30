package ghtoken

import (
	"context"
	"log"
	"strings"
	"sync"
)

// ChildEnv returns base with a GitHub credential for ONE child process, fetched
// now, for that child only (mg-37183).
//
// It is the per-call replacement for Ensure in a long-lived process. Ensure
// writes the token into the calling process's own environment, where it is
// inherited by every child spawned afterwards — agents, gates, hooks — and goes
// stale the moment the token is rotated (mg-4d59). ChildEnv writes it nowhere
// but the slice it returns, which the caller assigns to one exec.Cmd's Env and
// drops. The process that calls it never holds the value in its environment,
// and a token rotated in ~/.zshenv is picked up by the very next call.
//
// Who needs it: `gh` itself, and `git` NETWORK operations against an https
// github.com remote — on this box git's credential helper for github.com is
// `gh auth git-credential`, which reads GH_TOKEN from ITS environment, i.e. the
// git child's. A `git push` from a process without the token exits 128 there.
//
// The chain is Ensure's, minus the write: a base that already carries a
// non-blank GH_TOKEN or GITHUB_TOKEN is returned unchanged (nothing is asked);
// otherwise the user shell, then `gh auth token`. When no source yields, base
// is returned unchanged and the child fails exactly as it would have without
// this call — the caller already classifies that failure, and this function
// adds no new error path to it.
//
// Logging is existence-only and TRANSITION-only: one line when the chain stops
// yielding a credential, one when it starts again. A line per call would put a
// line in pogod.log for every fetch the refinery makes; a line per transition
// is the one an operator needs, and it names each source's reason, never a
// value.
func ChildEnv(base []string) []string {
	return ChildEnvContext(context.Background(), base)
}

// ChildEnvContext is ChildEnv with the credential fetch bounded by ctx as well
// as by each source's own 15s probe timeout (mg-c258b). A caller whose child
// runs under exec.CommandContext(ctx, …) must pass that same ctx: the fetch
// runs BEFORE the child starts, so under plain ChildEnv a hung source (a
// wedged `gh auth token`, a slow shell init) spends up to 15s per source
// outside the caller's deadline — a 300ms-bounded gh lookup then took 15s on
// a host with no ambient GH_TOKEN (pogo CI, drellem2/pogo main since 50076f4).
//
// What does NOT change: the chain, its order, and that the value goes only to
// the returned slice. When ctx ends before a source yields, base is returned
// unchanged; the child, started under the same expired ctx, fails as a
// timeout, which is how the caller already classifies it. A fetch cut short
// by the caller's deadline says nothing about whether a credential exists, so
// it is not logged as the chain going UNAVAILABLE.
func ChildEnvContext(ctx context.Context, base []string) []string {
	childMu.Lock()
	shellH, ghH := childShellHarvest, childGHHarvest
	childMu.Unlock()
	env, res := childEnv(base,
		func() (string, error) { return shellH(ctx) },
		func() (string, error) { return ghH(ctx) })
	if !res.OK() && ctx.Err() != nil {
		return env
	}
	noteChildResult(res)
	return env
}

// childEnv is the injectable core. The returned Result says where the token
// came from (SourceAmbient when base already had one) and never what it is.
func childEnv(base []string, shellHarvest, ghHarvest func() (string, error)) ([]string, Result) {
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if strings.TrimSpace(lookupEnv(base, k)) != "" {
			return base, Result{Source: SourceAmbient}
		}
	}
	env := base
	res := harvest(shellHarvest, ghHarvest, func(tok string) error {
		// A fresh slice, so the caller's base (often os.Environ()'s, but it
		// could be shared) is never written through. os/exec uses the LAST
		// value of a duplicated key, so a blank GH_TOKEN= earlier in base is
		// overridden rather than needing to be removed.
		env = append(append(make([]string, 0, len(base)+1), base...), "GH_TOKEN="+tok)
		return nil
	})
	return env, res
}

// lookupEnv returns the LAST value of key in env, matching os/exec's rule for
// duplicated keys.
func lookupEnv(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			val = kv[len(prefix):]
		}
	}
	return val
}

var (
	childMu           sync.Mutex
	childShellHarvest = func(ctx context.Context) (string, error) { return shellHarvestCtx(ctx, UserShell()) }
	childGHHarvest    = ghAuthTokenCtx
	childLastOK       *bool
	childLogf         = log.Printf
)

// noteChildResult logs a change between "a credential was available" and "none
// was". The first call logs only when it finds none, so a healthy daemon's log
// carries no credential line at all.
func noteChildResult(res Result) {
	childMu.Lock()
	defer childMu.Unlock()
	ok := res.OK()
	switch {
	case childLastOK == nil && ok:
	case childLastOK == nil || *childLastOK != ok:
		if ok {
			childLogf("ghtoken: per-call gh/git credential available again (source=%s)", res.Source)
		} else {
			childLogf("ghtoken: per-call gh/git credential UNAVAILABLE — gh and https git children "+
				"will run unauthenticated. %s", res)
		}
	}
	childLastOK = &ok
}

// SetChildHarvestForTest replaces ChildEnv's two sources for the duration of a
// test in ANY package, so a call site's test can prove its child received the
// credential without a real shell or a real secret. It returns the restore
// function; pass it to t.Cleanup. The replacements ignore ChildEnvContext's
// ctx; a test of the deadline itself lives in this package.
func SetChildHarvestForTest(shell, gh func() (string, error)) (restore func()) {
	childMu.Lock()
	prevShell, prevGH, prevOK := childShellHarvest, childGHHarvest, childLastOK
	childShellHarvest = func(context.Context) (string, error) { return shell() }
	childGHHarvest = func(context.Context) (string, error) { return gh() }
	childLastOK = nil
	childMu.Unlock()
	return func() {
		childMu.Lock()
		childShellHarvest, childGHHarvest, childLastOK = prevShell, prevGH, prevOK
		childMu.Unlock()
	}
}

// GitNeedsCredential reports whether a git argument list is a NETWORK operation
// that may authenticate to a remote — the only git children ChildEnv should be
// asked for. Leading global options (-C <dir>, -c <k=v>) are skipped so the
// same predicate serves both `git -C repo fetch` and `cmd.Dir`-style callers.
// Local operations (rebase, rev-parse, update-ref …) never contact a remote,
// and giving them a credential would only widen where the value travels.
func GitNeedsCredential(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-C" || a == "-c":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			switch a {
			case "fetch", "push", "pull", "ls-remote", "clone":
				return true
			}
			return false
		}
	}
	return false
}
