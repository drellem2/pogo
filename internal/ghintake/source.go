package ghintake

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testtmp"
)

// keyGH is the body line that makes a work item a carrier for an issue. It lives
// in the work-item BODY, not the frontmatter — see the filing recipe in
// internal/agent/prompts/mayor.md. internal/workitem parses frontmatter only and
// therefore cannot see it, which is why this package parses bodies itself.
const keyGH = "gh"

// ParseRef splits a `gh:` ref of the form owner/repo#number. Tolerates a full
// GitHub URL, because carriers occasionally get filed with one pasted in.
func ParseRef(ref string) (repo string, number int, err error) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "https://github.com/")
	ref = strings.TrimPrefix(ref, "http://github.com/")
	// .../issues/99 -> .../#99, so the URL form and the marker form share one
	// parse rather than two that can disagree.
	if i := strings.Index(ref, "/issues/"); i >= 0 {
		ref = ref[:i] + "#" + strings.TrimSuffix(ref[i+len("/issues/"):], "/")
	}
	// Strip decoration a human might wrap a ref in: backticks, angle brackets,
	// trailing punctuation from a sentence.
	ref = strings.Trim(ref, "`<>()[] \t.,;")

	repo, numStr, found := strings.Cut(ref, "#")
	if !found {
		return "", 0, fmt.Errorf("gh ref %q has no '#<number>'", ref)
	}
	repo = strings.TrimSpace(repo)
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", 0, fmt.Errorf("gh ref %q: want owner/repo#number", ref)
	}
	number, err = strconv.Atoi(strings.TrimSpace(numStr))
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("gh ref %q: bad issue number", ref)
	}
	return repo, number, nil
}

// ParseBodyRefs returns every normalised issue ref a work-item body structurally
// carries, in first-seen order.
//
// "Structurally" is doing real work here. A line counts only when:
//
//   - it begins with `gh:` (after leading whitespace), so a sentence that
//     happens to contain the word does not qualify;
//   - it is not inside a blockquote — the mayor annotates live carriers with
//     `> **CARRIER NOTE …**` blocks that quote refs and stages in prose, and
//     matching those would let commentary masquerade as state;
//   - it is not inside a fenced code block — ticket bodies routinely paste the
//     filing recipe itself, complete with a literal `gh: <owner>/<repo>#<n>`
//     line, and every such paste would otherwise register as a carrier.
//
// The fence rule is not hypothetical: mg-039b's own body contains the recipe.
// A loose parse would have let this ticket suppress the finding it was filed to
// produce, which is the most embarrassing failure mode available to it.
//
// ALL refs are collected, not just the first. Nothing forbids an item from
// carrying two refs (Daniel filed #99 and #100 to be considered together), and
// under-collecting carriers produces FALSE uncarried findings — the one error
// class a coordinator cannot clear by doing the right thing.
func ParseBodyRefs(body string) []string {
	var out []string
	seen := map[string]bool{}
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" || strings.HasPrefix(trimmed, ">") {
			continue
		}
		key, val, found := strings.Cut(trimmed, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), keyGH) {
			continue
		}
		// One line may name several refs; a carrier for the #99/#100 pair reads
		// naturally as `gh: drellem2/pogo#99, drellem2/pogo#100`.
		for _, field := range strings.FieldsFunc(val, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			ref := NormalizeRef(field)
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// mgListItem is the subset of `mg list --json` (NDJSON) this package reads.
// `mg list` does not emit bodies, which is why establishing the carrier
// population costs one `mg show` per item.
type mgListItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// mgShowItem is the subset of `mg show --json` this package reads.
type mgShowItem struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Body   string `json:"body"`
}

