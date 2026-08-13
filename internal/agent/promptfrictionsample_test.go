package agent

// The PM's transcript-friction scan has to sample the population it exists to
// sample, and its conclusion has to carry a denominator (mg-08f7).
//
// # The defect
//
// mg-75b7 pointed the scan at the real transcript location and made it print
// how many transcripts it read. What it left behind was a single merged
// ranking and the instruction:
//
//	That prints a hit-count ranking, which is a candidate list, not a finding.
//	Read the top sessions in the same shell.
//
// Match count tracks SESSION LENGTH, not friction density. Crew agents (mayor,
// architect, doctor, the PMs) run for hours and accumulate hits across a whole
// shift; workers are short-lived and numerous and each carries a handful. So
// the head of a merged ranking is crew BY CONSTRUCTION, and "read the top
// sessions" prescribes a crew-weighted sample no matter how carefully it is
// executed.
//
// That is a method defect, not an execution one — the distinction decides the
// fix. A mistake is repaired by being more careful next time; this is repaired
// only by changing the method, because the procedure cannot be executed
// correctly into a representative sample.
//
// It matters because pm-pogo's config names WORKER transcripts as its
// distinguishing source — the place real product friction shows up — while
// crew sessions are largely agents DISCUSSING friction, which is the noise
// class the same bullet already warns about.
//
// # Measured, 2026-08-13, 145 transcripts in a 24h window
//
//	polecat  163 hits in 61 sessions   (51% of hits, 82% of sessions)
//	crew     155 hits in 13 sessions   (49% of hits, 18% of sessions)
//	top 12 of the merged ranking:      9 crew, 3 polecat
//
// Workers are the majority in aggregate and a minority in the head. Reading
// the merged head, pm-pogo estimated crew at ~76% of hits; the aggregate said
// 49%. That morning's sweep concluded "no new friction gap" after reading 2
// worker sessions out of a 171-hit worker population, having followed the
// procedure exactly.
//
// # What this file pins
//
// Two things, one static and one executable.
//
// The STATIC half asserts the bullet still ranks the two classes separately,
// still prints the per-class denominator, still points the read at the worker
// list, and still requires the conclusion to state its sample size. Each is a
// line a later reader tidying a 700-line prompt could take for decoration, and
// each one returns the scan to sampling the wrong population.
//
// The EXECUTABLE half is the positive control the ticket asked for, and it is
// here because the static half cannot distinguish a command that SAYS it ranks
// by class from one that does. It builds a corpus with the measured shape —
// crew carrying more total hits, a worker session carrying real friction below
// every crew session — then runs the SHIPPED command out of the prompt body
// and the FROZEN pre-mg-08f7 command over the same corpus, and asserts the
// planted session is buried by the old one and surfaced by the new one.
// Running the shipped text rather than a copy of it is the point: a copy
// drifts, and the drift is invisible in exactly the way the original defect
// was.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// readTheWorkerListMarker is the instruction the scan prints above the class
// the PM is supposed to sample. The assertions locate the worker ranking by
// this marker rather than by the rendered role noun, because the role noun is
// deployment-configurable ({{.Worker}} defaults to "pogocat", mg-ccec) and the
// instruction is not.
const readTheWorkerListMarker = "READ THE TOP OF THIS LIST"

