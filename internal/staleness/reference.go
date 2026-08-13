package staleness

// The REFERENCE's own staleness (mg-afd0).
//
// The prompt witness compares the installed corpus against a git ref in a
// reference repo, and the default reference repo is ~/.pogo/deploy-src — a
// mirror the nightly fetches AT DEPLOY TIME and never after. Its `origin/main`
// is therefore frozen at the deployed revision, and the report labelled it
// `origin/main` with nothing to say so.
//
// WHY THAT IS THE WHOLE BUG AND NOT A NUANCE. The command's own header is "the
// fleet is running something older than what shipped, and nothing said so." A
// reference pinned at the last deploy structurally cannot witness that for
// anything that shipped SINCE the last deploy — which is the only window in
// which the fleet is running something older. Measured on 2026-08-13: the
// reference stood at 082ec38b (fetched 03:00), 17 commits behind origin/main,
// five of them touching the prompt corpus, and the witness printed
//
//	reference:  /Users/daniel/.pogo/deploy-src @ origin/main = 082ec38b0159
//	ok: all 9 shipped prompt(s) match the reference.
//
// while a shipped fix to crew/doctor.md (d27ecc1) sat unread on every agent.
//
// SO THIS FILE ADDS THE TWO THINGS THE ROW WAS MISSING:
//
//   - WHEN the reference last fetched, read from FETCH_HEAD's mtime, always,
//     with no network. That is the limit the row was asserting without carrying.
//   - WHETHER the live remote has moved past it, read with `git ls-remote`,
//     which queries the remote and writes nothing. When the reference already
//     holds the remote head's objects the gap is quantified — N commits since,
//     M of them touching the corpus — and that second half is the sentence
//     nothing emitted before.
//
// NOTHING HERE FETCHES BY DEFAULT, which preserves the property the command
// already claimed: a detector that mutates the tree it is judging has made
// itself a participant. `ls-remote` is a query, not a mutation. The opt-in
// `--fetch` path is deliberately separate and deliberately loud.
//
// AND THE REMEDY HAS THE DEFECT IT REMEDIES, so it is guarded. A plain
// `git fetch` rewrites FETCH_HEAD — the exact file the fetch-age above is read
// from — so running the fix would destroy the evidence the fault was found
// with, and every subsequent run would report the reference as freshly fetched
// when what it means is "this detector fetched it". FetchReference passes
// `--no-write-fetch-head` for that reason and falls back only on a git too old
// to know the flag, in which case the caller is told the timestamp moved.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultRemoteTimeout bounds the remote query. The number is not calibrated to
// how long an ls-remote takes (well under a second here); it is calibrated to
// how long a reader will accept a check command hanging on a network that has
// gone away. A half-open connection is the failure mode — it does not refuse,
// it waits — which is why a bound exists at all.
const DefaultRemoteTimeout = 15 * time.Second

// aheadCorpusCap limits the enumeration of corpus-touching commits. The COUNT
// is always exact; only the list is clipped, and the clipping is reported.
const aheadCorpusCap = 10

// FetchState is when the reference repo last fetched, and from what evidence.
//
// Source is part of the answer rather than an implementation detail: the whole
// defect here was a row that stated a fact without stating what limited it, and
// "fetched 03:00" is another such fact unless a reader can see which file
// dated it.
type FetchState struct {
	At         string `json:"at,omitempty"`
	AgeSeconds int    `json:"age_seconds,omitempty"`
	Source     string `json:"source,omitempty"`
	// Why is set when At is empty — the reason the age is unknown. Unknown is
	// reported as unknown; a zero age would read as "just fetched".
	Why string `json:"why,omitempty"`
}

// Known reports whether a fetch time was established.
func (f FetchState) Known() bool { return f.At != "" }