// MGSource reads carrier refs out of a macguffin store by shelling out to the
// `mg` CLI.
type MGSource struct {
	// Root overrides the store location, passed to mg as --root. Empty means
	// "resolve a default" — see resolveRoot, which does NOT resolve to the live
	// store under a test binary.
	Root string
	// Bin is the mg binary. Empty means "mg" on PATH.
	Bin string
	// Workers bounds concurrent `mg show` calls. Zero picks a default from the
	// CPU count.
	//
	// Concurrency is here for a specific reason: unlike ghteardown, which scans
	// only `status=done` carriers, this scan must cover EVERY status — an
	// archived carrier still means the issue was seen, and missing it would
	// produce a false uncarried finding. On this store that is ~2000 items and
	// ~2000 process spawns. Serially that is fourteen seconds on pogod's
	// heartbeat goroutine; fanned out it is about two, and it is all local.
	Workers int
}

// mgStatuses are the statuses the carrier scan covers.
//
// ALL of them, deliberately, and this is the one place where this package's
// coverage decision differs from its sibling's. ghteardown scans `done` only,
// and documents the resulting gap, because each of its carriers costs a network
// round-trip. Here the carrier scan is entirely local, so the cheap and correct
// choice is total coverage: for intake the question is only "does a carrier
// exist at all", and an archived or shelved carrier answers it yes.
var mgStatuses = []string{"available", "claimed", "pending", "done", "shelved", "archived"}

// testRootOnce memoises one temp directory per test binary, so every
// test-binary call resolves to the same empty scratch store.
var (
	testRootOnce sync.Once
	testRootDir  string
)

// resolveRoot returns the store root to hand mg.
//
// Under a test binary with no explicit Root it returns a per-binary temp
// directory — NEVER the live ~/.macguffin. This is a test-safe DEFAULT rather
// than an opt-in helper, which is the whole lesson of mg-da48: the witness store
// shipped a `sandboxWitness(t)` helper, and the only tests that remembered to
// call it were the ones whose subject WAS the witness. An opt-in guard is
// remembered by exactly the tests that least need it.
func (s MGSource) resolveRoot() string {
	if s.Root != "" {
		return s.Root
	}
	if testing.Testing() {
		testRootOnce.Do(func() {
			dir, err := testtmp.Dir("ghintake")
			if err != nil {
				// Deliberately no error return: every fallback must lead
				// somewhere that is not the live store. A temp path we failed to
				// create yields "store unreadable", a loud correct failure;
				// falling back to $HOME/.macguffin would be a silent wrong success.
				dir = filepath.Join(os.TempDir(), "ghintake-test-store-fallback")
			}
			testRootDir = dir
		})
		return testRootDir
	}
	return "" // production: let mg apply its own default ($MG_ROOT, then ~/.macguffin)
}

func (s MGSource) bin() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "mg"
}

func (s MGSource) workers() int {
	if s.Workers > 0 {
		return s.Workers
	}
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

func (s MGSource) run(args ...string) ([]byte, error) {
	if root := s.resolveRoot(); root != "" {
		args = append([]string{"--root", root}, args...)
	}
	cmd := exec.Command(s.bin(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", s.bin(), strings.Join(args, " "), msg)
	}
	return out, nil
}

// Statuses reports which mg statuses this source scans, so a report can state
// its own coverage rather than implying it saw everything.
func (s MGSource) Statuses() []string { return append([]string(nil), mgStatuses...) }

// Carriers returns every `gh:` marker in the store, how many items were
// SUCCESSFULLY read, and the items that could not be read at all.
//
// A store that cannot be LISTED is an error, never an empty slice: zero carriers
// and an unlistable store are different facts, and conflating them is how a
// detector goes quietly blind — this package's own failure mode reproduced inside
// itself.
//
// A single item that cannot be SHOWN is different, and this distinction was
// forced by live state rather than chosen on principle. The first run of this
// scan against the real store died on `mg show mg-3119: ambiguous_id` — two
// archived items in different monthly partitions share that short id, and mg
// refuses to guess. Aborting the scan on it made the detector permanently blind
// on the only store it has to work on, which is a strictly worse failure than the
// one it was built to catch. So a per-item failure is recorded as an ItemError,
// the scan continues, and the gap is reported. See ItemError for why the
// direction of that error makes it safe.
func (s MGSource) Carriers() ([]CarrierRef, int, []ItemError, error) {
	ids, err := s.ids()
	if err != nil {
		return nil, 0, nil, err
	}
	if len(ids) == 0 {
		// Not an error — an empty store is legitimate (a fresh install, a
		// sandbox). It is reported as ItemsScanned=0, which Detect classifies as
		// a blind scan rather than as "every issue is uncarried".
		return nil, 0, nil, nil
	}

	type result struct {
		refs []CarrierRef
		bad  *ItemError
	}
	results := make([]result, len(ids))
	sem := make(chan struct{}, s.workers())
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			refs, bad := s.refsFor(id)
			results[i] = result{refs: refs, bad: bad}
		}(i, id)
	}
	wg.Wait()

	var out []CarrierRef
	var bad []ItemError
	scanned := 0
	for _, r := range results {
		if r.bad != nil {
			bad = append(bad, *r.bad)
			continue
		}
		scanned++
		out = append(out, r.refs...)
	}
	return out, scanned, bad, nil
}

