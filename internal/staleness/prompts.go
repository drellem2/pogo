package staleness

// The PROMPT-CORPUS half of the witness (mg-dd49).
//
// On 2026-08-04 all nine repo-shipped prompts differed from main, and had for
// days. Every polecat dispatched that day ran a superseded worker template and
// every crew agent ran a superseded prompt — including the one that noticed,
// whose own pm-template was 90 lines behind. It was found by accident, when a
// prompt instructed an invocation the CLI had stopped accepting and the dispatch
// ate a 409.
//
// WHY THE EXISTING DRIFT CHECK DID NOT SEE IT. agent.CheckPromptDrift compares
// the installed corpus against the running binary's EMBEDDED copy, and
// `pogo doctor --check` fails loudly on a mismatch. It was not lying: the
// installed corpus really did match the binary's embed, because the binary was
// installed by the same redeploy that installs the prompts. A missed redeploy
// makes both stale TOGETHER, and a comparison between two artifacts that move as
// one cannot see either move. That check answers "did the installer run since
// this binary was built"; the question here is "is what the fleet reads the same
// as what the repo ships", and only a comparison against the REPO can answer it.
//
// So this compares the installed tree against a git ref, with no reference at
// all to the running binary's embed. That is also what lets a stale binary still
// raise the alarm — the property the daemon-revision case needs and the reason
// the deploy runner itself was put out-of-tree (mg-42ac / mg-b7d0).
//
// TWO-SIDED AND CONTENT-BASED, both load-bearing:
//
//   - The installed tree is NOT simply an older main. On 2026-08-04
//     polecat-build-pr.md was 231 lines installed against 230 on main — LONGER.
//     A comparison that only asked "is the installed file behind?" would have
//     reported eight of nine.
//   - Length is not the predicate. Lines are reported for a human to orient on
//     and are never consulted to decide; an edit that swaps a line for another
//     line is the ordinary shape of a prompt change and would be invisible to a
//     line-count test. The decision is a hash of the file body.
//
// The stamp line InstallPrompts writes is stripped before hashing (via
// agent.StripPromptStamp — one definition, next to the writer), since it exists
// in no source copy and would otherwise make every file differ.
//
// WHAT IT DOES NOT JUDGE, and says so every run: files under the corpus
// directories that the ref does not ship. ~/.pogo/agents holds a lot of
// legitimately local material — the crew/pm-*.md stubs, pm/anti-drift-protocol.md, the
// per-PM .toml configs — and reporting them as findings every run would train
// readers to skip the report, which is the line a real staleness surfaces on.
// They are listed as a census instead, the same posture check-prompts takes with
// the flag values it cannot decide.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drellem2/pogo/internal/agent"
)

// PromptsSubtree is where the shipped corpus lives in the repo. Its layout
// under that root mirrors the installed layout under ~/.pogo/agents, which is
// what makes the two comparable path-by-path.
const PromptsSubtree = "internal/agent/prompts"

// PromptFile is one file of a corpus, reduced to what a comparison needs.
type PromptFile struct {
	// Hash is sha256 of the file BODY (stamp stripped). The decision.
	Hash string `json:"hash"`
	// Lines and Bytes are for the reader, never for the decision.
	Lines int `json:"lines"`
	Bytes int `json:"bytes"`
}

// Corpus maps a corpus-relative path ("templates/polecat.md") to its file.
type Corpus map[string]PromptFile

func measure(data []byte) PromptFile {
	body := agent.StripPromptStamp(data)
	sum := sha256.Sum256(body)
	lines := 0
	if len(body) > 0 {
		lines = strings.Count(string(body), "\n")
		if !strings.HasSuffix(string(body), "\n") {
			lines++
		}
	}
	return PromptFile{Hash: hex.EncodeToString(sum[:]), Lines: lines, Bytes: len(body)}
}

// Reference identifies the shipped side of the comparison, so a report can say
// what it compared against instead of asserting a verdict from nowhere.
type Reference struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	// Commit and CommitTime are the resolved ref. They matter because the
	// default repo is a local mirror (~/.pogo/deploy-src) whose origin/main is
	// only as fresh as its last successful fetch — on 2026-08-05 that fetch
	// died on an ssh failure, leaving the mirror five days behind. A reader who
	// can see the reference's own age can tell a clean report from an
	// uninformative one.
	Commit     string `json:"commit"`
	CommitTime string `json:"commit_time"`
}

// gitOut runs git in repo and returns stdout, with stderr folded into the error
// so a failure says why rather than just "exit status 128".
func gitOut(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return out, nil
}