var frictionSampleProperties = []struct {
	what string
	re   *regexp.Regexp
	why  string
}{
	{
		what: "a per-class hit denominator the scan prints for itself",
		re:   regexp.MustCompile(`(?i)polecat %d in %d sessions.*crew %d in %d sessions`),
		why: "The scan already printed how many transcripts it READ (mg-75b7). It " +
			"did not print how the hits SPLIT, so a reader could not price a " +
			"conclusion without re-deriving the split by hand — and the one " +
			"reader who eyeballed it off the ranked head got 76% crew where the " +
			"aggregate said 49%. Both classes must print, zero-filled, so " +
			"\"the workers were quiet\" and \"the worker half of the scan " +
			"broke\" stay distinguishable.",
	},
	{
		what: "two rankings, split on the worker/crew class of the session dir",
		re:   regexp.MustCompile(`\$2 ~ /\^polecats-/[\s\S]*\$2 !~ /\^polecats-/`),
		why: "One merged ranking sorted by hit count is crew-weighted by " +
			"construction, because hit count tracks session length. Ranking the " +
			"classes separately is the fix; no amount of care in reading a " +
			"merged ranking substitutes for it.",
	},
	{
		what: "which of the two lists the PM is told to read",
		re:   regexp.MustCompile(regexp.QuoteMeta(readTheWorkerListMarker)),
		why: "Worker transcripts are the source pm-pogo's config names as its " +
			"distinguishing one; crew sessions are mostly agents discussing " +
			"friction, which is noise for this purpose. Printing both rankings " +
			"and leaving the choice implicit re-creates the defect one step " +
			"later, because the crew list is longer per session and reads as " +
			"the richer one.",
	},
	{
		what: "that the conclusion carries its denominator",
		re:   regexp.MustCompile(`(?i)denominator in the conclusion`),
		why: "\"No new friction gap\" and \"no new friction gap in the 5 worker " +
			"sessions I read, of 61 carrying hits\" are the same sentence to the " +
			"writer and completely different evidence to whoever reads the " +
			"digest. The sweep that exposed this read 2 of 171 and reported the " +
			"bare form. The scan now hands the denominator over, so there is " +
			"nothing to look up.",
	},
}

func TestPMFrictionScanSamplesTheWorkerPopulation(t *testing.T) {
	b, err := fs.ReadFile(DefaultPromptsFS(), "pm/pm-template.md")
	if err != nil {
		t.Fatalf("reading pm/pm-template.md: %v", err)
	}
	section := frictionScanSection(string(b))
	if strings.TrimSpace(section) == "" {
		t.Fatalf("pm/pm-template.md has no transcript-friction bullet matching /%s/.\n%s",
			frictionScanHeading, indent(
				"Either the source was dropped or the heading drifted and this test is "+
					"now asserting nothing (mg-75b7, mg-08f7)."))
	}
	for _, req := range frictionSampleProperties {
		if req.re.MatchString(section) {
			continue
		}
		t.Errorf("the transcript-friction bullet does not state %s (want /%s/).\n%s",
			req.what, req.re, indent(req.why))
	}
}

// preMG08f7Ranking is the ranking half of the scan exactly as it shipped
// before this ticket. It is frozen history, not a copy that can drift: its
// only job is to demonstrate what the old instruction ("read the top
// sessions") put in front of a PM over a corpus whose shape is known.
const preMG08f7Ranking = `
printf '%s\n' "$files" | grep -v '^$' | tr '\n' '\0' |
  xargs -0 -r grep -icE "$pat" 2>/dev/null |
  awk -F: '$2>0 {print $2"\t"$1}' | sort -rn | sed "s|$proj/$slug-||"
`

// mergedHeadRead is how many sessions "read the top sessions" plausibly means.
// The control has to hold for any reasonable reading of an unquantified
// instruction, so the planted session is placed below the largest of them.
const mergedHeadRead = 12

// frictionScanBlock returns the first fenced bash block inside the
// transcript-friction bullet, with the bullet's two-space indent stripped and
// role placeholders resolved through the shipped substitution path. The
// closing fence is indented too — matching a bare "\n```" runs off the end of
// the bullet and swallows the rest of the file.
var fencedBash = regexp.MustCompile("(?s)```bash\n(.*?)\n[ ]*```")

func frictionScanBlock(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(DefaultPromptsFS(), "pm/pm-template.md")
	if err != nil {
		t.Fatalf("reading pm/pm-template.md: %v", err)
	}
	m := fencedBash.FindStringSubmatch(frictionScanSection(string(b)))
	if m == nil {
		t.Fatalf("the transcript-friction bullet contains no ```bash block.\n%s", indent(
			"The scan is a command, and this test runs the shipped one rather than a "+
				"copy so the two cannot drift apart (mg-08f7)."))
	}
	body := regexp.MustCompile(`(?m)^  `).ReplaceAllString(m[1], "")
	return substituteRoleNames(body)
}

