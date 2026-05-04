package scheduler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestHTTPDeleteUsesAgentForDisambiguation exercises the HTTP layer's
// composite-key contract: DELETE /scheduler/schedules/{id} succeeds without
// ?agent= when only one agent owns the id, returns 409 Conflict when
// multiple do, and an explicit ?agent=X always targets a single row.
func TestHTTPDeleteUsesAgentForDisambiguation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	s, err := New(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := fixedTime()
	for _, agent := range []string{"pm-pogo", "pm-onethird"} {
		if _, err := s.Add(Entry{Agent: agent, Cron: "*/10 * * * *", ID: "mail-check"}, now); err != nil {
			t.Fatalf("Add %s: %v", agent, err)
		}
	}
	// Plus one unambiguous id so we can test the happy id-only path too.
	if _, err := s.Add(Entry{Agent: "pm-pogo", Cron: "0 9 * * *", ID: "sweep-morning"}, now); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	// 1) ID-only DELETE on the ambiguous id → 409, both rows still present.
	{
		req := httptest.NewRequest("DELETE", "/scheduler/schedules/mail-check", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Errorf("ambiguous DELETE: want 409, got %d (body=%q)", rec.Code, rec.Body.String())
		}
		if _, ok := s.Get("pm-pogo", "mail-check"); !ok {
			t.Error("pm-pogo row should be intact after rejected DELETE")
		}
		if _, ok := s.Get("pm-onethird", "mail-check"); !ok {
			t.Error("pm-onethird row should be intact after rejected DELETE")
		}
	}

	// 2) DELETE with ?agent=pm-pogo → 204; only pm-pogo's row removed.
	{
		req := httptest.NewRequest("DELETE", "/scheduler/schedules/mail-check?agent=pm-pogo", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("scoped DELETE: want 204, got %d (body=%q)", rec.Code, rec.Body.String())
		}
		if _, ok := s.Get("pm-pogo", "mail-check"); ok {
			t.Error("pm-pogo row should be gone after scoped DELETE")
		}
		if _, ok := s.Get("pm-onethird", "mail-check"); !ok {
			t.Error("pm-onethird row should still exist after scoped DELETE")
		}
	}

	// 3) ID-only DELETE for the now-unambiguous id → 204.
	{
		req := httptest.NewRequest("DELETE", "/scheduler/schedules/mail-check", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("unambiguous DELETE: want 204, got %d (body=%q)", rec.Code, rec.Body.String())
		}
	}

	// 4) ID-only DELETE for an id that was always unambiguous → 204.
	{
		req := httptest.NewRequest("DELETE", "/scheduler/schedules/sweep-morning", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("sweep-morning DELETE: want 204, got %d (body=%q)", rec.Code, rec.Body.String())
		}
	}

	// 5) DELETE for a missing id → 404.
	{
		req := httptest.NewRequest("DELETE", "/scheduler/schedules/nope", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("missing DELETE: want 404, got %d (body=%q)", rec.Code, rec.Body.String())
		}
	}
}

// TestHTTPGetUsesAgentForDisambiguation mirrors the DELETE test for GET so
// `pogo schedule list` and any future single-entry inspection share the same
// contract.
func TestHTTPGetUsesAgentForDisambiguation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	s, err := New(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := fixedTime()
	for _, agent := range []string{"pm-pogo", "pm-onethird"} {
		if _, err := s.Add(Entry{Agent: agent, Cron: "*/10 * * * *", ID: "mail-check"}, now); err != nil {
			t.Fatal(err)
		}
	}

	mux := http.NewServeMux()
	s.RegisterHandlers(mux)

	// Ambiguous GET → 409.
	req := httptest.NewRequest("GET", "/scheduler/schedules/mail-check", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("ambiguous GET: want 409, got %d (body=%q)", rec.Code, rec.Body.String())
	}

	// Scoped GET → 200.
	req = httptest.NewRequest("GET", "/scheduler/schedules/mail-check?agent=pm-pogo", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("scoped GET: want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
}
