package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

func fixedNow(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, "2026-08-13T02:01:29Z")
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	return ts
}

// roundTrip marshals the event the way events.Emit does and decodes the details
// back out. Asserting against the decoded JSON rather than the in-memory map is
// the point: every consumer of this record — `pogo events list --json | jq`,
// the deploy script's sed extractors — reads the serialized form, and a field
// that is right in Go and wrong on the wire is wrong.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestPromptRefreshEvent_AnswersTheFourQuestions is mg-b6bd's acceptance test,
// stated verbatim in the ticket: "The record should answer: what was installed,
// from which revision, for which agents, and when."
func TestPromptRefreshEvent_AnswersTheFourQuestions(t *testing.T) {
	now := fixedNow(t)
	res := &agent.InstallResult{
		Installed: []string{"crew/pm-new.md"},
		Updated:   []string{"crew/doctor.md", "mayor.md"},
		Skipped:   []string{"crew/pm-a.md", "architect.md"},
	}
	ev := promptRefreshEvent(res, "d27ecc1abcdef0123456789abcdef0123456789a", nil, now)

	if ev.EventType != EventPromptRefresh {
		t.Errorf("event_type = %q, want %q", ev.EventType, EventPromptRefresh)
	}
	// WHEN. events.Emit would stamp this itself if it were empty, but then the
	// record's time would be the write time of whichever process happened to
	// flush it, not the moment the install completed.
	if ev.Timestamp != now.UTC().Format(time.RFC3339Nano) {
		t.Errorf("timestamp = %q, want the install's own time %q", ev.Timestamp, now.UTC().Format(time.RFC3339Nano))
	}

	d := roundTrip(t, ev.Details)

	// FROM WHICH REVISION. Full SHA on the record — the short form is for the
	// human log line; a record is for `git merge-base --is-ancestor`.
	if got := d["revision"]; got != "d27ecc1abcdef0123456789abcdef0123456789a" {
		t.Errorf("revision = %v, want the full build stamp", got)
	}
	// FOR WHICH AGENTS / WHAT WAS INSTALLED.
	assertNames(t, d, "installed", []string{"crew/pm-new.md"})
	assertNames(t, d, "updated", []string{"crew/doctor.md", "mayor.md"})
	assertNames(t, d, "skipped", []string{"crew/pm-a.md", "architect.md"})
	assertNames(t, d, "conflicts", nil)

	if got := d["changed"]; got != float64(3) {
		t.Errorf("changed = %v, want 3 (1 installed + 2 updated)", got)
	}
	if got := d["ok"]; got != true {
		t.Errorf("ok = %v, want true", got)
	}
}

// TestPromptRefreshEvent_NoOpBootStillRecords is the case the log deliberately
// stays silent for, and the case the record most needs to carry.
//
// "All nine prompts were already current at revision R as of 02:01Z" is the
// most common answer to "did agent X get the fix", and before mg-b6bd it was
// the answer nothing on this box could give: the log printed nothing, so a
// reader could not distinguish a boot that verified every prompt from a boot
// where act 3 never ran at all. Those are opposite facts and they had the same
// evidence.
func TestPromptRefreshEvent_NoOpBootStillRecords(t *testing.T) {
	res := &agent.InstallResult{
		Skipped: []string{"mayor.md", "crew/doctor.md", "architect.md"},
	}
	// The log's silence, restated here so the two halves stay deliberately
	// different rather than accidentally so.
	if lines := promptRefreshLogLines(res, "abc123"); lines != nil {
		t.Fatalf("precondition: the no-op boot is supposed to be silent in the LOG; got %v", lines)
	}

	ev := promptRefreshEvent(res, "abc123", nil, fixedNow(t))
	d := roundTrip(t, ev.Details)
	if got := d["changed"]; got != float64(0) {
		t.Errorf("changed = %v, want 0", got)
	}
	// The positive observation: these three were checked and were current.
	assertNames(t, d, "skipped", []string{"mayor.md", "crew/doctor.md", "architect.md"})
	if got := d["revision"]; got != "abc123" {
		t.Errorf("revision = %v — a no-op boot's whole value is saying WHICH revision everything was current AT", got)
	}
	if got := d["ok"]; got != true {
		t.Errorf("ok = %v, want true — nothing failed here", got)
	}
}