// plantedSession is the worker session carrying real friction. Its hit count
// sits above every other worker session and below every crew session, which is
// the shape the 2026-08-13 measurement had and the shape that separates the
// two methods.
const (
	plantedSession = "polecats-pplant"
	plantedHits    = 9
	crewSessions   = 12 // hits 20..31 — every one above the plant
	workerSessions = 20 // hits 1..8 — every one below the plant
)

// writeCorpus builds a $HOME whose ~/.claude/projects looks like a 24h window
// of fleet sessions, and returns the home directory. Sessions are one JSONL
// line per hit; the scan counts matching LINES, so the count is exact.
func writeCorpus(t *testing.T) string {
	t.Helper()
	// A short root, deliberately. BSD xargs -I{} caps the assembled command
	// line at 255 bytes, and the session-dir name is the home path slugged —
	// so a deep temp root makes `find` produce nothing and the scan report
	// "scanned 0", which would make this test assert nothing while passing.
	// runScan checks for that and says so.
	root := ""
	if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
		root = "/tmp"
	}
	home, err := os.MkdirTemp(root, "pfs")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(filepath.Join(home, ".pogo"), "-")
	write := func(session string, lines []string) {
		dir := filepath.Join(home, ".claude", "projects", slug+"-"+session)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		f := filepath.Join(dir, "sess.jsonl")
		if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	hitLines := func(n int, last string) []string {
		out := make([]string, 0, n)
		for i := 0; i < n-1; i++ {
			out = append(out, `{"text":"this step was confusing"}`)
		}
		return append(out, last)
	}

	for i := 0; i < crewSessions; i++ {
		write(fmt.Sprintf("agents-crew%02d", i), hitLines(20+i, `{"text":"confusing"}`))
	}
	for i := 0; i < workerSessions; i++ {
		write(fmt.Sprintf("polecats-w%02d", i), hitLines(1+i%8, `{"text":"confusing"}`))
	}
	// The finding. First-person, product-shaped, and invisible to a crew-weighted
	// sample — the class of hit this source exists to catch.
	write(plantedSession, hitLines(plantedHits,
		`{"text":"I had to manually re-stamp the claim because pogo schedule ack said stale token — annoying"}`))
	return home
}

func runScan(t *testing.T, home, script string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the scan: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "scanned 0 transcripts") {
		t.Fatalf("the scan read no transcripts from the fixture at %s, so this test asserted nothing.\n%s\noutput:\n%s",
			home, indent(
				"Most likely the temp home is deep enough that the slugged session-dir "+
					"paths blow BSD xargs -I{}'s 255-byte command-line cap, and `find` "+
					"emitted nothing. That is a property of the fixture path, not of the "+
					"scan as the fleet runs it (real homes are ~/.pogo)."), indent(s))
	}
	return s
}

// ranked parses "<hits>\t<session>" lines out of a block of scan output.
func ranked(block string) []string {
	var out []string
	for _, ln := range strings.Split(block, "\n") {
		f := strings.SplitN(ln, "\t", 2)
		if len(f) != 2 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(f[0])); err != nil {
			continue
		}
		out = append(out, strings.TrimSpace(f[1]))
	}
	return out
}

func indexOfSession(list []string, session string) int {
	for i, s := range list {
		if strings.HasPrefix(s, session+"/") {
			return i
		}
	}
	return -1
}

