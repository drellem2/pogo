package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// mg-4377f: an enth MR has no git branch, so every operator-facing message that
// formatted "branch %s" printed "branch " with nothing after it. Each test below
// pairs the enth case — the item is named, no empty branch — with a git control
// asserted against the OLD format string, so git output is byte-unchanged.

func captureWorkLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })
	return &buf
}

// gitWorkMR is the git control: same shape as the enth fixture, with a branch.
func gitWorkMR() *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID: "mr-g1", RepoPath: "/r", Branch: "polecat-g1", TargetRef: "main", Author: "mg-g1", Item: "mg-g1",
		Status: refinery.StatusMerged, MergedSHA: "abc123def4567890",
	}
}

func TestReopenLineNamesTheEnthItemNotAnEmptyBranch(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-e1", "")
	line := reopenMailLine(failed, reopenResult{Outcome: reopenDeclinedLanded, Landed: *enthLandedMR()})
	if !strings.Contains(line, "mr-e1 (enth item mg-e1) already MERGED onto main as enth abababababab,") || strings.Contains(line, "(branch )") {
		t.Errorf("enth reopen line: %s", line)
	}
	g := guardMergedMR("mr-1", "mg-g1", "polecat-g1", "main", "45b4421abc", false)
	line = reopenMailLine(failed, reopenResult{Outcome: reopenDeclinedLanded, Landed: g})
	if want := fmt.Sprintf("%s (branch %s) already MERGED onto %s%s, ", "mr-1", "polecat-g1", "main", " as 45b4421"); !strings.Contains(line, want) {
		t.Errorf("git reopen line changed: %s\nwant it to contain %q", line, want)
	}
}

// Both the "raising merged-not-closed alert" log line and the alert's Branch:
// line, which share one fixture.
func TestMergedOpenAlertNamesTheEnthItemNotAnEmptyBranch(t *testing.T) {
	buf := captureWorkLog(t)
	got := captureMergedOpenAlerts(t, func(string) (bool, error) { return false, nil })
	reportMergedButOpen(enthLandedMR(), "e1", errors.New("refused"))
	if l := buf.String(); !strings.Contains(l, "work item mg-e1 — enth item mg-e1 landed on main as enth "+enthNew+" ") || strings.Contains(l, "branch  landed") {
		t.Errorf("enth alert log line:\n%s", l)
	}
	buf.Reset()
	reportMergedButOpen(gitWorkMR(), "g1", errors.New("refused"))
	if want := fmt.Sprintf("work item %s — branch %s landed on %s as %s ", "mg-g1", "polecat-g1", "main", "abc123def4567890"); !strings.Contains(buf.String(), want) {
		t.Errorf("git alert log line changed:\n%s\nwant it to contain %q", buf.String(), want)
	}
	if len(*got) != 2 {
		t.Fatalf("alerts = %d", len(*got))
	}
	if _, b := (*got)[0].Message(); !strings.Contains(b, "Branch:     (none: enth item mg-e1)\n") {
		t.Errorf("enth alert body:\n%s", b)
	}
	if _, b := (*got)[1].Message(); !strings.Contains(b, fmt.Sprintf("Branch:     %s\n", "polecat-g1")) {
		t.Errorf("git alert body changed:\n%s", b)
	}
}

// reap.go's "MERGED BUT NOT CLOSED" log line, reached when the close fails for a
// reason other than the item already being done.
func TestMergedButNotClosedLogNamesTheEnthItem(t *testing.T) {
	captureMergedOpenAlerts(t, func(string) (bool, error) { return false, nil })
	reap := func(mr *refinery.MergeRequest) string {
		buf := captureWorkLog(t)
		bare := strings.TrimPrefix(mr.Author, "mg-")
		reg := &fakeReaper{agents: map[string]*agent.Agent{bare: {Name: bare, WorkItemID: mr.Author, Type: agent.TypePolecat}}}
		reapMergedPolecat(reg, mr, func(_, _ string) error { return errors.New("declares-remainder") }, postMergeVerdict{}, nil, nil)
		return buf.String()
	}
	if l := reap(enthLandedMR()); !strings.Contains(l, "is STILL OPEN after enth item mg-e1 merged as enth "+enthNew+":") {
		t.Errorf("enth reap log:\n%s", l)
	}
	if l, want := reap(gitWorkMR()), fmt.Sprintf("is STILL OPEN after branch %s merged as %s:", "polecat-g1", "abc123def4567890"); !strings.Contains(l, want) {
		t.Errorf("git reap log changed:\n%s\nwant it to contain %q", l, want)
	}
}

