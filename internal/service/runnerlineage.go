package service

// The deploy runner's lineage (drellem2/pogo#126).
//
// `pogo service install-deploy` copies THIS BUILD's scripts/launchd/pogo-deploy.sh
// to ~/.pogo/bin/pogo-deploy.sh, and the payload audit compared the installed copy
// against the same file. Both assumed the host's runner comes from drellem2/pogo.
// On an org-templated host it does not: the reporting host ran a runner
// byte-identical to payitgov/.pogo's bin/pogo-deploy.sh, which diverged from the
// bundled one by ~680 lines in each direction. The audit called that STALE and
// told the operator to run install-deploy, and install-deploy would have replaced
// the org's runner with drellem2's.
//
// `[lineage] runner_repo / runner_ref / runner_path` let the host say where its
// runner comes from. This file resolves that declaration and answers the one
// question the install guard needs: is the declared upstream someone other than
// drellem2/pogo?

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// runnerLineage is the resolved [lineage] runner_* declaration.
type runnerLineage struct {
	// Declared is true when any runner_* key was set. Undeclared means
	// drellem2/pogo's defaults and changes nothing about any comparison.
	Declared bool
	Repo     string
	Ref      string
	Path     string
	// Origin is Repo's `origin` remote URL, or "" when it could not be read.
	// Read only for a declared lineage.
	Origin string
	// OriginErr says why Origin is empty, for the refusal message.
	OriginErr string
}

// Spec renders the declaration the way every message names it:
// <repo>@<ref>:<path>.
func (l runnerLineage) Spec() string {
	return fmt.Sprintf("%s@%s:%s", l.Repo, l.Ref, l.Path)
}

// Foreign reports whether the declared upstream is NOT drellem2/pogo.
//
// A declared lineage whose origin cannot be read counts as foreign. The
// operator took the trouble to name an upstream, and the cost of the two
// mistakes is not symmetric: wrongly refusing costs one `--force`, wrongly
// overwriting costs the host's runner.
func (l runnerLineage) Foreign() bool {
	if !l.Declared {
		return false
	}
	return !isDrellem2PogoRemote(l.Origin)
}

// OriginNote names the origin, or why it is unknown.
func (l runnerLineage) OriginNote() string {
	if l.Origin != "" {
		return "origin " + l.Origin
	}
	if l.OriginErr != "" {
		return "origin unreadable: " + l.OriginErr
	}
	return "origin unknown"
}

// isDrellem2PogoRemote matches the remote URL forms git accepts for
// drellem2/pogo: https://github.com/drellem2/pogo(.git), git@github.com:drellem2/pogo.git,
// ssh://git@github.com/drellem2/pogo, and a local path ending in drellem2/pogo.
func isDrellem2PogoRemote(url string) bool {
	u := strings.ToLower(strings.TrimSpace(url))
	if u == "" {
		return false
	}
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if !strings.HasSuffix(u, "drellem2/pogo") {
		return false
	}
	rest := strings.TrimSuffix(u, "drellem2/pogo")
	return rest == "" || strings.HasSuffix(rest, "/") || strings.HasSuffix(rest, ":")
}

// runnerGit runs git for the lineage reads. A seam so tests can answer without
// a network or a real remote; the reads themselves are local (no fetch).
var runnerGit = func(repo string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

// resolveRunnerLineage reads [lineage] runner_* from config. An undeclared
// lineage is returned with drellem2/pogo's defaults and no git call is made.
func resolveRunnerLineage() runnerLineage {
	return runnerLineageFrom(config.Load().Lineage)
}

func runnerLineageFrom(c config.LineageConfig) runnerLineage {
	l := runnerLineage{
		Declared: c.RunnerDeclared,
		Repo:     c.RunnerRepo,
		Ref:      c.RunnerRef,
		Path:     c.RunnerPath,
	}
	if l.Repo == "" {
		l.Repo = deploySrcDir()
	}
	if abs, err := filepath.Abs(l.Repo); err == nil {
		l.Repo = abs
	}
	if l.Ref == "" {
		l.Ref = config.DefaultPromptStaleRef
	}
	if l.Path == "" {
		l.Path = config.DefaultLineageRunnerPath
	}
	if !l.Declared {
		return l
	}
	out, err := runnerGit(l.Repo, "remote", "get-url", "origin")
	if err != nil {
		l.OriginErr = err.Error()
	} else {
		l.Origin = strings.TrimSpace(string(out))
	}
	return l
}

// Read returns the runner's bytes at <repo>@<ref>:<path>. Read-only: it never
// fetches, so it judges against the ref as the host last synced it.
func (l runnerLineage) Read() ([]byte, error) {
	out, err := runnerGit(l.Repo, "cat-file", "blob", l.Ref+":"+l.Path)
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob %s:%s in %s: %w", l.Ref, l.Path, l.Repo, err)
	}
	return out, nil
}

// RunnerLineageForeign reports whether [lineage] declares the deploy runner's
// upstream as something other than drellem2/pogo. Read by `pogo config get
// lineage.runner_foreign`, which pogo-deploy.sh's runner self-refresh asks
// before it overwrites the installed runner.
func RunnerLineageForeign() bool { return resolveRunnerLineage().Foreign() }

// RunnerLineageSpec is the declared <repo>@<ref>:<path>, or "" when no
// runner_* key is set.
func RunnerLineageSpec() string {
	l := resolveRunnerLineage()
	if !l.Declared {
		return ""
	}
	return l.Spec()
}