// archivePartition matches the monthly partition in an mg archive path, as it
// appears in an `ambiguous_id` error's list of colliding files.
var archivePartition = regexp.MustCompile(`work/archive/(\d{4}-\d{2})/`)

// refsFor reads one item's body and extracts its carrier refs, resolving the
// ambiguous-short-id collision that a plain `mg show` cannot.
//
// Short ids are 4 hex digits, so the store eventually collides. On this store 24
// items already do: two archived twins in different monthly partitions share an
// id, and `mg show <id>` refuses to guess between them. That refusal is correct —
// but it is also resolvable, because the error names the colliding paths and mg
// accepts the qualified form `<id>@<partition>` the hint suggests.
//
// So a collision is retried per partition rather than recorded as a coverage gap.
// Reporting 24 permanently unreadable items would have been honest and useless:
// every one of them is a potential false uncarried finding, and a gap that can be
// closed should be closed rather than annotated. The ItemError path stays for
// everything this cannot resolve.
//
// Refs from ALL partitions are unioned. Either twin could be the carrier and there
// is no way to tell which without reading both, so both count — over-collecting
// carriers costs nothing here, while under-collecting produces false findings.
func (s MGSource) refsFor(id string) ([]CarrierRef, *ItemError) {
	raw, err := s.run("show", id, "--json")
	if err == nil {
		return parseShow(raw, id)
	}

	partitions := map[string]bool{}
	for _, m := range archivePartition.FindAllStringSubmatch(err.Error(), -1) {
		partitions[m[1]] = true
	}
	if len(partitions) == 0 {
		return nil, &ItemError{ID: id, Detail: firstLine(err.Error())}
	}

	var out []CarrierRef
	var lastErr error
	resolved := 0
	for p := range partitions {
		qualified := id + "@" + p
		raw, qerr := s.run("show", qualified, "--json")
		if qerr != nil {
			lastErr = qerr
			continue
		}
		refs, bad := parseShow(raw, qualified)
		if bad != nil {
			lastErr = fmt.Errorf("%s", bad.Detail)
			continue
		}
		resolved++
		out = append(out, refs...)
	}
	if resolved == 0 {
		detail := "ambiguous short id, and no partition could be read"
		if lastErr != nil {
			detail = "ambiguous short id: " + firstLine(lastErr.Error())
		}
		return nil, &ItemError{ID: id, Detail: detail}
	}
	return out, nil
}

// parseShow turns one `mg show --json` payload into carrier refs. label is the id
// as it was requested, so an ItemError names what actually failed.
func parseShow(raw []byte, label string) ([]CarrierRef, *ItemError) {
	var item mgShowItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, &ItemError{ID: label, Detail: "unparseable: " + err.Error()}
	}
	id := item.ID
	if id == "" {
		id = label
	}
	var refs []CarrierRef
	for _, ref := range ParseBodyRefs(item.Body) {
		refs = append(refs, CarrierRef{ItemID: id, Status: item.Status, Ref: ref})
	}
	return refs, nil
}