func TestPMFrictionScanSurfacesAWorkerSessionTheMergedRankingBuries(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("no bash: %v", err)
	}
	home := writeCorpus(t)
	shipped := frictionScanBlock(t)

	// The old method: the same preamble the shipped block still opens with, plus
	// the frozen pre-mg-08f7 ranking. Taking the preamble from the shipped text
	// keeps the two runs reading the SAME corpus the same way, so the only thing
	// under comparison is how the results are ranked and presented.
	preamble := shipped
	if i := strings.Index(preamble, "ranked=$("); i >= 0 {
		preamble = preamble[:i]
	} else {
		t.Fatalf("the shipped scan no longer assigns `ranked=$(`; this test's split of "+
			"preamble-from-ranking is stale.\n%s", indent(shipped))
	}
	oldOut := runScan(t, home, preamble+preMG08f7Ranking)
	newOut := runScan(t, home, shipped)

	// State the control's premise from the data rather than assuming it: crew
	// must carry more total hits than the workers, or the two methods are not
	// being asked to disagree about anything.
	var crewHits, workerHits int
	for _, ln := range strings.Split(oldOut, "\n") {
		f := strings.SplitN(ln, "\t", 2)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(f[1]), "polecats-") {
			workerHits += n
		} else {
			crewHits += n
		}
	}
	if crewHits <= workerHits {
		t.Fatalf("fixture premise not met: crew %d hits, worker %d — the control needs crew "+
			"to carry MORE total hits than the workers, which is the situation that makes a "+
			"merged ranking crew-weighted", crewHits, workerHits)
	}
	t.Logf("fixture: crew %d hits, worker %d hits; plant carries %d", crewHits, workerHits, plantedHits)

	// The old method buries it: "read the top sessions" over one merged ranking
	// never reaches the plant.
	merged := ranked(oldOut)
	at := indexOfSession(merged, plantedSession)
	if at < 0 {
		t.Fatalf("the planted session is missing from the merged ranking entirely; the "+
			"fixture is wrong, not the method.\n%s", indent(oldOut))
	}
	if at < mergedHeadRead {
		t.Fatalf("the planted worker session ranked #%d of the merged ranking, inside any "+
			"reading of \"read the top sessions\" (<=%d).\n%s",
			at+1, mergedHeadRead, indent(
				"This test's fixture no longer reproduces the defect, so its verdict on "+
					"the new method means nothing. The plant must sit below every crew "+
					"session in the merged ranking."))
	}

	// The new method surfaces it: it is the head of the list the PM is told to read.
	i := strings.Index(newOut, readTheWorkerListMarker)
	if i < 0 {
		t.Fatalf("the scan's output never says %q, so nothing tells the PM which ranking to "+
			"sample.\n%s", readTheWorkerListMarker, indent(newOut))
	}
	workerList := ranked(newOut[i:])
	if len(workerList) == 0 {
		t.Fatalf("the worker ranking is empty.\n%s", indent(newOut))
	}
	if !strings.HasPrefix(workerList[0], plantedSession+"/") {
		t.Errorf("the planted session is not at the head of the worker ranking (head is %q).\n%s\noutput:\n%s",
			workerList[0], indent(
				"It carries more hits than every other worker session in the fixture, so "+
					"if it is not first the classes are not being ranked separately."), indent(newOut))
	}

	// And the denominator is on the output, without a further command.
	wantSplit := fmt.Sprintf("polecat %d in %d sessions", workerHits, workerSessions+1)
	if !strings.Contains(newOut, wantSplit) {
		t.Errorf("the scan did not print the worker denominator %q.\n%s\noutput:\n%s",
			wantSplit, indent(
				"A sweep has to be able to say \"read K of N\" from the scan's own output. "+
					"Re-deriving N by hand is what produced the 76%-crew estimate the "+
					"aggregate refuted."), indent(newOut))
	}
	if !strings.Contains(newOut, fmt.Sprintf("crew %d in %d sessions", crewHits, crewSessions)) {
		t.Errorf("the scan did not print the crew denominator (crew %d in %d sessions).\n%s\noutput:\n%s",
			crewHits, crewSessions, indent(
				"Both classes print, always. A missing line and a zero must not look alike."),
			indent(newOut))
	}
}