// TestPromptRefreshEvent_FailureIsOnTheRecord. A refresh that errors leaves
// every prompt at its old content. The condition system already shouts about
// it; the record must carry it too, or a reader reconstructing "was the fleet
// current on the night of the 13th" sees no prompt_refresh event and has to
// guess between "it failed" and "this pogod is too old to emit".
func TestPromptRefreshEvent_FailureIsOnTheRecord(t *testing.T) {
	ev := promptRefreshEvent(nil, "abc123", errors.New("mkdir /ro/agents: read-only file system"), fixedNow(t))
	d := roundTrip(t, ev.Details)
	if got := d["ok"]; got != false {
		t.Errorf("ok = %v, want false", got)
	}
	if got, _ := d["error"].(string); got == "" {
		t.Error("a failed refresh must record WHY; got no error field")
	}
	if got := d["revision"]; got != "abc123" {
		t.Errorf("revision = %v — the failed boot's revision is what says which fix did NOT land", got)
	}
	// No fabricated success. A failed install installed nothing, and reporting
	// an empty "updated" alongside ok=false would invite a consumer that reads
	// only the arrays to score it as a clean no-op boot.
	if _, ok := d["changed"]; ok {
		t.Errorf("a failed refresh must not report a `changed` count at all; got %v", d["changed"])
	}
	if _, ok := d["updated"]; ok {
		t.Error("a failed refresh must not report an `updated` array — an empty one reads as a clean boot")
	}
}

// TestPromptRefreshEvent_UnstampedRevisionIsAValue. A pogod built from a tree
// Go could not read VCS info for has no revision. That is a different fact from
// "this record predates the revision field", and collapsing both to the empty
// string makes an honest unknown indistinguishable from a missing key.
func TestPromptRefreshEvent_UnstampedRevisionIsAValue(t *testing.T) {
	for _, rev := range []string{"", "   "} {
		ev := promptRefreshEvent(&agent.InstallResult{}, rev, nil, fixedNow(t))
		d := roundTrip(t, ev.Details)
		if got := d["revision"]; got != promptRefreshUnknownRevision {
			t.Errorf("revision for %q = %v, want %q", rev, got, promptRefreshUnknownRevision)
		}
	}
	if got := shortRev(""); got != promptRefreshUnknownRevision {
		t.Errorf("shortRev(\"\") = %q, want %q", got, promptRefreshUnknownRevision)
	}
	if got := shortRev("d27ecc1abcdef0123456789abcdef0123456789a"); got != "d27ecc1abcde" {
		t.Errorf("shortRev truncates to 12; got %q", got)
	}
	if got := shortRev("abc123"); got != "abc123" {
		t.Errorf("shortRev must not pad or truncate a short rev; got %q", got)
	}
}

// TestPromptRefreshEvent_EmptyListsSerializeAsArrays pins the wire shape. The
// deploy script reads this record with BRE sed (`"updated":\[...\]`) and a key
// that becomes `null` when the list is empty simply does not match — so the
// no-op boot, the most common one, would read as an unparseable record.
func TestPromptRefreshEvent_EmptyListsSerializeAsArrays(t *testing.T) {
	ev := promptRefreshEvent(&agent.InstallResult{}, "abc123", nil, fixedNow(t))
	b, err := json.Marshal(ev.Details)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"installed":[]`, `"updated":[]`, `"skipped":[]`, `"conflicts":[]`} {
		if !strings.Contains(got, want) {
			t.Errorf("details JSON missing %s (null breaks the deploy's sed reader); got %s", want, got)
		}
	}
}

// TestPromptRefreshEvent_ConflictsAreNotChanged. A conflict is the file the
// installer DECLINED to write. Counting it as changed would report propagation
// that provably did not happen — the exact false-success mg-f86c fixed in the
// log, re-introduced on the record.
func TestPromptRefreshEvent_ConflictsAreNotChanged(t *testing.T) {
	res := &agent.InstallResult{
		Updated: []string{"architect.md"},
		Conflicts: []agent.PromptConflict{
			{Path: "mayor.md", DistPath: "mayor.md.dist"},
		},
	}
	d := roundTrip(t, promptRefreshEvent(res, "abc123", nil, fixedNow(t)).Details)
	if got := d["changed"]; got != float64(1) {
		t.Errorf("changed = %v, want 1 — the conflict was declined, not applied", got)
	}
	assertNames(t, d, "conflicts", []string{"mayor.md"})
	assertNames(t, d, "updated", []string{"architect.md"})
}

func assertNames(t *testing.T, d map[string]any, key string, want []string) {
	t.Helper()
	raw, ok := d[key]
	if !ok {
		t.Fatalf("details has no %q key", key)
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("details[%q] = %#v, want a JSON array", key, raw)
	}
	if len(list) != len(want) {
		t.Fatalf("details[%q] has %d entries, want %d (%v)", key, len(list), len(want), raw)
	}
	for i, w := range want {
		if list[i] != w {
			t.Errorf("details[%q][%d] = %v, want %q", key, i, list[i], w)
		}
	}
}
