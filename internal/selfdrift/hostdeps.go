package selfdrift

import (
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/config"
)

// versionTimeout bounds the /version probe. Short because the target is
// loopback: a live daemon answers immediately, a dead port refuses
// immediately, so this only guards a wedged host — and `pogo service status`
// must stay fast enough that nobody learns to skip it.
const versionTimeout = 3 * time.Second

// HostDeps wires Deps to the real host.
//
// NO GO TOOLCHAIN IS REQUIRED, and that is load-bearing rather than tidy.
// scripts/pogo-self-deploy reads installed revisions with `go version -m`,
// which is fine on the developer box it was written for and useless to a
// consumer who installed a release binary and has no Go toolchain — the exact
// population this check exists for. debug/buildinfo.ReadFile parses the same
// build metadata straight out of the ELF/Mach-O file, from the standard
// library, with nothing installed.
//
// git IS still required for the `main` axis, and its absence is handled the
// same way a missing checkout is: the axis is reported unavailable, with the
// reason, and the other two are still measured. Refusing to answer because one
// of three axes cannot be observed would throw away the finding a consumer is
// most likely to have.
//
// repoOverride, when non-empty, is an explicit --repo and wins over every
// heuristic; the check refuses to guess past it.
func HostDeps(repoOverride string) Deps {
	return Deps{
		RunningRev:   func() (string, string) { return runningRev(config.Load().ServerURL()) },
		InstalledBin: InstalledBin,
		BinaryRev:    BinaryRev,
		ResolveRepo:  func() (string, string) { return ResolveRepo(repoOverride) },
		MainRev:      MainRev,
		RevInRepo:    RevInRepo,
	}
}

// versionBody is the subset of pogod's GET /version we need. Declared here
// rather than shared with cmd/pogod on purpose: this is a WIRE contract with a
// daemon that may be an older build than this CLI, so it must tolerate fields
// appearing and disappearing, which a struct shared with the producer would
// quietly stop doing.
type versionBody struct {
	Revision string `json:"revision"`
}

// runningRev asks the live daemon what it is.
//
// UNREACHABLE and UNSTAMPED are kept apart: a daemon that will not talk owes a
// restart, a daemon that talks but cannot say what it is owes an
// investigation. Reporting both as "" would make them one state, which is the
// mg-de08 defect (absence of evidence read as evidence) in miniature.
func runningRev(baseURL string) (rev, url string) {
	url = strings.TrimSuffix(baseURL, "/") + "/version"
	client := &http.Client{Timeout: versionTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return RevUnreachable, url
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RevUnreachable, url
	}
	var body versionBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return RevUnstamped, url
	}
	if body.Revision == "" {
		return RevUnstamped, url
	}
	return body.Revision, url
}

// InstalledBin resolves where a deployed binary lives, in the order a consumer
// would actually experience it:
//
//	$POGO_GOBIN   an explicit override (the same variable pogo-self-deploy honors)
//	$PATH         what would ACTUALLY run if you typed the name — the answer
//	              that matters, and the one a GOBIN-first lookup gets wrong on
//	              a box with two copies installed
//	$GOBIN        / $GOPATH/bin / ~/go/bin, so a `go install` that landed
//	              outside PATH is still found and reported rather than shrugged
//	              at as "not on disk"
//
// The path is returned whether or not anything is there; BinaryRev decides
// that, and the report always names where it looked.
func InstalledBin(name string) string {
	if gobin := os.Getenv("POGO_GOBIN"); gobin != "" {
		return filepath.Join(gobin, name)
	}
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return filepath.Join(goBinDir(), name)
}

// goBinDir is `go env GOBIN` without the toolchain: GOBIN, else GOPATH/bin,
// else the documented default ~/go/bin.
func goBinDir() string {
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		return gobin
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		// GOPATH is a list; the first entry is where `go install` writes.
		if first := strings.Split(gopath, string(os.PathListSeparator))[0]; first != "" {
			return filepath.Join(first, "bin")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "bin"
	}
	return filepath.Join(home, "go", "bin")
}

// BinaryRev reads the vcs stamp baked into an on-disk binary.
//
// MISSING and UNSTAMPED are different answers to different questions. Missing:
// a build genuinely is owed and a build fixes it. Unstamped: a binary built
// from a tree Go could not read VCS info for — a build is NOT owed and does not
// help, because the rebuild is unstamped too, so the drift would never clear.
// "Never equals main" stops being a safe default the moment it becomes
// permanent.
func BinaryRev(path string) string {
	if path == "" {
		return RevMissing
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return RevMissing
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		// A file that exists but is not a readable Go binary tells us nothing
		// about its provenance — that is UNSTAMPED, not MISSING. Calling it
		// missing would owe a build that cannot fix it.
		return RevUnstamped
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return s.Value
		}
	}
	return RevUnstamped
}

// pogoModule is the module path that identifies a pogo checkout. A directory
// that is merely a git repo is not the pogo source, and comparing a daemon
// against some unrelated repo's HEAD would produce a confidently wrong verdict.
const pogoModule = "module github.com/drellem2/pogo"

// ResolveRepo finds the pogo source checkout for the `main` axis. Precedence:
//
//	override     --repo, which wins outright; a bad one is an error, never a
//	             silent fall-through to a guess
//	$POGO_REPO   the same variable pogo-self-deploy honors
//	cwd          the checkout you are standing in, if it is the pogo module
//
// It deliberately stops there. Having no checkout is the NORMAL consumer state,
// not a failure, and the note says so — the caller reports two axes instead of
// three rather than hunting the disk for something that looks close enough.
func ResolveRepo(override string) (repo, note string) {
	if override != "" {
		if !isGitRepo(override) {
			return "", fmt.Sprintf("--repo %s is not a git repository", override)
		}
		if !isPogoRepo(override) {
			return "", fmt.Sprintf("--repo %s is a git repository but not the pogo module (%s)", override, pogoModule)
		}
		return override, "from --repo"
	}
	if env := os.Getenv("POGO_REPO"); env != "" {
		if isGitRepo(env) && isPogoRepo(env) {
			return env, "from $POGO_REPO"
		}
		return "", fmt.Sprintf("$POGO_REPO=%s is not a pogo checkout", env)
	}
	if cwd, err := os.Getwd(); err == nil {
		if top := gitTopLevel(cwd); top != "" && isPogoRepo(top) {
			return top, "the checkout this command was run from"
		}
	}
	return "", "no pogo source checkout (pass --repo, or set POGO_REPO)"
}

func isGitRepo(path string) bool {
	return gitTopLevel(path) != ""
}

func gitTopLevel(path string) string {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isPogoRepo reads the checkout's go.mod rather than trusting the directory
// name. A path called "pogo" that is not pogo would otherwise anchor the whole
// comparison.
func isPogoRepo(path string) bool {
	b, err := os.ReadFile(filepath.Join(path, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == pogoModule {
			return true
		}
	}
	return false
}

// MainRev resolves ref's HEAD in repo. An unresolvable ref returns "", which
// the report surfaces as an unavailable axis rather than as drift.
func MainRev(repo, ref string) string {
	out, err := exec.Command("git", "-C", repo, "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RevInRepo is the foreign-stamp test: does this checkout actually contain the
// commit the binary claims to have been built from?
func RevInRepo(repo, rev string) bool {
	if repo == "" || isSentinel(rev) {
		return false
	}
	return exec.Command("git", "-C", repo, "cat-file", "-e", rev+"^{commit}").Run() == nil
}
