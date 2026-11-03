package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
)

// The GitHub boundary for an enth repo (enth-vcs slice 9, mg-2e9fc;
// docs/design/enth-vcs.md §11). An enth-written repo is mirrored to GitHub by
// enth's export, and pogo never pushes to the mirror, so nothing pogo does to
// an enth repo goes through a pull request. Every git repo is untouched here:
// each function below is reached only when the project record says enth.

// prFlowTemplates are the worker templates whose protocol is a GitHub pull
// request: the gh-issue build track opens a PR and works its review loop
// instead of submitting to the refinery.
var prFlowTemplates = map[string]bool{
	"polecat-build-pr": true,
}

// enthPRFlowRefusal is the spawn refusal for a PR-flow polecat on an enth
// repo, or "" when the spawn is not one. Two shapes are PR flow:
//
//   - a PR-flow template (polecat-build-pr), whose whole deliverable is a PR;
//   - a --branch target other than the repo's trunk. On git that is an
//     integration branch: the merge is one step, and completion is the PR the
//     worker opens from it afterwards (MR PRFlow, mg-7746). An enth repo has
//     no PRs to open, so the worker would land on a ref nothing ever merges
//     onward, then fail the step its prompt calls completion.
//
// A --branch naming the trunk itself is not PR flow and is allowed.
//
// Refused at spawn rather than at the end of the worker's life: the worker
// has not been spent yet, and a retry unchanged is refused identically, so it
// is a 409 like the gates around it.
func enthPRFlowRefusal(e vcs.EnthRepo, tmplName, branch string) string {
	tmpl := strings.TrimSuffix(filepath.Base(strings.TrimSpace(tmplName)), ".md")
	if prFlowTemplates[tmpl] {
		return fmt.Sprintf("template %s is the GitHub pull-request flow, and this repo is an enth repo (%s on %s): "+
			"an enth repo has no PR flow. Its GitHub repo is an export mirror that only enth writes, so a PR "+
			"opened there can never be merged by anyone (docs/design/enth-vcs.md §11). Nothing was dispatched. "+
			"Dispatch it with the ordinary build template (--template=%s), which lands through the refinery",
			tmpl, e.Repo, e.Remote, BuildWorkerTemplate)
	}
	b := strings.TrimSpace(branch)
	if b == "" {
		return ""
	}
	be := enthvcs.Backend{Repo: e}
	trunk := be.ResolveBase(context.Background(), vcs.Repo{}, "")
	if target := be.ResolveBase(context.Background(), vcs.Repo{}, b); target != trunk {
		return fmt.Sprintf("--branch %q targets %s, which is not this enth repo's trunk %s. On a git repo that is "+
			"PR flow: the merge is one step and completion is a pull request from it, which an enth repo does "+
			"not have — its GitHub repo is an export mirror that only enth writes (docs/design/enth-vcs.md "+
			"§11). Nothing was dispatched. Dispatch without --branch (or clear the item's `branch:` field) to "+
			"land on %s",
			b, target, trunk, trunk)
	}
	return ""
}
