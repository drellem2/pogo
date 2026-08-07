package client

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/drellem2/pogo/internal/server"
)

func TestCallerCommandStopsAtTheFirstFlag(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"subcommand path", []string{"/usr/local/bin/pogo", "service", "install"}, "pogo service install"},
		{"bare binary", []string{"/opt/pogo/bin/pogo"}, "pogo"},
		{"flag ends it", []string{"pogo", "server", "start", "--repo=/secret/path"}, "pogo server start"},
		{"leading flag", []string{"pogo", "--json", "server", "start"}, "pogo"},
		{"depth capped", []string{"pogo", "a", "b", "c", "d"}, "pogo a b c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := os.Args
			os.Args = tc.argv
			t.Cleanup(func() { os.Args = prev })
			if got := callerCommand(); got != tc.want {
				t.Errorf("callerCommand() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPostAttributedStampsIdentity is the client half of the attribution
// contract: without these headers the daemon sees "Go-http-client/1.1" and can
// record only that some Go program stopped the fleet.
func TestPostAttributedStampsIdentity(t *testing.T) {
	t.Setenv("POGO_AGENT_NAME", "mayor")
	prev := os.Args
	os.Args = []string{"pogo", "service", "install"}
	t.Cleanup(func() { os.Args = prev })

	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer ts.Close()

	resp, err := postAttributed(ts.URL + "/server/stop-orchestration")
	if err != nil {
		t.Fatalf("postAttributed: %v", err)
	}
	resp.Body.Close()

	if got.Get(server.HeaderActorAgent) != "mayor" {
		t.Errorf("%s = %q, want mayor", server.HeaderActorAgent, got.Get(server.HeaderActorAgent))
	}
	if got.Get(server.HeaderActorCmd) != "pogo service install" {
		t.Errorf("%s = %q, want %q", server.HeaderActorCmd,
			got.Get(server.HeaderActorCmd), "pogo service install")
	}
	if got.Get(server.HeaderActorPid) == "" {
		t.Errorf("%s was not sent", server.HeaderActorPid)
	}
}

// TestPostAttributedOmitsAgentWhenUnset: an empty header would read as "an
// agent called and its name is blank". Absence is the honest encoding of "not
// an agent" — a human at a shell, or the installer outside a crew context.
func TestPostAttributedOmitsAgentWhenUnset(t *testing.T) {
	t.Setenv("POGO_AGENT_NAME", "")

	var got http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer ts.Close()

	resp, err := postAttributed(ts.URL + "/server/start-orchestration")
	if err != nil {
		t.Fatalf("postAttributed: %v", err)
	}
	resp.Body.Close()

	if _, ok := got[http.CanonicalHeaderKey(server.HeaderActorAgent)]; ok {
		t.Errorf("%s was sent empty rather than omitted", server.HeaderActorAgent)
	}
}
