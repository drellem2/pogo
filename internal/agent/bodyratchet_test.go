package agent

// A ratchet on the `--body="..."` idiom in shipped prompt templates (mg-d91f).
//
// WHY THIS LIVES HERE AND NOT IN THE BINARIES. mg-7850 and mg-8380 fixed the
// tools: both `mg` and `pogo agent spawn-polecat` grew --body-file, and both
// document it. Neither fix moved the number, because an agent does not learn
// its idioms from --help — it copies them from its own prompt. The architect's
// walk of this tree (mg-e0ca) found the gradient at its source:
//
//	teach --body="..."       (UNSAFE):  62
//	teach --body-file <path> (2-step):   0
//	teach --body-file -      (1-step):   0
//
// The zero is the load-bearing number: there is not one safe example anywhere
// in the corpus an agent actually reads. And the inflow exceeds the stock — 80
// new `--body="` example lines were added in the 30 days before this ticket,
// more than the entire standing count. A one-time sweep is a snapshot fix on a
// target our own authoring moves, so the ratchet is the deliverable and the
// sweep is not: new violations fail at the moment of authoring, existing ones
// are grandfathered until they are swept.
//
// WHAT COUNTS AS A VIOLATION — EXAMPLE LINES ONLY. A bare grep for `--body="`
// is the wrong predicate in both directions: it fires on a template that
// documents "never use `--body=`" — punishing the very prose that fixes this —
// and it passes a template that teaches the idiom without the literal token. So
// the predicate is anchored to *example* lines: fenced or indented command
// blocks, with comments excluded. Prose is how you talk about the hazard;
// example blocks are how you teach it. Only teaching is ratcheted.
//
// THE QUOTING IS THE WHOLE PROPERTY. The safe idiom is a heredoc with a QUOTED
// delimiter:
//
//	mg mail send mayor --from=me --subject=s --body-file - <<'EOF'
//	body text with `backticks` and $VARS, all literal
//	EOF
//
// `<<EOF` expands exactly like `--body="..."` and silently reintroduces the
// bug while looking correct to a reviewer, so an unquoted heredoc on a
// --body-file example is a violation too — and unlike the inline form it is
// grandfathered nowhere. There are zero today; there must stay zero.
//
// Verified on both binaries before shipping: `pogo agent spawn-polecat
// --body-file -` fed a quoted heredoc carrying a backtick, an unset $VAR and
// both quote styles rendered all three byte-exact into the polecat's prompt
// file. See TestSpawnPolecat_BodyFileStdin and the A/B/C refutation control in
// cmd/pogo/bodymetachar_test.go.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// bodyRatchet is the frozen inventory of grandfathered `--body="` example
// lines, by path. Two properties make a hard-coded literal tolerable here, and
// both must keep holding:
//
//   - It is MONOTONICALLY DECREASING. Every entry may shrink and none may grow;
//     an entry that reaches zero is deleted. Shrinking is the goal, so the
//     literal's only legitimate motion is toward its own deletion.
//   - Raising an entry is not a remedy the failure message offers. The message
//     names --body-file with a quoted heredoc and says so explicitly. If the
//     inventory can be grown to silence the test, the ratchet is decorative.
//
// Paths, not a bare count: a count is arbitrary and tells an author nothing.
// bodyRatchetTotal below is a deliberate second edit site, so raising a number
// cannot be done as a one-character diff that reads as noise in review.
// The total is 40, not the architect's 62 and not grep's 58. The three numbers
// measure different things and only one of them is the predicate: 62 counted
// every mention, grep counts the raw token including prose, and 40 is what this
// tree actually TEACHES on an example line. The zero that matters is unchanged
// under all three — there is no safe example anywhere in the corpus.
var bodyRatchet = map[string]int{
	"crew/doctor.md":                 3,
	"mayor.md":                       15,
	"pm/pm-template.md":              3,
	"templates/polecat-architect.md": 1,
	"templates/polecat-build-pr.md":  4,
	"templates/polecat-qa.md":        2,
	"templates/polecat-review.md":    6,
	"templates/polecat-triage.md":    4,
	"templates/polecat.md":           2,
}

