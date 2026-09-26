package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// A spawn that outlives pogod's http.Server WriteTimeout used to reach its
// caller as a bare EOF (mg-c252, split from drellem2/pogo#175). The handler
// kept running and the spawn usually SUCCEEDED, but net/http had already torn
// the connection down, so `pogo agent spawn-polecat` printed a transport error
// and exited 1 — which every caller reads as "failed", and the natural answer
// to "failed" is to dispatch again. A second dispatch of a live spawn is
// drellem2/pogo#167, the path that destroyed a running polecat's worktree.
//
// The WriteTimeout comment in cmd/pogod said it "must cover the slowest
// handler", and nothing enforced that. This file makes the spawn routes
// enforce it themselves: they answer BEFORE the server's write deadline, and
// when the work is not finished by then the answer is an explicit, distinct
// 202 "still running" — never a timeout the client cannot tell from a crash.
//
// The deadline is derived from the server's WriteTimeout (see
// ResponseDeadlineFor and SetSpawnResponseDeadline), not restated beside it,
// so raising or lowering the server timeout moves this with it.

// SpawnStillRunningReason is the StartErrorResponse.Reason carried by the 202 a
// spawn route returns when its work outlives the response deadline. The client
// keys on it (ErrSpawnStillRunning).
const SpawnStillRunningReason = "spawn-still-running"

// spawnResponseDeadline is how long a spawn route waits for its handler before
// answering 202 still-running. Zero disables the wrapper (the handler writes
// straight to the connection, as before). Stored as nanoseconds so a test can
// set it while requests are in flight.
var spawnResponseDeadline atomic.Int64

// SetSpawnResponseDeadline sets the spawn routes' response deadline. pogod calls
// it with ResponseDeadlineFor(httpServer.WriteTimeout).
func SetSpawnResponseDeadline(d time.Duration) { spawnResponseDeadline.Store(int64(d)) }

// SpawnResponseDeadline reports the current spawn response deadline.
func SpawnResponseDeadline() time.Duration { return time.Duration(spawnResponseDeadline.Load()) }

// ResponseDeadlineFor returns the latest point, measured from the start of a
// handler, at which a response can still be written safely under an
// http.Server with the given WriteTimeout. The margin (a tenth, capped at 30s)
// covers the time the server spends reading the request before the handler
// starts — WriteTimeout runs from the end of the header read — and the time it
// takes to write the answer itself. Zero or negative means no server deadline,
// and so no wrapper.
func ResponseDeadlineFor(writeTimeout time.Duration) time.Duration {
	if writeTimeout <= 0 {
		return 0
	}
	margin := writeTimeout / 10
	if margin > 30*time.Second {
		margin = 30 * time.Second
	}
	return writeTimeout - margin
}

// WithSpawnResponseDeadline wraps a spawn handler so it always answers inside
// the server's write deadline.
//
// The handler runs on its own goroutine against a buffered ResponseWriter. If
// it finishes in time, its buffered response is copied out unchanged — the
// fast path is byte-for-byte what the handler wrote. If it does not, the
// client gets a 202 with Reason SpawnStillRunningReason and the handler is
// left to finish: the spawn is NOT cancelled, because cancelling a spawn
// halfway through a worktree add is its own mess, and the whole point is that
// the work is legitimately still going. Its eventual outcome is logged, and the
// handler's own agent_spawned / agent_spawn_failed event records it durably.
//
// The request body is read before the handler is started, and the handler's
// context is detached from the connection's, because both would otherwise be
// torn down when this wrapper returns while the handler is still using them.
func WithSpawnResponseDeadline(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		deadline := SpawnResponseDeadline()
		if deadline <= 0 {
			h(w, req)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("bad request: reading body: %v", err), http.StatusBadRequest)
			return
		}
		inner := req.Clone(context.WithoutCancel(req.Context()))
		inner.Body = io.NopCloser(bytes.NewReader(body))

		rec := &bufferedResponse{header: make(http.Header)}
		done := make(chan struct{})
		GoSafe("agent.spawnResponseDeadline", func() {
			defer close(done)
			if !Safely("agent.spawnHandler", func() { h(rec, inner) }) && rec.status == 0 {
				rec.status = http.StatusInternalServerError
				rec.body.WriteString("spawn handler panicked; see pogod log\n")
			}
		})

		timer := time.NewTimer(deadline)
		defer timer.Stop()
		select {
		case <-done:
			rec.copyTo(w)
		case <-timer.C:
			started := time.Now().Add(-deadline)
			log.Printf("%s: handler still running after %s; answered 202 %s — the spawn continues", route, deadline, SpawnStillRunningReason)
			GoSafe("agent.spawnResponseDeadline.late", func() {
				<-done
				log.Printf("%s: late handler finished after %s: HTTP %d %s", route,
					time.Since(started).Round(time.Second), rec.statusOrOK(), strings.TrimSpace(rec.body.String()))
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(StartErrorResponse{
				Reason: SpawnStillRunningReason,
				Message: fmt.Sprintf("pogod accepted the spawn and it is STILL RUNNING after %s — this is not a failure, and dispatching again "+
					"would put a second worker on the same name (drellem2/pogo#167). Check `pogo agent list` for the agent, "+
					"and pogod's log / event log (agent_spawned or agent_spawn_failed) for the outcome.", deadline),
			})
		}
	}
}

// bufferedResponse is the ResponseWriter the wrapped handler writes to. It is
// written only by the handler goroutine, and read by the wrapper only after
// that goroutine has closed done.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *bufferedResponse) statusOrOK() int {
	if b.status == 0 {
		return http.StatusOK
	}
	return b.status
}

func (b *bufferedResponse) copyTo(w http.ResponseWriter) {
	for k, v := range b.header {
		w.Header()[k] = v
	}
	w.WriteHeader(b.statusOrOK())
	w.Write(b.body.Bytes())
}
