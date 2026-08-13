// Package apimount holds the prefixes under which pogod mounts its
// orchestrated sub-mux, and the one function that performs the mount.
//
// It is a package rather than a few lines in cmd/pogod because the mount is a
// contract with two other readers: the handlers registered on the sub-mux
// (internal/agent, internal/refinery, internal/scheduler) and the clients that
// build request paths for them (internal/client). Only cmd/pogod can import
// itself, so before this package the only way to test a route end-to-end was
// to re-declare the mount inside the test — a copy that can agree with a test
// and disagree with the daemon.
//
// The gap this closes is measured. `/hostload` was registered on the sub-mux
// and mounted under none of these prefixes, so `pogo host load` returned
// `404 Not Found` for two weeks (added mg-1b8c, found mg-c26d). Its three
// tests passed throughout, because all three invoked the handler directly and
// a request a test constructs itself cannot fail on a mount.
package apimount

import "net/http"

// Prefixes are the only paths under which the orchestrated sub-mux is
// reachable, in mount order. A handler registered on that sub-mux at a path
// outside every one of these is unreachable by construction: the request never
// enters the sub-mux at all, it falls through to pogod's "/" home page and
// 404s.
//
// Adding a route is therefore not the same as shipping it. Either put the
// route under a prefix that is already here — which is why every agent route
// begins with "/agents" — or add its prefix here and mount it.
var Prefixes = []string{
	"/agents/",
	"/agents",
	"/refinery/",
	"/scheduler/",
}

// Mount attaches h to root under every prefix in Prefixes.
//
// pogod calls this from both of its wiring branches, the orchestration-guarded
// one and the direct one. It is a single function because it used to be two
// open-coded blocks, and a route that reached neither of them is what made
// this package necessary.
func Mount(root *http.ServeMux, h http.Handler) {
	for _, prefix := range Prefixes {
		root.Handle(prefix, h)
	}
}

// Covers reports whether path is reachable through one of the mounted
// prefixes, following net/http.ServeMux's own matching rules.
//
// It is here so a test can ask the reachability question about a route pattern
// without standing up a server, and get the same answer the daemon would give.
// That "same answer" is not assumed: TestCoversAgreesWithARealMux checks this
// function against a real mounted ServeMux on both sides of every prefix
// boundary, because a predicate that claims reachability without being checked
// against routing is the same shape of mistake as the route this package was
// written for — a registration that looked like reach and was not.
//
// The redirect clause below is what that test found. Covers originally said no
// to "/refinery", and a real mux says yes: registering the subtree pattern
// "/refinery/" makes ServeMux answer "/refinery" with a 301 to it, so a client
// that follows redirects does arrive. Reachable, by a route the predicate did
// not know about.
func Covers(path string) bool {
	for _, prefix := range Prefixes {
		if path == prefix {
			return true
		}
		if n := len(prefix); n > 0 && prefix[n-1] == '/' {
			// Everything beneath a subtree pattern.
			if len(path) > n && path[:n] == prefix {
				return true
			}
			// And the pattern's own path without the trailing slash, which
			// ServeMux redirects into the subtree.
			if path == prefix[:n-1] {
				return true
			}
		}
	}
	return false
}
