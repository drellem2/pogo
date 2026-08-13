package homevcs

// Is any repository this host pushes agent state to PUBLIC? (mg-015c)
//
// WHY THIS EXISTS. Daniel ruled on 2026-08-13 that pogo-config "absolutely
// should not be public". Nothing on this box checked. The repo whose work tree
// is `~/.pogo` tracks `agents/crew/pa.md` — his email address, his calendar ids
// and a full description of his personal-assistant setup — and if it were
// flipped public by a person, an org policy change or a repo transfer, no
// instrument here would have said so. The only evidence the constraint held was
// that two agents happened to run `gh repo view` by hand while arguing about a
// sentence in a doc. A constraint whose sole evidence is that somebody looked is
// a wish.
//
// WHY IT IS A SET AND NOT A REPO. The rule is "a repository this host pushes
// agent state to must not be public", not "drellem2/pogo-config must not be
// public". That distinction stopped being theoretical while this was being
// written: the mayor found a second live member — `drellem2/pogo-agent-memory`,
// whose work tree is the shared memory corpus under the harness projects dir,
// created 2026-08-12 and holding Daniel's real calendar events in its history.
// A detector that named one repo would have been visibly wrong against a repo
// that already existed. So the subjects are ENUMERATED by the caller from the
// directories pogo already knows it writes agent state into, and a third one
// arrives covered.
//
// WHY IT IS NOT KEYED TO WHAT A REPO TRACKS. The sibling check in audit.go asks
// whether a repository versions files *pogo* writes. That question and this one
// do not overlap: `agents/crew/pa.md` and the memory notes are hand-authored, so
// they are not ManagedPaths, would never appear in Report.Tracked, and are
// precisely the content that makes publication expensive. Gating publication
// behind tracked drift would go silent on the repo that looks healthiest.
//
// WHY EVERY FAILURE IS "UNESTABLISHED" AND NEVER "PRIVATE". Both live subjects
// are private right now, so this check will spend its life green — which makes
// the blind path the one that matters. An unauthenticated or rate-limited `gh`
// cannot see a private repo *or* a public one; GitHub answers a missing token
// with "Could not resolve to a Repository", which reads like absence and is not.
// Folding that into a pass would enrol this in the class of instruments that go
// quiet exactly when they stop being able to see (mg-afd0, mg-4e02) — the class
// it was filed against.
//
// READ-ONLY, with one qualification. Against the host it issues `rev-parse` and
// `remote get-url` and nothing else. It does leave the machine: one `gh repo
// view` per distinct remote, bounded by a timeout, no writes.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/gitceiling"
)

// Subject is one directory this host writes agent state into. Label is how a
// reader recognises it on the checklist; Dir is what gets resolved to a repo.
type Subject struct {
	Label string `json:"label"`
	Dir   string `json:"dir"`
}

// RepoPublication is one distinct repository and its publication state.
type RepoPublication struct {
	// Toplevel is the work tree root; it is the de-duplication key, because
	// several subject dirs routinely live in one repo (every per-agent memory
	// dir under $POGO_HOME resolves to the same work tree).
	Toplevel string `json:"toplevel"`
	// Remote is origin's URL, empty when the repo has none. A repo with no
	// origin pushes nothing anywhere and has no publication state to have —
	// that is a decided answer, not an undecidable one.
	Remote string `json:"remote,omitempty"`
	// Name is Remote spelled the way `gh repo edit` wants it typed
	// (owner/name), when Remote is a github.com URL.
	Name string `json:"name,omitempty"`
	// Holds names the subjects this repo versions, so a reader knows what is
	// at stake without going and looking.
	Holds []string `json:"holds"`
	// Visibility is what the host reported. Empty means nothing established
	// it, which is NOT a synonym for private — read Unknown first.
	Visibility Visibility `json:"visibility,omitempty"`
	// Unknown, when non-empty, says why the publication state could not be
	// established.
	Unknown string `json:"unknown,omitempty"`
}

// Exposure ranks a repo for reporting: 2 = published, 1 = not established or a
// state that is neither public nor private, 0 = private or unpushed. The
// ordering exists so a clean repo can never sort above a public one and get
// truncated out of a capped list.
func (r RepoPublication) Exposure() int {
	if r.Remote == "" {
		return 0
	}
	if r.Unknown != "" {
		return 1
	}
	switch r.Visibility {
	case VisibilityPrivate:
		return 0
	case VisibilityPublic:
		return 2
	default:
		// INTERNAL, or a state GitHub grows after this was written. Not
		// world-readable, not private either — named rather than bucketed.
		return 1
	}
}

// PublicationReport is the whole answer, including the parts it could not
// decide.
type PublicationReport struct {
	// Subjects is every directory that was looked at. It renders even on a
	// clean report, so "nothing published" can be told from "nothing checked".
	Subjects []Subject `json:"subjects"`
	// Repos are the distinct repositories those subjects live in, most
	// exposed first.
	Repos []RepoPublication `json:"repos,omitempty"`
	// Unversioned names subject dirs under no git work tree at all. A decided
	// answer: nothing there is pushed anywhere.
	Unversioned []string `json:"unversioned,omitempty"`
	// Undecided names subject dirs this run could not resolve, with reasons.
	// Never folded into Unversioned — "there is no repo" and "I could not
	// tell" are different facts.
	Undecided []string `json:"undecided,omitempty"`
}

// subjectGroup is one subject directory plus the labels of any subjects nested
// inside it, which it answers for.
type subjectGroup struct {
	Subject
	Encloses []string
}

