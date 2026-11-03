package main

import (
	"testing"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/vcs"
)

// `pogo gc --repo` sweeps an enth project as enth (mg-0ff9c): the record's
// trailing slash must not hide it, and an unrecorded or vcs-less repo is git.
func TestGCKindOf(t *testing.T) {
	projs := []project.Project{
		{Path: "/dev/mg-enth/", VCS: vcs.Enth},
		{Path: "/dev/pogo/"},
	}
	for repo, want := range map[string]vcs.Kind{
		"/dev/mg-enth": vcs.Enth,
		"/dev/pogo":    vcs.Git,
		"/dev/other":   vcs.Git,
	} {
		if got := gcKindOf(projs, repo); got != want {
			t.Errorf("gcKindOf(%s) = %q, want %q", repo, got, want)
		}
	}
}
