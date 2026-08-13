package apimount

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sentinel = "FELL-THROUGH"

// TestCoversAgreesWithARealMux pins Covers to the thing it models.
//
// Covers is a claim about reachability that is not itself routing, which makes
// it the same kind of artifact as the bug it exists to catch: `/hostload` was
// registered, looked registered, and was not reachable. A predicate that
// answers "yes, reachable" while a real mux answers 404 would put a green test
// in front of the identical failure, one layer up.
//
// So it is checked against a real http.ServeMux, mounted by Mount, over paths
// on both sides of every prefix boundary.
func TestCoversAgreesWithARealMux(t *testing.T) {
	root := http.NewServeMux()
	root.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, sentinel, http.StatusNotFound)
	})
	// The mounted handler answers anything that reaches it, so "reached" and
	// "did not reach" are the only two outcomes.
	reached := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	Mount(root, reached)

	paths := []string{
		"/agents",
		"/agents/",
		"/agents/hostload",
		"/agents/roster",
		"/agents/some-name/nudge",
		"/refinery/",
		"/refinery/queue",
		"/scheduler/schedules",
		"/scheduler/schedules/abc/ack",
		// Outside every prefix — what /hostload was.
		"/hostload",
		"/version",
		"/",
		"/health",
		"/workitems",
		// Near-misses that must not be judged reachable by a sloppy prefix test.
		"/agentsfoo",
		"/refinery",
		"/scheduler",
	}

	for _, path := range paths {
		rr := httptest.NewRecorder()
		root.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		actuallyReachable := !strings.Contains(rr.Body.String(), sentinel)

		if got := Covers(path); got != actuallyReachable {
			t.Errorf("Covers(%q) = %v, but a real mounted mux says reachable=%v — "+
				"the predicate disagrees with routing", path, got, actuallyReachable)
		}
	}
}

// TestMountUsesEveryPrefix checks Mount actually binds all of Prefixes, so a
// prefix added to the list cannot sit there unmounted — which is the list-level
// restatement of the bug itself.
func TestMountUsesEveryPrefix(t *testing.T) {
	if len(Prefixes) == 0 {
		t.Fatal("Prefixes is empty: nothing would be reachable")
	}
	root := http.NewServeMux()
	root.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, sentinel, http.StatusNotFound)
	})
	Mount(root, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, prefix := range Prefixes {
		rr := httptest.NewRecorder()
		root.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, prefix, nil))
		if strings.Contains(rr.Body.String(), sentinel) {
			t.Errorf("prefix %q is listed in Prefixes but Mount did not bind it", prefix)
		}
	}
}