// bodyRatchetTotal must equal the sum of bodyRatchet. It exists only to make
// raising an entry require two edits that agree — a speed bump on the one
// change this file is built to discourage.
const bodyRatchetTotal = 40

// safeIdiom is the remedy every failure message carries. An author who trips
// the ratchet must not have to go looking for what to do instead.
const safeIdiom = "use --body-file - with a QUOTED heredoc:\n" +
	"    mg mail send mayor --from=me --subject=s --body-file - <<'EOF'\n" +
	"    body text with `backticks` and $VARS, all literal\n" +
	"    EOF\n" +
	"  The quoting is the whole property: <<'EOF' is literal, <<EOF expands\n" +
	"  exactly like --body=\"...\" and silently reintroduces the bug.\n" +
	"  Do NOT add to the bodyRatchet inventory — it may only shrink."

// inlineBodyRE matches an inline double-quoted body value: --body="..." or
// --body "...". Single quotes are deliberately NOT matched — `--body='...'`
// reaches argv unmangled and is not the defect. --body-file cannot match: the
// character after "body" is a hyphen.
var inlineBodyRE = regexp.MustCompile(`--body(=|\s+)"`)

// heredocRE captures the delimiter of a heredoc redirect and whether it was
// quoted. Group 1 is the opening quote, empty when the delimiter is bare.
var heredocRE = regexp.MustCompile(`<<-?\s*(['"]?)[A-Za-z_][A-Za-z0-9_]*`)

type bodyViolation struct {
	Line int
	Kind string // "inline-body" or "unquoted-heredoc"
	Text string
}

// scanBodyExamples reports body-flag violations on the EXAMPLE lines of a
// markdown file. An example line is one inside a fenced code block, or indented
// far enough to be an indented code block. Prose and comments are exempt on
// purpose (see the file header): documenting the hazard must not be punished by
// the check that exists to reduce it.
func scanBodyExamples(src string) []bodyViolation {
	var out []bodyViolation
	inFence := false
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue // the fence marker itself is never an example line
		}

		indented := strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
		if !inFence && !indented {
			continue // prose
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, ">") {
			continue // comment
		}

		if inlineBodyRE.MatchString(line) {
			out = append(out, bodyViolation{Line: i + 1, Kind: "inline-body", Text: trimmed})
		}
		if strings.Contains(line, "--body-file") {
			if m := heredocRE.FindStringSubmatch(line); m != nil && m[1] == "" {
				out = append(out, bodyViolation{Line: i + 1, Kind: "unquoted-heredoc", Text: trimmed})
			}
		}
	}
	return out
}