// firstLine trims an mg error to something a report can print on one line. mg's
// JSON errors carry a multi-line message plus a hint; the whole blob in a mail
// body would bury the finding it is annotating.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// ids lists every work-item id across every status.
func (s MGSource) ids() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, status := range mgStatuses {
		listed, err := s.run("list", "--status="+status, "--json")
		if err != nil {
			return nil, fmt.Errorf("listing %s work items: %w", status, err)
		}
		for _, line := range strings.Split(string(listed), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var item mgListItem
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				return nil, fmt.Errorf("parsing mg list output: %w", err)
			}
			if item.ID == "" || seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			out = append(out, item.ID)
		}
	}
	return out, nil
}

// ghIssue is the subset of `gh issue list --json` this package reads.
type ghIssue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

// GHListLimit bounds how many open issues are fetched per repo. Generous: a repo
// that legitimately exceeds it would silently lose coverage, which is the
// failure this whole package is about, so the limit is set far above any
// plausible watched repo and the count is reported.
const GHListLimit = 500

// GHOpenIssues lists the OPEN issues on one repo via the `gh` CLI.
//
// PULL REQUESTS ARE EXCLUDED. `gh issue list` already omits them, and that is
// load-bearing rather than incidental: PRs have no carrier convention, so
// including them would make every open PR a permanent uncarried finding.
//
// Any failure returns an error and NO issues, and callers must record it as a
// RepoError rather than dropping it. The tempting bug is the mirror of
// ghteardown's: an empty list from a failed call reads as "no open issues here,
// nothing to reconcile, all clear" — the detector reporting a clean bill of
// health precisely when it can see nothing at all.
func GHOpenIssues(repo string) ([]Issue, error) {
	if repo == "" {
		return nil, fmt.Errorf("empty repo in watch list")
	}
	cmd := exec.Command("gh", "issue", "list", "--repo", repo, "--state", "open",
		"--json", "number,title,url,createdAt,author", "--limit", strconv.Itoa(GHListLimit))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh issue list --repo %s failed: %s", repo, msg)
	}
	var raw []ghIssue
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("gh issue list --repo %s: unparseable output: %w", repo, err)
	}
	issues := make([]Issue, 0, len(raw))
	for _, r := range raw {
		if r.Number <= 0 {
			continue
		}
		issues = append(issues, Issue{
			Repo: repo, Number: r.Number, Title: r.Title, URL: r.URL,
			CreatedAt: r.CreatedAt, Author: r.Author.Login,
		})
	}
	return issues, nil
}

// IssueLister resolves the open issues of one repo. Production binds
// GHOpenIssues; tests substitute a table so every branch — including the failure
// branches, which are the whole point — is reachable without a network.
type IssueLister func(repo string) ([]Issue, error)

// CarrierLister yields the carrier population, the number of items successfully
// examined, and the items that could not be read.
type CarrierLister func() ([]CarrierRef, int, []ItemError, error)

// Collect assembles an Inventory. Partial failures on both sides are recorded and
// the scan continues — one unreachable repo must not blind the others, and one
// unreadable work item must not blind the whole store — but neither is allowed to
// be invisible.
//
// A failure to LIST the store at all is fatal and returns an error: with no
// carrier population there is nothing to reconcile, and a report built on that
// would be fiction.
func Collect(repos []string, list IssueLister, carriers CarrierLister, statuses []string) (Inventory, error) {
	refs, scanned, itemErrs, err := carriers()
	if err != nil {
		return Inventory{}, err
	}
	inv := Inventory{
		Carriers: refs, ItemsScanned: scanned, ItemErrors: itemErrs,
		Statuses: statuses, Repos: append([]string(nil), repos...),
	}
	for _, repo := range repos {
		issues, err := list(repo)
		if err != nil {
			inv.RepoErrors = append(inv.RepoErrors, RepoError{Repo: repo, Detail: err.Error()})
			continue
		}
		inv.Issues = append(inv.Issues, issues...)
	}
	return inv, nil
}