// LoadShippedCorpus reads the prompt corpus out of a git ref without checking
// anything out. Reading the object store rather than a working tree means the
// answer does not depend on what branch the reference repo happens to be on,
// and the reference repo is never written to — a detector that mutates the tree
// it is judging has made itself a participant.
func LoadShippedCorpus(ctx context.Context, repo, ref string) (Corpus, Reference, error) {
	info := Reference{Repo: repo, Ref: ref}

	sha, err := gitOut(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return nil, info, fmt.Errorf("resolving %s in %s: %w", ref, repo, err)
	}
	info.Commit = strings.TrimSpace(string(sha))

	when, err := gitOut(ctx, repo, "log", "-1", "--format=%cI", info.Commit)
	if err != nil {
		return nil, info, fmt.Errorf("dating %s: %w", info.Commit, err)
	}
	info.CommitTime = strings.TrimSpace(string(when))

	names, err := gitOut(ctx, repo, "ls-tree", "-r", "-z", "--name-only", info.Commit, "--", PromptsSubtree)
	if err != nil {
		return nil, info, fmt.Errorf("listing %s at %s: %w", PromptsSubtree, info.Commit, err)
	}

	corpus := Corpus{}
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		rel, err := filepath.Rel(PromptsSubtree, filepath.FromSlash(name))
		if err != nil {
			continue
		}
		data, err := gitOut(ctx, repo, "show", info.Commit+":"+name)
		if err != nil {
			return nil, info, fmt.Errorf("reading %s at %s: %w", name, info.Commit, err)
		}
		corpus[filepath.ToSlash(rel)] = measure(data)
	}
	if len(corpus) == 0 {
		return nil, info, fmt.Errorf("%s holds no files at %s (%s) — nothing to compare against", PromptsSubtree, ref, info.Commit)
	}
	return corpus, info, nil
}

// CorpusLayout is the shape of a shipped corpus: which directories it occupies
// and which file extensions it uses. The installed side is enumerated through
// it so the walk stays inside the corpus and does not sweep up ~/.pogo/agents'
// runtime directories (receipts/, sockets/, per-agent state).
type CorpusLayout struct {
	Dirs       []string
	Extensions map[string]bool
}

// LayoutOf derives the layout from the shipped corpus, so the installed-side
// enumeration is defined by what the repo ships rather than by a hand-kept list
// that would silently stop covering a new directory.
func LayoutOf(c Corpus) CorpusLayout {
	l := CorpusLayout{Extensions: map[string]bool{}}
	seen := map[string]bool{}
	for rel := range c {
		dir := path.Dir(rel)
		if !seen[dir] {
			seen[dir] = true
			l.Dirs = append(l.Dirs, dir)
		}
		if ext := path.Ext(rel); ext != "" {
			l.Extensions[ext] = true
		}
	}
	sort.Strings(l.Dirs)
	return l
}

// LoadInstalledCorpus reads the live corpus under root (~/.pogo/agents),
// enumerated through the shipped layout. It returns the corpus and the paths of
// files it matched but could not read.
//
// Non-recursive within each directory, and extension-filtered, which is what
// keeps the installer's own sidecars out: `mayor.md.dist` and
// `mayor.md.bak-1784309533` have extensions `.dist` and `.bak-1784309533`, so
// neither is mistaken for a prompt. They are the RESULT of a drift being
// handled, and counting them as corpus would report the handling as a defect.
//
// DOTFILES ARE SKIPPED, and the first live run is why. ~/.pogo/agents/templates
// holds `.#polecat.md`, an Emacs lock file — a DANGLING SYMLINK whose extension
// is `.md`, so it matched, and whose target does not exist, so reading it
// failed. An editor left open over a prompt is not a fact about the corpus, and
// before this rule it took the entire prompt witness down with an unreadable
// path. The installer never writes a dotfile; editors put their lock and swap
// files nowhere else.
//
// An unreadable file that ISN'T a dotfile is returned rather than raised,
// because one bad file must not decide the other eight. That is the same reason
// the whole-check error is reserved for a reference that could not be resolved:
// a witness that a stray file can silence has the failure mode it was built to
// remove.
func LoadInstalledCorpus(root string, layout CorpusLayout) (Corpus, []string, error) {
	corpus := Corpus{}
	var unreadable []string
	for _, dir := range layout.Dirs {
		abs := root
		if dir != "." {
			abs = filepath.Join(root, filepath.FromSlash(dir))
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			if os.IsNotExist(err) {
				// A shipped directory with nothing installed under it. Not an
				// error: the per-file comparison reports each of its files as
				// not-installed, which is more useful than one directory-level
				// failure that hides how much is missing.
				continue
			}
			return nil, nil, fmt.Errorf("reading %s: %w", abs, err)
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !layout.Extensions[path.Ext(e.Name())] {
				continue
			}
			rel := e.Name()
			if dir != "." {
				rel = dir + "/" + e.Name()
			}
			data, err := os.ReadFile(filepath.Join(abs, e.Name()))
			if err != nil {
				unreadable = append(unreadable, rel)
				continue
			}
			corpus[rel] = measure(data)
		}
	}
	sort.Strings(unreadable)
	return corpus, unreadable, nil
}

