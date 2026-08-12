package agent

// The PM's transcript-friction scan has to point at where transcripts actually
// are, and has to be able to tell "no friction" apart from "blind" (mg-75b7).
//
// # The defect
//
// pm-template.md prescribed, as the {{.Worker}}/crew transcript friction scan:
//
//	grep -i -E "(annoying|frustrat|wish|...)" ~/.pogo/polecats/*/transcript.* | tail -200
//
// `~/.pogo/polecats/<id>/` is a git WORKTREE. It holds repo files — LICENSE,
// README.md, cmd/, internal/ — and there is no `transcript.*` in it, ever. So
// the scan returned zero on every sweep since it was written, and a PM
// following the template reported "no friction signals" permanently. For
// pm-pogo that is worse than a generic gap: its config names transcripts as its
// distinguishing source.
//
// Re-run over the same 24h window against the real location, the same patterns
// went annoying 0→9, confusing 0→6, frustrat 0→2 — and one hit was a verbatim
// Daniel complaint about duplicate watcher mail that had reached nobody for a
// day. That is the positive control the old command could not have passed.
//
// # Why a corpus test and not just the edit
//
// The edit is one paragraph and a code block; nothing about it resists being
// re-broken. Two properties in particular look like decoration to a later
// reader tidying a long prompt, and each one returns the scan to being blind:
//
//   - The LOCATION. Claude Code writes session transcripts to
//     ~/.claude/projects/<slug-of-workdir>/*.jsonl, slugged from the working
//     directory — so a {{.Worker}}'s transcript lives OUTSIDE its worktree, by
//     construction. Any prescription that reaches for a transcript file inside
//     `.pogo/polecats/…` or `.pogo/agents/…` is the original defect, whichever
//     prompt it appears in. That assertion is corpus-wide for that reason.
//
//   - The DENOMINATOR. A scan that prints only its hits emits identical output
//     — nothing — whether it read 63 transcripts and found no friction or read
//     zero files. That indistinguishability is what let this defect survive
//     from the day it was written; it is not incidental to it. The corrected
//     command therefore prints how many transcripts it read, and that line is
//     asserted.
//
// # What is deliberately NOT asserted
//
// Not the pattern set, which is a judgement call a PM should be free to tune —
// `surprising` was measured to be swamped by mathematical prose in proof-repo
// worktrees and left out, but that is guidance, not an invariant.
//
// Not the "no leading `.{0,80}` context" performance rule either, though it is
// documented in the prompt and was measured at ~96× (9.6s vs 0.10s on 2.3 MB;
// the full 62 MB corpus blew a 2-minute timeout). Losing it makes the scan slow
// — which is loud. Only the silent failure modes are pinned here.

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// worktreeTranscriptPath matches a transcript file addressed under an agent's
// working directory — the defect itself. The `[^\s` + backtick + `]*` bound
// keeps it from spanning prose: pm-template.md now says "`~/.pogo/polecats/<id>/`
// is a git **worktree** … never a `transcript.*`", and that sentence must not
// trip the assertion that the sentence exists to explain.
var worktreeTranscriptPath = regexp.MustCompile("\\.pogo/(?:polecats|agents)/[^\\s`]*transcript")