// DefaultRepos is the fallback watch list, and it is deliberately EMPTY: when
// nothing on the host names a repo, this detector watches nothing.
//
// It used to name `drellem2/pogo` and `drellem2/macguffin` — pogo's own
// upstream repos (mg-f04b). On the machine that wrote them that was invisible,
// because the poller state directory always answered first. On every other
// install it was not: a daemon with no [gh_intake] repos and no poller state
// would reconcile a STRANGER'S issue tracker against its local work items, find
// that none of those issues had carriers, and mail its coordinator a wall of
// findings about repos its operator has nothing to do with.
//
// Empty is the honest zero value. A deployment that wants intake coverage names
// its repos in [gh_intake] repos, or runs the issue poller, whose state
// directory DiscoverRepos reads. ResolveRepos reports "no repos configured" so
// the absence is stated rather than looking like a clean scan.
var DefaultRepos []string

// PollerStateDirName is the subdirectory of POGO_HOME in which the issue poller
// (`poll-gh-issues.sh`, a standalone launchd job in drellem2/pogo-reminders)
// keeps one `seen-<owner>-<repo>.json` per repo it polls. Callers join it onto
// config.PogoHome(); this package deliberately does not import config, so that
// Detect and its sources stay testable without a resolved host layout.
const PollerStateDirName = "gh-issues"

// seenFilePattern matches the issue poller's per-repo dedup state,
// `seen-<owner>-<repo>.json`.
var seenFilePattern = regexp.MustCompile(`^seen-([^-]+)-(.+)\.json$`)

// DiscoverRepos derives the watch list from the issue poller's own on-disk state
// directory — one `seen-<owner>-<repo>.json` per repo it polls.
//
// Reading the poller's state rather than duplicating its repo list is the point.
// The two halves of this reconciliation must not drift: if a repo is added to the
// poller's REPOS and mail starts arriving for it, this detector covers it on the
// next sample with no config change and no second edit to forget. A hardcoded
// list here would eventually watch a strict subset of what mails the coordinator,
// and the gap would be exactly the class of issue nobody was looking at.
//
// It reads state, not the sent ledger. That distinction is what keeps the poller
// a dumb transport: nothing here depends on WHICH mails it sent or whether it
// sent any, only on which repos it is configured to watch. So a poller that is
// stopped, wedged, or has never delivered a single mail still yields a correct
// watch list, and this detector keeps working — which matters, because "the
// poller stopped mailing" is itself a way for an issue to go uncarried.
//
// Returns nil when the directory is absent or names no repos; callers fall back
// to DefaultRepos.
func DiscoverRepos(stateDir string) []string {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := seenFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		out = append(out, m[1]+"/"+m[2])
	}
	sort.Strings(out)
	return out
}

// ResolveRepos picks the watch list: an explicit configured list wins, then
// discovery from the poller's state directory, then DefaultRepos. The second
// return value names which source was used, so a report can say where its
// population came from instead of presenting a list as self-evident.
//
// With DefaultRepos empty (its shipped value — see there), the last branch
// returns no repos and says so. An empty watch list and a watch list whose
// every repo is clean both produce zero findings; only the source string tells
// them apart, which is why it is returned rather than inferred.
func ResolveRepos(configured []string, stateDir string) ([]string, string) {
	if len(configured) > 0 {
		return configured, "config"
	}
	if discovered := DiscoverRepos(stateDir); len(discovered) > 0 {
		return discovered, "poller state (" + stateDir + ")"
	}
	if len(DefaultRepos) == 0 {
		return nil, "no repos configured"
	}
	return append([]string(nil), DefaultRepos...), "built-in default"
}
