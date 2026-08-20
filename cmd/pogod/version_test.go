package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/health"
)

// TestVersionHandler verifies GET /version reports the running process's build
// identity as JSON — the axis bin/pogo-self-deploy reads for drift detection
// (mg-6afa / mg-cae1). Build-stamp fields (revision/time) may be empty in a
// `go test` binary, so assert on shape and the always-present start_time.
func TestVersionHandler(t *testing.T) {
	startTime = time.Now()

	req := httptest.NewRequest("GET", "/version", nil)
	rr := httptest.NewRecorder()
	versionHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var info versionInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("response is not valid JSON: %v (body=%s)", err, rr.Body.String())
	}
	if info.StartTime == "" {
		t.Error("start_time should always be populated")
	}
	// Round-trips through RFC3339.
	if _, err := time.Parse(time.RFC3339, info.StartTime); err != nil {
		t.Errorf("start_time %q not RFC3339: %v", info.StartTime, err)
	}
}

// TestVersionAndHealthReportOurOwnPid pins the pid onto both endpoints an agent
// can reach (mg-cbee).
//
// It exists because `pgrep` cannot answer the question. `pgrep`/`pkill` exclude
// the calling process and every one of its ancestors unless passed `-a`
// (`man pgrep`), and pogod is the ancestor of every agent it spawns — so
// `pgrep -x pogod` returns empty at exit 1 from any agent while pogod is
// serving on its port (measured 2026-08-20; see
// docs/investigations/pgrep-cannot-see-pogod-2026-08-20.md).
//
// The assertion is `== os.Getpid()`, not `> 0`. A non-zero pid is satisfied by
// any hardcoded number, and the whole value of this field is that it names the
// process that ANSWERED — a reading that came from somewhere else is the defect
// the ticket was filed about, one layer down.
func TestVersionAndHealthReportOurOwnPid(t *testing.T) {
	startTime = time.Now()

	rr := httptest.NewRecorder()
	versionHandler(rr, httptest.NewRequest("GET", "/version", nil))
	var info versionInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("/version is not valid JSON: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("/version pid = %d, want this process's pid %d", info.PID, os.Getpid())
	}

	rr = httptest.NewRecorder()
	healthFull(rr, httptest.NewRequest("GET", "/health/full", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/health/full status = %d, want 200", rr.Code)
	}
	var full health.FullResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &full); err != nil {
		t.Fatalf("/health/full is not valid JSON: %v (body=%s)", err, rr.Body.String())
	}
	if full.Pogod.PID != os.Getpid() {
		t.Errorf("/health/full pogod.pid = %d, want this process's pid %d", full.Pogod.PID, os.Getpid())
	}
}