// ReadFetchState dates the reference repo's last fetch from FETCH_HEAD.
//
// FETCH_HEAD rather than the remote-tracking ref's own mtime, and the
// difference decides the answer: git rewrites a ref only when it MOVES, so a
// ref's mtime dates the last time the branch changed, not the last time anyone
// looked. A mirror that fetched an hour ago and found nothing new would date
// itself days old by ref mtime and read as a stale reference when it is
// current. FETCH_HEAD is written on every fetch either way.
func ReadFetchState(ctx context.Context, repo string, now time.Time) FetchState {
	out, err := gitOut(ctx, repo, "rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		return FetchState{Why: fmt.Sprintf("could not locate FETCH_HEAD in %s: %v", repo, err)}
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return FetchState{Why: fmt.Sprintf("git named no FETCH_HEAD path for %s", repo)}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo, p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return FetchState{Why: fmt.Sprintf("no %s — this repo has never fetched, or the record was removed", p)}
	}
	at := fi.ModTime()
	age := int(now.Sub(at).Seconds())
	if age < 0 {
		age = 0
	}
	return FetchState{At: at.Format(time.RFC3339), AgeSeconds: age, Source: p}
}

// RemoteTarget is the remote and branch a ref is tracking, resolved from the
// repo's own configuration rather than by splitting the string. "origin/main"
// splits obviously; "origin/release/2026-08" does not, and a local branch does
// not split at all while still having a remote to be behind.
type RemoteTarget struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	Branch string `json:"branch"`
}

// ResolveRemoteTarget maps a ref to the remote head it should be compared with.
//
// Two shapes are recognised and no others:
//
//	refs/remotes/<remote>/<branch>   the mirror case — deploy-src's origin/main
//	refs/heads/<branch>              a working checkout, via branch.<b>.remote
//
// Anything else (a tag, a bare sha, a detached HEAD) resolves to no target, and
// the caller reports the comparison as NOT ARMED rather than guessing. A guess
// here would produce a confident row about the wrong branch, which is the class
// of defect this whole file exists to remove.
func ResolveRemoteTarget(ctx context.Context, repo, ref string) (RemoteTarget, error) {
	// No --end-of-options here, unlike the rev-parse in LoadShippedCorpus:
	// `--symbolic-full-name` refuses to sit after it and rev-parse ECHOES the
	// unconsumed flag as an output line, so the guard would silently become the
	// answer. A ref that looks like an option instead fails as an unknown
	// option, which is reported and disarms the comparison — the safe end.
	full, err := gitOut(ctx, repo, "rev-parse", "--symbolic-full-name", ref)
	if err != nil {
		return RemoteTarget{}, fmt.Errorf("resolving %s to a full ref name: %w", ref, err)
	}
	name := ""
	for _, line := range strings.Split(strings.TrimSpace(string(full)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			name = line
		}
	}

	var t RemoteTarget
	switch {
	case strings.HasPrefix(name, "refs/remotes/"):
		rest := strings.TrimPrefix(name, "refs/remotes/")
		slash := strings.Index(rest, "/")
		if slash <= 0 || slash == len(rest)-1 {
			return RemoteTarget{}, fmt.Errorf("%s is not <remote>/<branch>", name)
		}
		t = RemoteTarget{Name: rest[:slash], Branch: rest[slash+1:]}
	case strings.HasPrefix(name, "refs/heads/"):
		branch := strings.TrimPrefix(name, "refs/heads/")
		remote, err := gitOut(ctx, repo, "config", "--get", "branch."+branch+".remote")
		if err != nil {
			return RemoteTarget{}, fmt.Errorf("%s tracks no remote (no branch.%s.remote)", branch, branch)
		}
		upstream := branch
		if merge, err := gitOut(ctx, repo, "config", "--get", "branch."+branch+".merge"); err == nil {
			upstream = strings.TrimPrefix(strings.TrimSpace(string(merge)), "refs/heads/")
		}
		t = RemoteTarget{Name: strings.TrimSpace(string(remote)), Branch: upstream}
	default:
		return RemoteTarget{}, fmt.Errorf("%s is neither a remote-tracking ref nor a branch with an upstream", name)
	}

	url, err := gitOut(ctx, repo, "config", "--get", "remote."+t.Name+".url")
	if err != nil {
		return RemoteTarget{}, fmt.Errorf("no remote named %q is configured in %s", t.Name, repo)
	}
	t.URL = strings.TrimSpace(string(url))
	return t, nil
}