// checkBodyRatchet walks a prompt tree and returns one problem string per
// ratchet failure. Empty means the tree holds the line.
func checkBodyRatchet(root fs.FS) ([]string, error) {
	counts := map[string]int{}
	var problems []string

	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		for _, v := range scanBodyExamples(string(data)) {
			if v.Kind == "unquoted-heredoc" {
				// Never grandfathered. There are zero of these today and the
				// only way one appears is a new example written wrong.
				problems = append(problems, fmt.Sprintf(
					"%s:%d teaches an UNQUOTED heredoc — %q\n  A bare <<EOF expands the body exactly like --body=\"...\".\n  Quote the delimiter: <<'EOF'",
					path, v.Line, v.Text))
				continue
			}
			counts[path]++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for path, got := range counts {
		allowed, known := bodyRatchet[path]
		switch {
		case !known:
			problems = append(problems, fmt.Sprintf(
				"%s teaches %d unsafe --body=\"...\" example line(s) and is not in the ratchet inventory.\n  %s",
				path, got, safeIdiom))
		case got > allowed:
			problems = append(problems, fmt.Sprintf(
				"%s teaches %d unsafe --body=\"...\" example line(s); the ratchet allows %d.\n  %s",
				path, got, allowed, safeIdiom))
		}
	}

	// The ratchet must also TIGHTEN. A swept file whose inventory entry was not
	// lowered leaves headroom for the next author to silently re-add what was
	// just removed.
	for path, allowed := range bodyRatchet {
		got := counts[path]
		if got < allowed {
			problems = append(problems, fmt.Sprintf(
				"%s now teaches only %d unsafe --body=\"...\" example line(s) but the ratchet still allows %d.\n"+
					"  Lower bodyRatchet[%q] to %d (and bodyRatchetTotal by %d) to lock the gain in.",
				path, got, allowed, path, got, allowed-got))
		}
	}

	sort.Strings(problems)
	return problems, nil
}

// TestBodyRatchet_ShippedPromptsHoldTheLine is the standing guard: the shipped
// prompt tree must not grow a new unsafe example, and must not silently keep
// headroom after a sweep.
func TestBodyRatchet_ShippedPromptsHoldTheLine(t *testing.T) {
	problems, err := checkBodyRatchet(os.DirFS("prompts"))
	if err != nil {
		t.Fatalf("walking prompts: %v", err)
	}
	for _, p := range problems {
		t.Errorf("%s", p)
	}
}

// TestBodyRatchet_TotalAgreesWithInventory keeps the two edit sites honest.
func TestBodyRatchet_TotalAgreesWithInventory(t *testing.T) {
	sum := 0
	for _, n := range bodyRatchet {
		sum += n
	}
	if sum != bodyRatchetTotal {
		t.Errorf("bodyRatchet sums to %d but bodyRatchetTotal is %d.\n"+
			"If you are SHRINKING the inventory, lower both. If you are raising it, don't: %s",
			sum, bodyRatchetTotal, safeIdiom)
	}
}

// copyPromptTree materializes the real prompt tree into a temp dir so a test
// can perturb it. The controls below must run against the REAL corpus, not a
// toy fixture: a ratchet that only ever fires on a hand-built file has not been
// shown to fire on the thing it guards.
func copyPromptTree(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := os.DirFS("prompts")
	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dst, path)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying prompt tree: %v", err)
	}
	return dst
}

func appendToTemplate(t *testing.T, dir, rel, text string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, []byte(text)...), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestBodyRatchet_FailsOnNewInlineBodyExample is the positive control the
// acceptance criteria ask for by name, and the reason to trust every green run
// of the guard above. A ratchet only ever observed passing on the existing
// corpus has not been tested — it may be structurally incapable of failing.
func TestBodyRatchet_FailsOnNewInlineBodyExample(t *testing.T) {
	dir := copyPromptTree(t)
	appendToTemplate(t, dir, "templates/polecat.md",
		"\n```bash\nmg mail send mayor --from=me --subject=hi --body=\"new unsafe example\"\n```\n")

	problems, err := checkBodyRatchet(os.DirFS(dir))
	if err != nil {
		t.Fatalf("walking perturbed tree: %v", err)
	}
	if len(problems) == 0 {
		t.Fatal("ratchet did not fire on a NEW --body=\"...\" example line — it cannot catch the defect it exists for")
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "templates/polecat.md") {
		t.Errorf("failure must name the offending file, got:\n%s", joined)
	}
	if !strings.Contains(joined, "--body-file -") || !strings.Contains(joined, "<<'EOF'") {
		t.Errorf("failure must name the safe idiom (--body-file - with <<'EOF'), got:\n%s", joined)
	}
}

