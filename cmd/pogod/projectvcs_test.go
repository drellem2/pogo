package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
)

func TestBusyFromMatchesTheRegistryPathForm(t *testing.T) {
	mrs := []refinery.MergeRequest{
		{ID: "mr-1", RepoPath: "/x/repo", Branch: "polecat-a", TargetRef: "main", Status: refinery.StatusQueued},
		{ID: "mr-2", RepoPath: "/x/other", Branch: "polecat-b", TargetRef: "main", Status: refinery.StatusQueued},
	}
	pcs := []agent.PolecatInfo{
		{Name: "cat-a", PID: 42, WorkItemID: "mg-a", SourceRepo: "/x/repo"},
		{Name: "cat-b", PID: 43, SourceRepo: "/x/other"},
	}
	// The registry stores "/x/repo/"; the MR and the polecat say "/x/repo".
	got := busyFrom("/x/repo/", mrs, pcs)
	if len(got) != 2 || !strings.Contains(got[0], "mr-1") || !strings.Contains(got[1], "cat-a") {
		t.Errorf("busyFrom = %v, want mr-1 and cat-a only", got)
	}
	if got := busyFrom("/x/quiet/", mrs, pcs); len(got) != 0 {
		t.Errorf("a quiet repo reads busy: %v", got)
	}
}

func TestServeSetVCSStatusCodes(t *testing.T) {
	orig := project.ProjectFileName
	t.Cleanup(func() { project.ProjectFileName = orig })
	project.ProjectFileName = "projects-pogod-setvcs-test.json"
	project.Init()
	t.Cleanup(project.RemoveSaveFile)
	project.Add(&project.Project{Path: "/x/repo/"})

	post := func(body string, busy project.BusyFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		serveSetVCS(rec, httptest.NewRequest(http.MethodPost, "/projects/vcs", strings.NewReader(body)), busy)
		return rec
	}
	enth := `{"path":"/x/repo","vcs":"enth","enth":{"remote":"https://enth.app","repo":"o/r"}}`
	busy := func(string) []string { return []string{"open MR mr-1"} }
	quiet := func(string) []string { return nil }

	if rec := post(enth, busy); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "mr-1") {
		t.Errorf("busy repo: %d %q, want 409 naming the MR", rec.Code, rec.Body.String())
	}
	if k, _ := project.VCSOf("/x/repo"); k != vcs.Git {
		t.Errorf("refused change took effect: %q", k)
	}
	if rec := post(`{"path":"/x/repo","vcs":"hg"}`, quiet); rec.Code != http.StatusBadRequest {
		t.Errorf("bad kind: %d, want 400", rec.Code)
	}
	if rec := post(`{"path":"/x/nope","vcs":"git"}`, quiet); rec.Code != http.StatusNotFound {
		t.Errorf("unregistered: %d, want 404", rec.Code)
	}
	if rec := post(enth, quiet); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"vcs":"enth"`) {
		t.Errorf("quiet repo: %d %q, want 200 with the enth record", rec.Code, rec.Body.String())
	}
	if k, _ := project.VCSOf("/x/repo"); k != vcs.Enth {
		t.Errorf("accepted change did not take effect: %q", k)
	}
}