// LiveRemoteHead asks the remote what the branch points at NOW. It reads the
// network and writes nothing — not the object store, not a ref, not
// FETCH_HEAD — which is what lets the default path answer "has anything shipped
// since?" without becoming a participant in the state it judges.
func LiveRemoteHead(ctx context.Context, repo string, t RemoteTarget, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultRemoteTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := gitNetOut(bounded, repo, "ls-remote", "--heads", "--", t.Name, "refs/heads/"+t.Branch)
	if err != nil {
		if bounded.Err() != nil && ctx.Err() == nil {
			return "", fmt.Errorf("querying %s gave no answer within %s", t.URL, timeout)
		}
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "refs/heads/"+t.Branch {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s does not have a branch %s", t.URL, t.Branch)
}

// AheadCommit is one commit that shipped after the reference and touched the
// prompt corpus. Subject is carried because a count answers "how far behind"
// and only a subject answers "behind on what" — the question a reader deciding
// whether to redeploy tonight is actually asking.
type AheadCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// RemoteState is the reference judged against the live remote head.
type RemoteState struct {
	// Armed is false when there is no remote head to compare against at all —
	// a fixture repo, an offline checkout with no origin, a detached ref.
	// Reported out loud rather than skipped: this witness's finding is an
	// absence, so "printed nothing" and "found nothing wrong" must not look
	// alike.
	Armed  bool         `json:"armed"`
	Target RemoteTarget `json:"target,omitempty"`
	// Err is set when the remote was configured but could not be consulted.
	Err string `json:"error,omitempty"`
	// Head is the live remote sha; Behind says it differs from the reference.
	Head   string `json:"head,omitempty"`
	Behind bool   `json:"behind"`
	// Counted says whether Commits/CorpusCommits are known. They are knowable
	// only when the reference repo already holds the remote head's objects —
	// which, for a mirror that has not fetched since the deploy, it does not.
	// That case is the DEFAULT one on the live box, so it gets an honest
	// "unknown" rather than a zero.
	Counted       bool          `json:"counted"`
	Commits       int           `json:"commits,omitempty"`
	CorpusCommits int           `json:"corpus_commits,omitempty"`
	Corpus        []AheadCommit `json:"corpus,omitempty"`
	Truncated     int           `json:"truncated,omitempty"`
	// Fetched records that --fetch ran, and Was is where the reference stood
	// before it. Keeping the pre-fetch revision is what lets the report still
	// say "the deploy left you here, and this shipped since" after the fetch
	// has moved the ref past it.
	Fetched          bool   `json:"fetched,omitempty"`
	Was              string `json:"was,omitempty"`
	FetchStampMoved  bool   `json:"fetch_stamp_moved,omitempty"`
	FetchStampReason string `json:"fetch_stamp_reason,omitempty"`
}

// Clean reports whether the reference can be trusted to have seen what shipped.
//
// THE RULE, stated here because it is the one judgement call in this file:
//
//	behind + corpus commits known ≥ 1  ->  NOT clean. The installed corpus
//	                                       matches a revision that is provably
//	                                       missing a shipped prompt change.
//	behind + composition UNKNOWN       ->  NOT clean. Unknown is not clean; the
//	                                       reference demonstrably cannot see the
//	                                       window that matters.
//	behind + 0 corpus commits          ->  clean. The corpus verdict above is
//	                                       still exactly right, and this witness
//	                                       judges the corpus. The commit count
//	                                       is printed as context, not as a
//	                                       finding — over-claiming here would be
//	                                       this ticket's own defect.
//	not armed, or armed and unreachable ->  clean, and SAID OUT LOUD. An offline
//	                                       host exiting 1 forever teaches readers
//	                                       to ignore the exit status, which is
//	                                       the failure this command exists to
//	                                       avoid. The corpus-vs-reference verdict
//	                                       is intact either way; what is missing
//	                                       is the qualifier, and the fetch age
//	                                       still carries it.
func (r RemoteState) Clean() bool {
	if !r.Armed || r.Err != "" || !r.Behind {
		return true
	}
	if !r.Counted {
		return false
	}
	return r.CorpusCommits == 0
}

// CheckRemote judges refCommit — the resolved reference — against the branch's
// live remote head.
func CheckRemote(ctx context.Context, repo, ref, refCommit string, timeout time.Duration) RemoteState {
	t, err := ResolveRemoteTarget(ctx, repo, ref)
	if err != nil {
		return RemoteState{Armed: false, Err: err.Error()}
	}
	st := RemoteState{Armed: true, Target: t}

	head, err := LiveRemoteHead(ctx, repo, t, timeout)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	st.Head = head
	if head == refCommit {
		return st
	}
	st.Behind = true
	countAhead(ctx, repo, refCommit, head, &st)
	return st
}

// countAhead fills in the gap when the reference already holds the objects.
//
// `cat-file -e` first, deliberately: rev-list against an unknown object exits
// non-zero with "bad revision", which is indistinguishable at the call site
// from a real failure, and a swallowed error there would print "0 commits
// since" over a reference that is arbitrarily far behind.
func countAhead(ctx context.Context, repo, from, to string, st *RemoteState) {
	if _, err := gitOut(ctx, repo, "cat-file", "-e", to+"^{commit}"); err != nil {
		return
	}
	total, err := revCount(ctx, repo, from, to)
	if err != nil {
		return
	}
	corpus, err := revCount(ctx, repo, from, to, "--", PromptsSubtree)
	if err != nil {
		return
	}
	st.Counted = true
	st.Commits = total
	st.CorpusCommits = corpus
	if corpus == 0 {
		return
	}
	out, err := gitOut(ctx, repo, "log", "--format=%h\x1f%s", from+".."+to, "--", PromptsSubtree)
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sha, subject, ok := strings.Cut(line, "\x1f")
		if !ok {
			continue
		}
		if len(st.Corpus) == aheadCorpusCap {
			st.Truncated = corpus - len(st.Corpus)
			break
		}
		st.Corpus = append(st.Corpus, AheadCommit{SHA: sha, Subject: subject})
	}
}

func revCount(ctx context.Context, repo, from, to string, extra ...string) (int, error) {
	args := append([]string{"rev-list", "--count", from + ".." + to}, extra...)
	out, err := gitOut(ctx, repo, args...)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// FetchReference refreshes exactly the one remote-tracking ref the comparison
// reads, and nothing else.
//
// An explicit refspec rather than a bare `git fetch <remote>`: deploy-src
// carries two hundred and forty polecat branches, and dragging all of them to
// answer a question about main is a detector that costs more than the deploy it
// is watching.
//
// `--no-write-fetch-head` is the guard described at the top of this file — the
// fetch would otherwise rewrite the one file ReadFetchState dates the DEPLOY's
// fetch from, so the remedy would erase the evidence of the fault. A git older
// than 2.29 does not know the flag; there the fetch still happens and the
// caller is told the timestamp moved, which is worse than preserving it and far
// better than moving it in silence.
func FetchReference(ctx context.Context, repo string, t RemoteTarget, timeout time.Duration) (stampMoved bool, err error) {
	if timeout <= 0 {
		timeout = DefaultRemoteTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	spec := fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", t.Branch, t.Name, t.Branch)
	_, err = gitNetOut(bounded, repo, "fetch", "--quiet", "--no-write-fetch-head", "--", t.Name, spec)
	if err == nil {
		return false, nil
	}
	if !strings.Contains(err.Error(), "no-write-fetch-head") {
		if bounded.Err() != nil && ctx.Err() == nil {
			return false, fmt.Errorf("fetching %s from %s gave no answer within %s", t.Branch, t.URL, timeout)
		}
		return false, err
	}
	if _, err := gitNetOut(bounded, repo, "fetch", "--quiet", "--", t.Name, spec); err != nil {
		return false, err
	}
	return true, nil
}