func TestNoPromptLooksForATranscriptInsideAWorktree(t *testing.T) {
	var checked int
	err := fs.WalkDir(DefaultPromptsFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		b, err := fs.ReadFile(DefaultPromptsFS(), p)
		if err != nil {
			return err
		}
		checked++
		if m := worktreeTranscriptPath.FindString(string(b)); m != "" {
			t.Errorf("%s prescribes %q — a transcript path inside an agent's working directory.\n%s",
				p, m, indent(
					"~/.pogo/polecats/<id>/ and ~/.pogo/agents/<name>/ are git worktrees and "+
						"agent state dirs; harness session transcripts are never in them. Claude "+
						"Code writes them to ~/.claude/projects/<slug-of-workdir>/*.jsonl (see "+
						"projectSlug in internal/claude/provider.go). A scan of the worktree "+
						"returns zero forever and reads as \"no friction\" (mg-75b7)."))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the prompt corpus: %v", err)
	}
	if checked == 0 {
		t.Fatal("no prompts were read; this test asserted nothing")
	}
	t.Logf("checked %d prompts", checked)
}

// frictionScanSection returns pm-template.md's transcript-friction bullet, from
// its heading to the start of the next top-level bullet. The assertions have to
// hold IN THAT BULLET: pm-template.md is ~700 lines and mentions `.claude` and
// `find` elsewhere, so a whole-body match would be a presence check wearing a
// correctness label.
var frictionScanHeading = regexp.MustCompile(`(?m)^- \*\*\{\{\.WorkerTitle\}\} / crew transcripts\*\*`)

func frictionScanSection(body string) string {
	loc := frictionScanHeading.FindStringIndex(body)
	if loc == nil {
		return ""
	}
	rest := body[loc[0]+1:]
	if i := regexp.MustCompile(`(?m)^- \*\*`).FindStringIndex(rest); i != nil {
		rest = rest[:i[0]]
	}
	return rest
}

var frictionScanProperties = []struct {
	what string
	re   *regexp.Regexp
	why  string
}{
	{
		what: "the real transcript location",
		re:   regexp.MustCompile(`\.claude/projects`),
		why: "This is the whole correction. Transcripts are at " +
			"~/.claude/projects/<slug-of-workdir>/*.jsonl, not in the agent's " +
			"working directory. A scan that does not name this path is scanning " +
			"something that holds no transcripts.",
	},
	{
		what: "the slug derivation, and where pogo declares it",
		re:   regexp.MustCompile(`projectSlug|\[\^A-Za-z0-9\]`),
		why: "The directory name is the workdir with every byte outside [A-Za-z0-9] " +
			"replaced by '-'. That encoding is the harness's, not pogo's, and " +
			"internal/claude/provider.go's projectSlug is where pogo declares it. " +
			"Without the citation the command and the code drift apart silently — " +
			"and the failure mode of a wrong slug is finding no files, which is " +
			"exactly the defect being fixed.",
	},
	{
		what: "the denominator the scan prints for itself",
		re:   regexp.MustCompile(`(?i)scanned \$?\(?[^\n]*transcripts`),
		why: "A scan that prints only hits gives byte-identical output — nothing — " +
			"whether it read 63 transcripts and found no friction or read zero " +
			"files. That is how the broken version survived every sweep since it " +
			"was written. Printing the count is what makes the two distinguishable.",
	},
	{
		what: "a guard that names the slug when nothing matches",
		re:   regexp.MustCompile(`(?i)WARNING: no session dirs`),
		why: "Zero session dirs and zero friction must not look alike either. The " +
			"guard names the slug it looked for, so a wrong HOME or a changed " +
			"harness encoding reports itself instead of reporting an all-clear.",
	},
	{
		what: "the mtime bound on the corpus",
		re:   regexp.MustCompile(`-newermt`),
		why: "There are ~870 session dirs. The mtime filter is the only thing " +
			"keeping a twice-daily sweep off tens of gigabytes; an unbounded grep " +
			"across all of them is a different problem from the one being fixed.",
	},
	{
		what: "that the output is a candidate list, not a finding",
		re:   regexp.MustCompile(`(?i)candidate list, not a finding`),
		why: "The corrected scan's first run yielded a real Daniel complaint AND a " +
			"code comment using \"annoying\" descriptively AND the fleet's own " +
			"tickets about friction — mg-75b7's dispatch ranked first. A PM that " +
			"reads the ranking as a result files tickets about tickets.",
	},
}

func TestPMTranscriptFrictionScanCanTellBlindFromQuiet(t *testing.T) {
	b, err := fs.ReadFile(DefaultPromptsFS(), "pm/pm-template.md")
	if err != nil {
		t.Fatalf("reading pm/pm-template.md: %v", err)
	}
	section := frictionScanSection(string(b))
	if strings.TrimSpace(section) == "" {
		t.Fatalf("pm/pm-template.md has no transcript-friction bullet matching /%s/.\n%s",
			frictionScanHeading, indent(
				"Either the source was dropped — it is pm-pogo's distinguishing source, "+
					"per its config — or the heading drifted and this test is now "+
					"asserting nothing (mg-75b7)."))
	}
	for _, req := range frictionScanProperties {
		if req.re.MatchString(section) {
			continue
		}
		t.Errorf("the transcript-friction bullet does not state %s (want /%s/).\n%s\nbullet:\n%s",
			req.what, req.re, indent(req.why), indent(section))
	}
}
