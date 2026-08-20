package ineffect

// The real observations. Everything here reads the box; nothing here decides
// anything — the judgement is in assess.go, behind Deps, so it can be tested
// without a daemon, a home directory or a git checkout.
//
// WHY THE CARRIER LIST IS BUILT BY PROBING RATHER THAN DECLARED. Every entry
// below is created only if the thing exists, and carries whatever it says about
// itself, sentinels included. A declared roster would report a carrier this box
// does not have (and go green on it) or miss one it does — and "a carrier
// nobody was reporting" is the whole subject. ~/.pogo/deploy-src is the worked
// example: it is a SECOND checkout that executes repo scripts in place, it was
// four days behind main when this was written, and no existing surface named
// it.
//
// WHY `go list -deps` AND NOT A PREFIX TABLE. "internal/x is in pogod" is a
// claim that rots the day an import moves. `go list -deps ./cmd/pogod` is an
// observation of the build graph as it is now. It costs ~0.15s per binary with
// a warm cache and is memoised per process. When it cannot run, the carrier row
// says the graph could not be read — it does not fall back to a guess dressed
// as a measurement.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/selfdrift"
	"github.com/drellem2/pogo/internal/version"
)

// HostOpts are the knobs the CLI exposes.
type HostOpts struct {
	// Repo is the checkout whose history is surveyed. Required.
	Repo string
	// Ref is the branch PathHistory walks when dating an installed copy.
	Ref string
	// PogoHome is the state root whose installed copies are inspected.
	// Defaults to config.PogoHome().
	PogoHome string
	// DeploySrc is the deploy checkout. Defaults to <PogoHome>/deploy-src.
	DeploySrc string
	// DaemonURL is the running pogod. Defaults to selfdrift's.
	DaemonURL string
}

// DefaultRef is the branch installed copies are dated against.
const DefaultRef = "main"

// SelfDescription is the identity of the binary producing the report, in the
// form `pogo version` already prints: version, revision, branch, and WHERE THE
// REVISION CAME FROM.
//
// It goes through internal/version rather than reading debug.ReadBuildInfo
// here, and that is not a style preference. Go stamps a binary built inside a
// LINKED WORKTREE with the ENCLOSING repository's HEAD — silently, and with a
// well-formed SHA that is not an object in this repo at all (mg-8d0f measured
// exactly that on a pogod built from this tree). A fifth private reader of that
// stamp would print a confidently wrong revision on the line that exists to
// tell a reader whether to trust the rest of the report. internal/version is
// the enumerated reader: it prefers the ldflags stamp, marks the toolchain one
// `source=buildinfo`, and carries that provenance on the human line, which is
// the one that gets quoted into a mail.
func SelfDescription() string {
	info := version.Get()
	if !info.Stamped() {
		return ""
	}
	return info.Describe("pogo")
}

// gitTimeout bounds every git call. A hung git behind a network remote would
// otherwise turn a read-only report into a wedge; nothing here needs the
// network, so a timeout this short is an assertion that it does not.
const gitTimeout = 20 * time.Second

// HostDeps wires the real observations for opts.
func HostDeps(opts HostOpts) Deps {
	if opts.Ref == "" {
		opts.Ref = DefaultRef
	}
	if opts.PogoHome == "" {
		opts.PogoHome = config.PogoHome()
	}
	if opts.DeploySrc == "" {
		opts.DeploySrc = filepath.Join(opts.PogoHome, "deploy-src")
	}
	repo := opts.Repo

	deps := Deps{
		SurveyRef:       opts.Ref,
		Reporter:        SelfDescription(),
		NormalizePrompt: agent.StripPromptStamp,
		RevCarriers:     hostRevCarriers(opts),
		GoCarriers:      goCarriers(repo),
	}

	deps.Resolve = func(rev string) (string, string, string, error) {
		full, err := gitLine(repo, "rev-parse", rev+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("%q is not a commit in %s: %w", rev, repo, err)
		}
		subject, _ := gitLine(repo, "log", "-1", "--format=%s", full)
		when, _ := gitLine(repo, "log", "-1", "--format=%cI", full)
		return full, subject, when, nil
	}

	deps.ChangedPaths = func(rev string) ([]string, error) {
		out, err := gitOut(repo, "show", "--no-renames", "--pretty=format:", "--name-only", rev)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var ps []string
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			ps = append(ps, l)
		}
		return ps, nil
	}

	deps.IsAncestor = func(anc, desc string) (bool, error) {
		cmd := gitCmd(repo, "merge-base", "--is-ancestor", anc, desc)
		err := cmd.Run()
		if err == nil {
			return true, nil
		}
		var ee *exec.ExitError
		// git exits 1 for "not an ancestor" and >1 for "I could not tell" —
		// a bad object, an unreadable repo. Collapsing those would report a
		// carrier as measured-and-behind when it was never measured.
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}

	deps.BlobAt = func(rev, p string) ([]byte, error) {
		out, err := gitOut(repo, "show", rev+":"+p)
		if err != nil {
			return nil, ErrNoBlob()
		}
		return out, nil
	}

	deps.PathHistory = func(p string, max int) ([]string, error) {
		out, err := gitOut(repo, "log", fmt.Sprintf("-%d", max), "--format=%H", opts.Ref, "--", p)
		if err != nil {
			return nil, err
		}
		var cs []string
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				cs = append(cs, l)
			}
		}
		return cs, nil
	}

	deps.FileCarriers = func(repoPath string) []FileCarrier {
		return hostFileCarriers(opts, repoPath)
	}

	return deps
}