// TestBodyRatchet_PassesOnQuotedHeredocExample is the other half of the
// control: the remedy the failure message recommends must itself pass. A guard
// that lints its own cure gets suppressed, and a suppressed guard is worse than
// none.
func TestBodyRatchet_PassesOnQuotedHeredocExample(t *testing.T) {
	dir := copyPromptTree(t)
	appendToTemplate(t, dir, "templates/polecat.md",
		"\n```bash\nmg mail send mayor --from=me --subject=hi --body-file - <<'EOF'\n"+
			"body with `backticks` and $VARS, all literal\nEOF\n```\n")

	problems, err := checkBodyRatchet(os.DirFS(dir))
	if err != nil {
		t.Fatalf("walking perturbed tree: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("the safe idiom must pass the ratchet, got:\n%s", strings.Join(problems, "\n"))
	}
}

// TestBodyRatchet_FailsOnUnquotedHeredoc covers the regression that will look
// correct to a reviewer: --body-file with a BARE <<EOF, which expands the body
// exactly like --body="..." and undoes the fix while appearing to apply it.
func TestBodyRatchet_FailsOnUnquotedHeredoc(t *testing.T) {
	dir := copyPromptTree(t)
	appendToTemplate(t, dir, "templates/polecat.md",
		"\n```bash\nmg mail send mayor --from=me --subject=hi --body-file - <<EOF\n"+
			"this $VAR gets eaten\nEOF\n```\n")

	problems, err := checkBodyRatchet(os.DirFS(dir))
	if err != nil {
		t.Fatalf("walking perturbed tree: %v", err)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "UNQUOTED heredoc") {
		t.Fatalf("ratchet must reject a bare <<EOF on a --body-file example, got:\n%s", joined)
	}
}

// TestBodyExampleScanner_ExemptsProseAndComments is the architect's objection
// made executable. A template that TELLS an author never to use --body="..."
// must not be punished by the check that wants exactly that outcome.
func TestBodyExampleScanner_ExemptsProseAndComments(t *testing.T) {
	const doc = "" +
		"Never use `--body=\"...\"` for anything with metacharacters.\n" +
		"Prose that spells --body=\"the hazard\" is describing it, not teaching it.\n" +
		"\n" +
		"```bash\n" +
		"# WRONG: --body=\"...\" lets the shell eat backticks\n" +
		"mg mail send mayor --from=me --subject=s --body-file - <<'EOF'\n" +
		"literal `backticks`, $VARS, 'single' and \"double\" quotes\n" +
		"EOF\n" +
		"```\n"

	if got := scanBodyExamples(doc); len(got) != 0 {
		t.Errorf("prose and comments must be exempt, got %d violation(s): %+v", len(got), got)
	}
}

// TestBodyExampleScanner_CatchesExampleLines is the matching negative: the
// scanner must still see a violation when it is genuinely being taught, in a
// fenced block and in an indented block alike.
func TestBodyExampleScanner_CatchesExampleLines(t *testing.T) {
	const doc = "" +
		"```bash\n" +
		"mg mail send mayor --from=me --subject=s --body=\"fenced example\"\n" +
		"```\n" +
		"\n" +
		"    mg new --type=task --title=t --body=\"indented example\"\n"

	got := scanBodyExamples(doc)
	if len(got) != 2 {
		t.Fatalf("want 2 violations (fenced + indented), got %d: %+v", len(got), got)
	}
	for _, v := range got {
		if v.Kind != "inline-body" {
			t.Errorf("line %d: want kind inline-body, got %q", v.Line, v.Kind)
		}
	}
}

// TestBodyExampleScanner_SingleQuotedBodyIsNotAViolation records the boundary.
// --body='...' reaches argv unmangled — it is safe, just awkward for bodies
// that contain single quotes. Flagging it would be the false positive that
// refuted the metacharacter gate (see cmd/pogo/bodymetachar_test.go, case C).
func TestBodyExampleScanner_SingleQuotedBodyIsNotAViolation(t *testing.T) {
	const doc = "```bash\nmg mail send mayor --from=me --subject=s --body='literal `backticks` here'\n```\n"
	if got := scanBodyExamples(doc); len(got) != 0 {
		t.Errorf("--body='...' is safe and must not be flagged, got: %+v", got)
	}
}
