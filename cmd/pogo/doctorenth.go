package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/project"
	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/vcs"
	"github.com/drellem2/pogo/internal/vcs/enthvcs"
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

// enthToolLine is the enth row, and bin is the enth it found ("" when none),
// which the "enth login" row then runs, so both rows judge one binary. applies is false when no project is recorded
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
func enthToolLine(projs []project.Project, lookPath func(string) (string, error), help func(string) ([]byte, error)) (status, detail, bin string, applies bool) {
	var repos []string
	for _, p := range projs {
		if p.Kind() == vcs.Enth {
			repos = append(repos, p.Path)
		}
	}
	if len(repos) == 0 {
		return "", "", "", false
	}
	sort.Strings(repos)
	which := fmt.Sprintf("%d enth repo(s): %s", len(repos), strings.Join(repos, ", "))
	bin, err := lookPath("enth")
	if err != nil {
		return "fail", "not found, and " + which + " need it — pogo cannot check out, judge or land work there", "", true
	}
	out, herr := help(bin)
	if herr != nil {
		return "warn", fmt.Sprintf("%s found, but `enth --help` failed (%v), so whether it is enth #9 or later is unknown; %s", bin, herr, which), bin, true
	}
	if !bytes.Contains(out, []byte("status DIR")) {
		return "warn", fmt.Sprintf("%s predates enth #9 (no `status` verb): its checkouts carry no .enth/ state pogo can read; %s", bin, which), bin, true
	}
	return "pass", fmt.Sprintf("%s (for %s; this shell's PATH — pogod spawns with its own)", bin, which), bin, true
}

// enthLoginCheckName is the `pogo doctor --check` row asking whether each
// enth remote a project names will accept pogo's token (GH #284 item 4,
// mg-d8dc9). "enth in PATH" proves the binary exists; a worker on a repo whose
// remote refuses its token still cannot make or land a Change.
const enthLoginCheckName = "enth login"

// enthWhoamiTimeout bounds one `enth whoami` round trip to an enthd.
const enthWhoamiTimeout = 10 * time.Second

// enthWhoami runs `enth --remote REMOTE whoami`, with ENTH_TOKEN set to token
// when token is non-empty (a project's token file) and the caller's own
// environment otherwise. The token reaches enth only through the child's
// environment; it is never printed.
func enthWhoami(bin, remote, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), enthWhoamiTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--remote", remote, "whoami")
	if token != "" {
		cmd.Env = append(os.Environ(), vcs.EnthTokenEnv+"="+token)
	}
	return cmd.CombinedOutput()
}

// enthLoginRow is one "enth login" row.
type enthLoginRow struct{ status, detail string }

// enthLoginLines asks each distinct (remote, token file) pair among the enth
// projects who its token names. Nothing is returned for a host with no enth
// project, so a git-only host's output is unchanged.
//
//   - pass: enth answered a user ("signed in as NAME");
//   - fail: enth answered an error (unauthorized, not_logged_in, …), or the
//     project's token file cannot be read: pogo's workers would be refused;
//   - warn: no parseable answer (enthd unreachable, a timeout): whether the
//     token is good is unknown, and the row says so rather than guessing.
//
// With no token file the token is this shell's ($ENTH_TOKEN, else the one
// `enth login` saved), which is what pogod hands its workers only when pogod
// runs as the same user with the same environment; the detail says so.
func enthLoginLines(projs []project.Project, bin string, whoami func(bin, remote, token string) ([]byte, error)) []enthLoginRow {
	type key struct{ remote, tokenFile string }
	repos := map[key][]string{}
	var keys []key
	for _, p := range projs {
		if p.Kind() != vcs.Enth || p.Enth == nil {
			continue
		}
		k := key{p.Enth.Remote, p.Enth.TokenFile}
		if _, seen := repos[k]; !seen {
			keys = append(keys, k)
		}
		repos[k] = append(repos[k], p.Enth.Repo)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].remote != keys[j].remote {
			return keys[i].remote < keys[j].remote
		}
		return keys[i].tokenFile < keys[j].tokenFile
	})
	var rows []enthLoginRow
	for _, k := range keys {
		names := repos[k]
		sort.Strings(names)
		which := fmt.Sprintf("for %s", strings.Join(names, ", "))
		source := "this shell's enth login / $ENTH_TOKEN"
		token := ""
		if k.tokenFile != "" {
			source = "token file " + k.tokenFile
			tok, _, err := (vcs.EnthRepo{TokenFile: k.tokenFile}).Token()
			if err != nil {
				rows = append(rows, enthLoginRow{"fail", fmt.Sprintf("%s: %v (%s)", k.remote, err, which)})
				continue
			}
			token = tok
		}
		out, err := whoami(bin, k.remote, token)
		var ans struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if jerr := json.Unmarshal(lastLine(out), &ans); jerr != nil || ans.Type == "" {
			why := "no answer"
			if err != nil {
				why = err.Error()
			}
			rows = append(rows, enthLoginRow{"warn", fmt.Sprintf("%s: could not ask who the token names (%s), so whether workers can land is unknown; %s, %s", k.remote, why, source, which)})
			continue
		}
		switch ans.Type {
		case "user":
			rows = append(rows, enthLoginRow{"pass", fmt.Sprintf("%s: signed in as %s (%s, %s)", k.remote, ans.Name, source, which)})
		case "error":
			rows = append(rows, enthLoginRow{"fail", fmt.Sprintf("%s: %s: %s (%s, %s)", k.remote, ans.Code, ans.Message, source, which)})
		default:
			rows = append(rows, enthLoginRow{"warn", fmt.Sprintf("%s: unexpected answer type %q from enth whoami; %s, %s", k.remote, ans.Type, source, which)})
		}
	}
	return rows
}

