package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
