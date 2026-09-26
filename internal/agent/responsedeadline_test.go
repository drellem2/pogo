package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func withSpawnDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	old := SpawnResponseDeadline()
	SetSpawnResponseDeadline(d)
	t.Cleanup(func() { SetSpawnResponseDeadline(old) })
}

func TestResponseDeadlineFor(t *testing.T) {
	cases := []struct{ in, want time.Duration }{
		{0, 0},
		{-time.Second, 0},
		{time.Second, 900 * time.Millisecond},
		{5 * time.Minute, 4*time.Minute + 30*time.Second},
		{time.Hour, time.Hour - 30*time.Second}, // margin capped at 30s
	}
	for _, c := range cases {
		if got := ResponseDeadlineFor(c.in); got != c.want {
			t.Errorf("ResponseDeadlineFor(%s) = %s, want %s", c.in, got, c.want)
		}
		if c.in > 0 && ResponseDeadlineFor(c.in) >= c.in {
			t.Errorf("ResponseDeadlineFor(%s) is not strictly inside the write timeout", c.in)
		}
	}
}

// The fast path must be byte-for-byte what the handler wrote: status, headers
// and body — the client's refusal parsing depends on all three.
func TestSpawnResponseDeadline_FastPathPassesThrough(t *testing.T) {
	withSpawnDeadline(t, 5*time.Second)
	h := WithSpawnResponseDeadline("/test", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo", string(b))
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"reason":"x","message":"refused"}`))
	})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("POST", "/test", strings.NewReader("payload")))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if got := rr.Header().Get("X-Echo"); got != "payload" {
		t.Errorf("handler did not see the body: X-Echo=%q", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rr.Body.String(); got != `{"reason":"x","message":"refused"}` {
		t.Errorf("body = %q", got)
	}
}

// A handler that outlives the deadline gets an explicit 202 still-running, and
// is NOT cancelled: it can still read its body and its context stays live after
// the wrapper has returned (both would be torn down by net/http otherwise).
func TestSpawnResponseDeadline_SlowHandlerAnswers202AndKeepsRunning(t *testing.T) {
	withSpawnDeadline(t, 50*time.Millisecond)
	release := make(chan struct{})
	finished := make(chan string, 1)
	h := WithSpawnResponseDeadline("/test", func(w http.ResponseWriter, r *http.Request) {
		<-release
		b, _ := io.ReadAll(r.Body)
		if err := r.Context().Err(); err != nil {
			finished <- "context cancelled: " + err.Error()
			return
		}
		w.WriteHeader(http.StatusCreated)
		finished <- string(b)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("the-body"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var se StartErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&se); err != nil {
		t.Fatalf("decode 202 body: %v", err)
	}
	if se.Reason != SpawnStillRunningReason {
		t.Errorf("reason = %q, want %q", se.Reason, SpawnStillRunningReason)
	}
	if !strings.Contains(se.Message, "STILL RUNNING") || !strings.Contains(se.Message, "#167") {
		t.Errorf("message does not say still-running / do not redispatch: %q", se.Message)
	}

	close(release)
	select {
	case got := <-finished:
		if got != "the-body" {
			t.Errorf("late handler: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late handler never finished")
	}
}

func TestSpawnResponseDeadline_ZeroIsPassthrough(t *testing.T) {
	withSpawnDeadline(t, 0)
	h := WithSpawnResponseDeadline("/test", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("POST", "/test", nil))
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
}

// A panicking handler must not take pogod down (it runs on a goroutine the
// wrapper launched, where net/http's own recover does not reach) and must
// answer 500, not hang to the deadline.
func TestSpawnResponseDeadline_PanicAnswers500(t *testing.T) {
	withSpawnDeadline(t, 5*time.Second)
	h := WithSpawnResponseDeadline("/test", func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("POST", "/test", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
