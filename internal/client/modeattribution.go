package client

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/server"
)

// postAttributed issues a POST that names its caller.
//
// It exists for the two mode-transition routes and nothing else. Those are the
// only endpoints on the daemon that can disable dispatch fleet-wide, and until
// mg-293c they arrived carrying Go's default identification —
// "Go-http-client/1.1" — which says a Go program called and nothing more. The
// 2026-08-07 investigation narrowed "who stopped orchestration at 02:00Z" to a
// leading candidate over two days and could not demonstrate it; these three
// headers are the difference between that and a name in the record.
//
// Deliberately NOT applied to every client call: the point is attribution on
// the transitions that dark the fleet, and stamping headers across an unrelated
// surface would be a larger change than the instrument warrants.
func postAttributed(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range callerAttribution() {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

// callerAttribution describes this process to the daemon.
func callerAttribution() map[string]string {
	h := map[string]string{
		server.HeaderActorPid: fmt.Sprint(os.Getpid()),
		server.HeaderActorCmd: callerCommand(),
	}
	if name := os.Getenv("POGO_AGENT_NAME"); name != "" {
		h[server.HeaderActorAgent] = name
	}
	return h
}

// callerCommand renders the invoking command as e.g. "pogo service install".
//
// Only leading non-flag arguments are included: they are the subcommand path,
// which is what identifies the caller, and stopping at the first flag keeps
// values (paths, tokens, message bodies) out of a record that is written to a
// shared log and read by other agents.
func callerCommand() string {
	if len(os.Args) == 0 {
		return ""
	}
	parts := []string{filepath.Base(os.Args[0])}
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			break
		}
		parts = append(parts, arg)
		if len(parts) == 4 { // binary + at most three subcommand words
			break
		}
	}
	return strings.Join(parts, " ")
}