// hostRevCarriers probes every revision-reporting carrier this box has.
func hostRevCarriers(opts HostOpts) []RevCarrier {
	var out []RevCarrier

	daemonURL := opts.DaemonURL
	if daemonURL == "" {
		daemonURL = config.Load().ServerURL()
	}
	rev, url := selfdrift.RunningRev(daemonURL)
	out = append(out, RevCarrier{
		Name: "running pogod", At: url, Revision: rev, Binary: "pogod",
		Remedy: "restart pogod onto a current build (`pogo service reconcile`, or wait for the nightly redeploy)",
	})

	for _, name := range installedCmds(opts.Repo) {
		p := selfdrift.InstalledBin(name)
		r := selfdrift.BinaryRev(p)
		if r == selfdrift.RevMissing {
			// Not installed is not a stale carrier — it is not a carrier.
			// Reporting it would put an UNKNOWN row against a binary nobody
			// on this box runs.
			continue
		}
		out = append(out, RevCarrier{
			Name: "installed " + name, At: p, Revision: r, Binary: name,
			Remedy: fmt.Sprintf("reinstall the binary (`go install ./cmd/%s` from a current checkout)", name),
		})
	}

	for _, co := range []struct{ name, dir, remedy string }{
		{"this checkout", opts.Repo, ""},
		{"deploy checkout", opts.DeploySrc, "`git -C " + opts.DeploySrc + " fetch && git -C " + opts.DeploySrc + " reset --hard origin/" + opts.Ref + "`, or let the nightly redeploy do it"},
	} {
		if co.dir == "" {
			continue
		}
		head, err := gitLine(co.dir, "rev-parse", "HEAD")
		if err != nil {
			continue
		}
		out = append(out, RevCarrier{Name: co.name, At: co.dir, Revision: head, Checkout: true, Remedy: co.remedy})
	}
	return out
}

// installedCmds lists the programs cmd/ ships, from the repo rather than from a
// list in this file — a hardcoded roster would silently omit a program added
// after it was written, and that omission renders as "no carrier".
func installedCmds(repo string) []string {
	ents, err := os.ReadDir(filepath.Join(repo, "cmd"))
	if err != nil {
		return selfdrift.DeployedCmds
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// goCarriers memoises one `go list -deps` per program.
func goCarriers(repo string) func(string) ([]string, error) {
	graphs := map[string]map[string]bool{}
	var loadErr error
	loaded := false
	load := func() {
		if loaded {
			return
		}
		loaded = true
		mod, err := gitFreeOut(repo, "go", "list", "-m")
		if err != nil {
			loadErr = fmt.Errorf("go list -m: %w", err)
			return
		}
		module := strings.TrimSpace(string(mod))
		for _, name := range installedCmds(repo) {
			out, err := gitFreeOut(repo, "go", "list", "-deps", "./cmd/"+name)
			if err != nil {
				continue
			}
			set := map[string]bool{"cmd/" + name: true}
			for _, l := range strings.Split(string(out), "\n") {
				l = strings.TrimSpace(l)
				if l == "" || !strings.HasPrefix(l, module+"/") {
					continue
				}
				set[strings.TrimPrefix(l, module+"/")] = true
			}
			graphs[name] = set
		}
		if len(graphs) == 0 && loadErr == nil {
			loadErr = errors.New("no program's dependency graph could be listed")
		}
	}
	return func(pkgDir string) ([]string, error) {
		load()
		if loadErr != nil {
			return nil, loadErr
		}
		var names []string
		for name, set := range graphs {
			// An empty pkgDir is go.mod / go.sum: every program is rebuilt
			// from them, so every program carries the change.
			if pkgDir == "" || set[pkgDir] {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		return names, nil
	}
}

// hostFileCarriers finds the installed COPIES of one repo path. Two lookups,
// both by observation:
//
//	prompts   <PogoHome>/agents/<path under the corpus>
//	assets    <PogoHome>/bin/<basename>
//
// The second is deliberately basename-keyed and table-free: whether a given
// script has an installed copy is a fact about what an installer did, and a
// list of which scripts "should" be installed is the kind of claim that goes
// stale without saying so.
func hostFileCarriers(opts HostOpts, repoPath string) []FileCarrier {
	var candidates []struct{ name, path, remedy string }

	if ip := InstalledPromptPath(filepath.Join(opts.PogoHome, "agents"), repoPath); ip != "" {
		candidates = append(candidates, struct{ name, path, remedy string }{
			"installed prompt", ip, "`pogo agent prompt install` from a current checkout",
		})
	}
	if Classify(repoPath) == ClassAsset {
		candidates = append(candidates, struct{ name, path, remedy string }{
			"installed copy", filepath.Join(opts.PogoHome, "bin", basename(repoPath)),
			"re-run the installer that owns this copy (`pogo service install-deploy`, `install-recovery`, `install-reclaim`)",
		})
	}

	var out []FileCarrier
	for _, c := range candidates {
		data, err := os.ReadFile(c.path)
		if errors.Is(err, os.ErrNotExist) {
			// No copy is not a carrier, and it is not an unknown either. The
			// asset's checkout rows still answer for it.
			continue
		}
		out = append(out, FileCarrier{Name: c.name, Path: c.path, Data: data, Err: err, Remedy: c.remedy})
	}
	return out
}

func gitCmd(repo string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return cmd
}

func gitOut(repo string, args ...string) ([]byte, error) {
	cmd := gitCmd(repo, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), nil
	case <-time.After(gitTimeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("git %s: timed out after %s", strings.Join(args, " "), gitTimeout)
	}
}

func gitLine(repo string, args ...string) (string, error) {
	out, err := gitOut(repo, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitFreeOut runs a non-git command in repo. Same timeout, same reason.
func gitFreeOut(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), nil
	case <-time.After(gitTimeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("%s %s: timed out after %s", name, strings.Join(args, " "), gitTimeout)
	}
}