func TestProgressNotesNameTheEnthItem(t *testing.T) {
	e, g := enthLandedMR(), gitWorkMR()
	if got := submittedNote(e); got != "enth item mg-e1 submitted by mg-e1" {
		t.Errorf("enth submitted note = %q", got)
	}
	if got := mergeLandedNote(e); got != "merge mr-e1 ((none: enth item mg-e1)) landed" {
		t.Errorf("enth landed note = %q", got)
	}
	if got, want := submittedNote(g), fmt.Sprintf("branch %s submitted by %s", g.Branch, g.Author); got != want {
		t.Errorf("git submitted note = %q, want %q", got, want)
	}
	if got, want := mergeLandedNote(g), fmt.Sprintf("merge %s (%s) landed", g.ID, g.Branch); got != want {
		t.Errorf("git landed note = %q, want %q", got, want)
	}
}

func TestBusyFromNamesAnEnthMRsItem(t *testing.T) {
	e, g := *enthLandedMR(), *gitWorkMR()
	e.Status, g.Status = refinery.StatusQueued, refinery.StatusQueued
	got := busyFrom("/r/", []refinery.MergeRequest{e, g}, nil)
	if len(got) != 2 {
		t.Fatalf("busyFrom = %v", got)
	}
	if got[0] != "open MR mr-e1 ((none: enth item mg-e1) -> main, queued)" {
		t.Errorf("enth busy line = %q", got[0])
	}
	if want := fmt.Sprintf("open MR %s (%s -> %s, %s)", g.ID, g.Branch, g.TargetRef, g.Status); got[1] != want {
		t.Errorf("git busy line = %q, want %q", got[1], want)
	}
}

func TestUnroutedFailReasonNamesTheEnthItem(t *testing.T) {
	e := &refinery.MergeRequest{ID: "mr-e1", Author: "mg-be37", Item: "mg-be37", EnthSubmitted: []string{"c1"}}
	route := routeRefineryFailMail(e, noRegistry)
	if !route.Unrouted || !strings.Contains(route.Reason, "for enth item mg-be37: it has no branch") || strings.Contains(route.Reason, `branch ""`) {
		t.Errorf("enth reason = %q (unrouted %v)", route.Reason, route.Unrouted)
	}
	g := &refinery.MergeRequest{ID: "mr-g1", Author: "mg-be37", Branch: "feature-x"}
	route = routeRefineryFailMail(g, noRegistry)
	want := fmt.Sprintf("no agent mailbox could be resolved for branch %q: it carries no %q prefix and pogod's registry has no live agent for author %q",
		"feature-x", "polecat-", "mg-be37")
	if route.Reason != want {
		t.Errorf("git reason = %q, want %q", route.Reason, want)
	}
}

func TestMergedNoticeNamesTheEnthItem(t *testing.T) {
	e := enthLandedMR()
	s, b := mergedNoticeHead(e)
	if s != "MERGED: mr-e1 (item=mg-e1)" || !strings.Contains(b, "\nBranch: (none: enth item mg-e1)\n") {
		t.Errorf("enth notice = %q / %q", s, b)
	}
	e.PostMergeError = "tag failed"
	if s, _ := mergedNoticeHead(e); s != "MERGED but POST-MERGE STEP FAILED: mr-e1 (item=mg-e1)" {
		t.Errorf("enth post-merge subject = %q", s)
	}
	g := gitWorkMR()
	s, b = mergedNoticeHead(g)
	if want := fmt.Sprintf("MERGED: %s (branch=%s)", g.ID, g.Branch); s != want {
		t.Errorf("git subject = %q, want %q", s, want)
	}
	if want := fmt.Sprintf("Merge request %s succeeded.\nBranch: %s\nTarget: %s\nAuthor: %s", g.ID, g.Branch, g.TargetRef, g.Author); b != want {
		t.Errorf("git body = %q, want %q", b, want)
	}
	g.PostMergeError = "tag failed"
	if s, _ := mergedNoticeHead(g); s != fmt.Sprintf("MERGED but POST-MERGE STEP FAILED: %s (branch=%s)", g.ID, g.Branch) {
		t.Errorf("git post-merge subject = %q", s)
	}
}
