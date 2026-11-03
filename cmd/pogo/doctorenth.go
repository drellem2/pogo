package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/vcs"
)

// enthToolCheckName is the `pogo doctor --check` row for enth (O4, enth-vcs
// slice 9, mg-2e9fc): `enth` on PATH, the way the git row asks for git.
const enthToolCheckName = "enth in PATH"

// enthHelpTimeout bounds the `enth --help` probe; it is a local exec.
const enthHelpTimeout = 5 * time.Second

// enthHelp runs `enth --help` and returns what it printed.
func enthHelp(bin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), enthHelpTimeout)
	defer cancel()
	return exec.CommandContext(ctx, bin, "--help").CombinedOutput()
}

// enthToolLine is the enth row. applies is false when no project is recorded
// as enth, and then there is no row at all: a host of git repos prints
// exactly what it printed before enth existed.
//
// When some project is enth:
//   - fail when `enth` is not on PATH: pogo cannot make a workspace for,
//     judge, or land any work in those repos;
//   - warn when the enth found predates enth #9 (its help has no `status DIR`
//     verb, the same test the enth tests use): it makes checkouts with no
//     .enth/ state, which pogo cannot read;
//   - pass otherwise, naming the binary.
//
// The PATH asked about is this shell's. pogod's own PATH is what spawns use,
// and it can differ (a launchd service has its own); the detail says so
// rather than claiming more than was measured.
func enthToolLine(projs []project.Project, lookPath func(string) (string, error), help func(string) ([]byte, error)) (status, detail string, applies bool) {
	var repos []string
	for _, p := range projs {
		if p.Kind() == vcs.Enth {
			repos = append(repos, p.Path)
		}
	}
	if len(repos) == 0 {
		return "", "", false
	}
	sort.Strings(repos)
	which := fmt.Sprintf("%d enth repo(s): %s", len(repos), strings.Join(repos, ", "))
	bin, err := lookPath("enth")
	if err != nil {
		return "fail", "not found, and " + which + " need it — pogo cannot check out, judge or land work there", true
	}
	out, herr := help(bin)
	if herr != nil {
		return "warn", fmt.Sprintf("%s found, but `enth --help` failed (%v), so whether it is enth #9 or later is unknown; %s", bin, herr, which), true
	}
	if !bytes.Contains(out, []byte("status DIR")) {
		return "warn", fmt.Sprintf("%s predates enth #9 (no `status` verb): its checkouts carry no .enth/ state pogo can read; %s", bin, which), true
	}
	return "pass", fmt.Sprintf("%s (for %s; this shell's PATH — pogod spawns with its own)", bin, which), true
}