// PromptDelta is one corpus-relative path on which the installed tree and the
// ref disagree.
type PromptDelta struct {
	Path string `json:"path"`
	// Kind is "differs" (both present, bodies unequal) or "not-installed" (the
	// ref ships it and the live tree has no copy — a fleet reading a prompt
	// that is not there falls back to whatever the caller does without one).
	Kind           string `json:"kind"`
	ShippedHash    string `json:"shipped_hash,omitempty"`
	InstalledHash  string `json:"installed_hash,omitempty"`
	ShippedLines   int    `json:"shipped_lines,omitempty"`
	InstalledLines int    `json:"installed_lines,omitempty"`
}

// LineNote describes the size relationship in words, for the human line of the
// report. It is commentary on a decision already made by hash — including the
// case it exists to make unmissable, where the two files are the same length
// and still different.
func (d PromptDelta) LineNote() string {
	switch {
	case d.Kind != "differs":
		return fmt.Sprintf("%d lines shipped, not installed", d.ShippedLines)
	case d.InstalledLines == d.ShippedLines:
		return fmt.Sprintf("same length (%d lines), different content", d.ShippedLines)
	case d.InstalledLines > d.ShippedLines:
		return fmt.Sprintf("installed %d lines, ref %d — installed is LONGER by %d",
			d.InstalledLines, d.ShippedLines, d.InstalledLines-d.ShippedLines)
	default:
		return fmt.Sprintf("installed %d lines, ref %d — installed is behind by %d",
			d.InstalledLines, d.ShippedLines, d.ShippedLines-d.InstalledLines)
	}
}

// ComparePrompts judges the installed corpus against the shipped one and
// returns the deltas plus the census of installed files the ref does not ship.
//
// The census is a return value rather than a log line because a caller that
// prints findings and drops it would be reporting "clean" over an unexamined
// remainder. Both are sorted so two runs over unchanged input read identically.
func ComparePrompts(shipped, installed Corpus) (deltas []PromptDelta, unjudged []string) {
	for rel, want := range shipped {
		got, ok := installed[rel]
		if !ok {
			deltas = append(deltas, PromptDelta{
				Path: rel, Kind: "not-installed",
				ShippedHash: want.Hash, ShippedLines: want.Lines,
			})
			continue
		}
		if got.Hash != want.Hash {
			deltas = append(deltas, PromptDelta{
				Path: rel, Kind: "differs",
				ShippedHash: want.Hash, InstalledHash: got.Hash,
				ShippedLines: want.Lines, InstalledLines: got.Lines,
			})
		}
	}
	for rel := range installed {
		if _, ok := shipped[rel]; !ok {
			unjudged = append(unjudged, rel)
		}
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i].Path < deltas[j].Path })
	sort.Strings(unjudged)
	return deltas, unjudged
}

// PromptReport is the prompt-corpus witness's answer.
type PromptReport struct {
	Reference Reference `json:"reference"`
	// InstalledRoot is the tree that was read, named so a reader can check it.
	InstalledRoot string `json:"installed_root"`
	// Err is set when the comparison could not be made at all — an unreachable
	// reference repo, an unresolvable ref, an unreadable corpus. Distinct from
	// "no deltas": a check that could not run has not found the corpus clean.
	Err     string        `json:"error,omitempty"`
	Shipped int           `json:"shipped_files"`
	Deltas  []PromptDelta `json:"deltas"`
	// Unreadable are corpus-shaped files that could not be read. A finding, not
	// a census line: an unreadable prompt is a prompt whose content is unknown,
	// and unknown is not the same as matching.
	Unreadable []string `json:"unreadable,omitempty"`
	Unjudged   []string `json:"unjudged"`
}

// Clean reports whether the fleet is reading what the ref ships.
func (r PromptReport) Clean() bool {
	return r.Err == "" && len(r.Deltas) == 0 && len(r.Unreadable) == 0
}

// CheckPrompts runs the prompt-corpus comparison end to end.
func CheckPrompts(ctx context.Context, repo, ref, installedRoot string) PromptReport {
	rep := PromptReport{InstalledRoot: installedRoot}
	shipped, info, err := LoadShippedCorpus(ctx, repo, ref)
	rep.Reference = info
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	rep.Shipped = len(shipped)
	installed, unreadable, err := LoadInstalledCorpus(installedRoot, LayoutOf(shipped))
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	rep.Unreadable = unreadable
	rep.Deltas, rep.Unjudged = ComparePrompts(shipped, installed)
	return rep
}