// enclose folds each subject that lives inside another subject into its
// enclosing one.
//
// WHY THIS IS NOT AN OPTIMISATION. On this fleet's host, twelve of the
// seventeen agent-state directories are per-agent memory dirs under $POGO_HOME
// — and `git -C` in any of them answers "not a git repository", because pogod
// sets GIT_CEILING_DIRECTORIES=$POGO_HOME on itself and everything it spawns
// (internal/gitceiling). That ceiling is a deliberate guard against a git
// command aimed at a nested worktree silently escaping into pogo-config, and it
// is not this check's to defeat. But taking its refusal at face value would have
// this row print "nothing there is pushed anywhere" twelve times about
// directories that sit inside a repo with a remote — false, and twelve lines of
// noise burying the two verdicts that matter. The enclosing subject
// ($POGO_HOME) resolves fine, since the ceiling never excludes the working
// directory itself, so it answers for all of them.
//
// WHAT IT IS NOT. This is de-duplication that happens to route around the
// ceiling; it is not the fix for the ceiling, and reading it as one is how the
// next check re-derives the workaround or fails to notice it needs one
// (mg-490c). gitceiling.ResolveWorkTree is the fix, and AuditPublication calls
// it — so a subject that grouping misses is answered honestly rather than
// reported as versioned by nothing.
func enclose(subjects []Subject) []subjectGroup {
	// Cleaned first: a trailing slash on one of two otherwise identical paths
	// would defeat the containment comparison silently.
	sorted := make([]Subject, 0, len(subjects))
	for _, s := range subjects {
		s.Dir = filepath.Clean(s.Dir)
		sorted = append(sorted, s)
	}
	// Shortest path first, so an enclosing directory is always considered
	// before anything nested in it.
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].Dir) < len(sorted[j].Dir) })

	var groups []subjectGroup
	for _, s := range sorted {
		nested := false
		for i := range groups {
			if under(s.Dir, groups[i].Dir) {
				groups[i].Encloses = append(groups[i].Encloses, s.Label)
				nested = true
				break
			}
		}
		if !nested {
			groups = append(groups, subjectGroup{Subject: s})
		}
	}
	return groups
}

// under reports whether dir sits inside root. Compared as path components, so
// `/a/bc` is not read as living under `/a/b`.
func under(dir, root string) bool {
	if dir == root {
		return true
	}
	return strings.HasPrefix(dir, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

// AuditPublication resolves the publication state of every repository holding
// one of subjects.
//
// The visibility resolver is a parameter rather than a reach for ghVisibility,
// so tests — which build real git repositories in temp dirs — cannot touch the
// network by accident. A nil resolver is honest about itself: a remote nothing
// asked about reports as undecided, never as private.
func AuditPublication(ctx context.Context, subjects []Subject, lookup VisibilityFunc) PublicationReport {
	rep := PublicationReport{Subjects: subjects}

	byTop := map[string]*RepoPublication{}
	var order []string
	for _, g := range enclose(subjects) {
		holds := append([]string{g.Label}, g.Encloses...)
		if _, err := os.Stat(g.Dir); err != nil {
			// A subject dir that is simply absent is not a subject. Say so
			// rather than reporting a repo nobody has.
			rep.Undecided = append(rep.Undecided, g.Label+" ("+g.Dir+") could not be read: "+err.Error())
			continue
		}
		// Via the shared resolver rather than a bare `rev-parse`, so a subject
		// that is NOT folded into an enclosing one still gets an honest answer
		// instead of git's ceiling refusal (mg-490c). enclose covers today's
		// seventeen; this covers the eighteenth, whoever adds it.
		w, err := gitceiling.ResolveWorkTree(ctx, g.Dir)
		if err != nil {
			rep.Undecided = append(rep.Undecided, g.Label+" ("+g.Dir+"): "+err.Error())
			continue
		}
		if !w.Versioned() {
			rep.Unversioned = append(rep.Unversioned, g.Label+" ("+g.Dir+")")
			continue
		}
		top := w.Toplevel
		if r, seen := byTop[top]; seen {
			r.Holds = append(r.Holds, holds...)
			continue
		}
		r := &RepoPublication{Toplevel: top, Holds: holds}
		if url, uerr := gitOut(ctx, top, "remote", "get-url", "origin"); uerr == nil {
			r.Remote = strings.TrimSpace(url)
			if nwo, ok := GitHubRepo(r.Remote); ok {
				r.Name = nwo
			}
		}
		byTop[top] = r
		order = append(order, top)
	}

	// One lookup per distinct repository, after de-duplication — asking twice
	// about one remote is two network calls and two chances to disagree.
	for _, top := range order {
		r := byTop[top]
		if r.Remote == "" {
			continue
		}
		if lookup == nil {
			r.Unknown = "no visibility resolver was configured, so nothing asked whether " + r.Remote + " is published"
			continue
		}
		v, err := lookup(ctx, r.Remote)
		if err != nil {
			r.Unknown = err.Error()
			continue
		}
		r.Visibility = v
	}

	for _, top := range order {
		rep.Repos = append(rep.Repos, *byTop[top])
	}
	// Most exposed first. A capped renderer truncates the tail, so a public
	// repo must never be able to sort behind a private one.
	sort.SliceStable(rep.Repos, func(i, j int) bool {
		if a, b := rep.Repos[i].Exposure(), rep.Repos[j].Exposure(); a != b {
			return a > b
		}
		return rep.Repos[i].Toplevel < rep.Repos[j].Toplevel
	})
	sort.Strings(rep.Unversioned)
	sort.Strings(rep.Undecided)
	return rep
}