// lastLine is the last non-empty line of b: enth prints one JSON object per
// answer, and anything before it is not the answer.
func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return bytes.TrimSpace(lines[len(lines)-1])
}

// enthUnrecordedCheckName is the row for an enth checkout that pogo has on
// record as a git repo (GH #284, mg-d8dc9).
const enthUnrecordedCheckName = "enth repo recorded as git"

// enthUnrecordedLines finds projects recorded as git (no vcs key) whose path
// is an enth checkout root (.enth/checkout). Auto-registration (pogo visit, the
// indexer) records every repo it finds with no vcs key, because the record
// needs an enthd endpoint and repo name a directory scan cannot be trusted to
// supply. Such a record is read as git: a dispatch onto it tries to make a git
// worktree of a directory with no .git and fails. Each row names the set-vcs
// command, filled in from the checkout's recorded origin when it has one.
//
// isCheckout and origin are the enthvcs readers, injected for the test.
func enthUnrecordedLines(projs []project.Project, isCheckout func(string) bool, origin func(string) (remote, repo string)) []string {
	var out []string
	for _, p := range projs {
		if p.Kind() != vcs.Git || !isCheckout(p.Path) {
			continue
		}
		remote, repo := origin(p.Path)
		if remote == "" {
			remote = "URL"
		}
		if repo == "" {
			repo = "NAME"
		}
		out = append(out, fmt.Sprintf("%s is an enth checkout but is recorded as git, so workers cannot be dispatched there; run: pogo project set-vcs %s --vcs=enth --enth-remote=%s --enth-repo=%s",
			p.Path, p.Path, remote, repo))
	}
	sort.Strings(out)
	return out
}

// enthCheckoutOrigin reads the remote and repo an enth checkout was made from;
// empty strings when it cannot tell (a local-store checkout, an unreadable
// state file).
func enthCheckoutOrigin(dir string) (remote, repo string) {
	st, err := enthvcs.ReadState(dir)
	if err != nil {
		return "", ""
	}
	return st.Origin.Remote, st.Origin.Repo
}

// enthGateCheckName is the row asking whether each enth repo has a gate for
// its workers' self_land to run (GH #287). Where enth-land-gate is installed,
// self_land gates the merged tree with the repo's declared gate, or with
// ./build.sh when it declares none. A repo with neither, as the coordinator's
// new-repo recipe once left one, has its first self_land refused.
const enthGateCheckName = "enth land gate"

// enthGateInstalled reports whether self_land would take its gated path on
// this host: the same test the template makes, enth-land-gate on PATH and an
// enth whose land takes --require.
func enthGateInstalled(bin string, lookPath func(string) (string, error), landHelp func(string) ([]byte, error)) bool {
	if bin == "" {
		return false
	}
	if _, err := lookPath("enth-land-gate"); err != nil {
		return false
	}
	out, _ := landHelp(bin)
	return bytes.Contains(out, []byte("--require"))
}

// enthLandHelp runs `enth land --help`. Its exit status is not read: only
// whether the help names --require.
func enthLandHelp(bin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), enthHelpTimeout)
	defer cancel()
	return exec.CommandContext(ctx, bin, "land", "--help").CombinedOutput()
}

// enthGateLines gives one row per enth project when the gate is installed,
// and none otherwise (self_land then lands untested and says so itself). It
// reads the project's checkout, the read checkout pogo keeps fresh on the
// trunk, the way self_land reads it: a gate declared in .pogo/refinery.toml
// (refinery.DeclaredGate, injected as declared), else ./build.sh.
//
//   - pass: a gate is declared, or ./build.sh is there and executable;
//   - warn: no gate declared and no executable ./build.sh, so the gate would
//     run a ./build.sh that cannot start and refuse every land.
//
// It warns rather than fails: a checkout that is behind its trunk can lack a
// gate the trunk already has.
func enthGateLines(projs []project.Project, installed bool, declared func(dir string) (string, bool), stat func(string) (os.FileInfo, error)) []enthLoginRow {
	if !installed {
		return nil
	}
	var rows []enthLoginRow
	for _, p := range projs {
		if p.Kind() != vcs.Enth || p.Enth == nil {
			continue
		}
		dir := strings.TrimRight(p.Path, "/")
		if cmd, ok := declared(dir); ok {
			rows = append(rows, enthLoginRow{"pass", fmt.Sprintf("%s (%s): declared gate %q", dir, p.Enth.Repo, cmd)})
			continue
		}
		bs := dir + "/build.sh"
		fi, err := stat(bs)
		switch {
		case err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0:
			rows = append(rows, enthLoginRow{"pass", fmt.Sprintf("%s (%s): no gate declared; self_land falls back to ./build.sh, which is there", dir, p.Enth.Repo)})
		default:
			why := "no ./build.sh"
			if err == nil {
				why = "./build.sh is not executable"
			}
			rows = append(rows, enthLoginRow{"warn", fmt.Sprintf("%s (%s): no gate declared in .pogo/refinery.toml and %s, so with enth-land-gate installed every self_land is refused. Land a gate with the repo's next Change: a .pogo/refinery.toml with quality_gate = \"<a command that checks the repo>\", or an executable build.sh. If the trunk already has one, this checkout is behind it: enth --remote %s --repo %s checkout update %s",
				dir, p.Enth.Repo, why, p.Enth.Remote, p.Enth.Repo, dir)})
		}
	}
	return rows
}

// enthDeclaredGate is the gate a checkout declares, read as self_land's
// `pogo refinery gate --repo=<dir>` reads it.
func enthDeclaredGate(dir string) (string, bool) { return refinery.DeclaredGate("", dir) }
