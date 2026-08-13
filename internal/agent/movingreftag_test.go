package agent

// A ratchet on `git tag` targets in the shipped prompts, and the content pins
// for the three prose/command splits mg-7537 swept out.
//
// THE DEFECT THIS GUARDS IS NOT A WRONG LINE. pm/pm-template.md's release-cut
// body read, inside one instruction:
//
//	Step 2 ... after the merge LANDS, tag the merged sha:
//	  git fetch origin main && git tag -a vX.Y.Z -m "Release vX.Y.Z" origin/main
//
// The prose says "the merged sha". The command says `origin/main`. Those are the
// same commit only while main has not advanced past the merge — a race, not an
// invariant, since another worker in the same repo can merge in the window. At
// v0.4.0 main was four commits past the smoke-tested prep commit, so the command
// would have published commits that were never in the release that was tested.
//
// NEITHER HALF IS WRONG ON ITS OWN, which is why no line-by-line review catches
// it: whoever reads the prose is right, whoever reads the command is right, and
// they are equally diligent. At the v0.10.0 cut the coordinator read the prose,
// checked origin/main against the refinery's merged sha, and tagged correctly —
// so the instruction's defect cost nothing that night and was invisible to every
// artifact the night produced. A grep for wrong commands does not find these.
//
// SO THE RATCHET IS KEYED ON THE SHAPE, NOT ON THE STRING. Any `git tag` in a
// shipped prompt whose object is a MOVING ref — origin/main, main, HEAD, or an
// omitted object, which is HEAD — is refused. Every legitimate release tag
// target is a recorded sha (the refinery's merged_sha) or is created by the
// refinery itself via `--post-merge-tag`, which writes no sha into any command
// and therefore has nothing to disagree with.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// movingRefs are the operands a `git tag` may not name. Each resolves to
// whatever a branch tip happens to be at the moment the command runs, so a tag
// created against one records "wherever we were" rather than "the commit we
// tested and merged".
var movingRefs = map[string]string{
	"origin/main":   "the remote branch tip — equals the merged sha only until main advances",
	"origin/master": "the remote branch tip — equals the merged sha only until master advances",
	"origin/HEAD":   "the remote's default-branch tip",
	"main":          "a local branch tip, and in a shared worktree not even necessarily current",
	"master":        "a local branch tip, and in a shared worktree not even necessarily current",
	"HEAD":          "whatever is checked out right now",
	"@":             "HEAD by another name",
}

// tagRemedy is the alternative every failure message carries. An author who
// trips this must not have to go looking for what to do instead.
const tagRemedy = "Tag the sha the refinery recorded merging, or do not name a commit at all:\n" +
	"    pogo refinery submit <branch> --repo=<repo> --post-merge-tag=vX.Y.Z\n" +
	"  writes no sha into any command, so the command cannot name a different\n" +
	"  commit from the one that merged. By hand:\n" +
	"    MERGED=$(pogo refinery show <mr-id> --json | jq -r .merged_sha)\n" +
	"    git tag -a vX.Y.Z -m \"Release vX.Y.Z\" \"$MERGED\"\n" +
	"  The same value is the \"Merged SHA:\" line of the MERGED mail and\n" +
	"  .result.merged_sha under `mg sidecar <item> --json` (NOT .merged_sha —\n" +
	"  that key is one level up and renders as null at exit 0)."

type tagUse struct {
	Line   int
	Object string // "" when the invocation names no object at all
	Text   string
}

// splitArgs splits a command line on whitespace, keeping double-quoted runs
// together. Good enough for the shapes a prompt teaches; a `git tag` line with
// shell substitutions in its object is reported by its raw text either way,
// since "$MERGED" and $(…) are not in movingRefs.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// takesValue names the `git tag` flags whose NEXT argument is a value and not
// the tagname. Without this, `-m "Release vX.Y.Z"` would be read as the tagname
// and `origin/main` as merely another operand — the scanner would still flag it,
// but it would name the wrong position and the message would mislead.
var takesValue = map[string]bool{
	"-m": true, "--message": true,
	"-F": true, "--file": true,
	"-u": true, "--local-user": true,
	"--cleanup": true,
	"--sort":    true,
	"--format":  true,
}

// scanTagUses reports every `git tag` invocation that CREATES a tag, with the
// object it names. Read-only forms (`git tag -l`, `--list`, `-d`, `-v`, and a
// bare `git tag`) create nothing and are skipped: they carry no claim about
// which commit a release is.
//
// The source is scanned after `\n` escapes are expanded, so a command living
// inside a printf format string — which is how pm-template.md composes the
// release-cut ticket body — is seen as the line it will be rendered as rather
// than as one 3000-character line. That expansion is the whole reason this
// scanner exists as its own pass instead of reusing the example-line predicate
// in bodyratchet_test.go: the defect it is aimed at was inside a printf.
func scanTagUses(src string) []tagUse {
	// Track the real line number across the \n expansion: an escape inside a
	// printf belongs to the physical line that holds it.
	var out []tagUse
	for i, physical := range strings.Split(src, "\n") {
		for _, logical := range strings.Split(physical, `\n`) {
			idx := strings.Index(logical, "git tag")
			if idx < 0 {
				continue
			}
			rest := logical[idx+len("git tag"):]
			// Stop at a shell operator or a comment: what follows belongs to
			// another command, not to this tag's operands.
			for _, stop := range []string{"&&", "||", ";", "|", "#"} {
				if j := strings.Index(rest, stop); j >= 0 {
					rest = rest[:j]
				}
			}
			args := splitArgs(rest)

			var operands []string
			skipNext := false
			readOnly := false
			for _, a := range args {
				if skipNext {
					skipNext = false
					continue
				}
				if strings.HasPrefix(a, "-") {
					if takesValue[a] {
						skipNext = true
					}
					switch a {
					case "-l", "--list", "-d", "--delete", "-v", "--verify", "-n":
						readOnly = true
					}
					// `--flag=value` carries its value inline; nothing to skip.
					continue
				}
				operands = append(operands, a)
			}
			if readOnly || len(operands) == 0 {
				// A bare `git tag` lists; it creates nothing.
				continue
			}
			object := ""
			if len(operands) > 1 {
				object = operands[1]
			}
			out = append(out, tagUse{Line: i + 1, Object: object, Text: strings.TrimSpace(logical)})
		}
	}
	return out
}

// checkTagTargets walks a prompt tree and returns one problem per `git tag`
// example that names a moving ref, or names nothing (which is HEAD).
func checkTagTargets(root fs.FS) ([]string, error) {
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
		for _, u := range scanTagUses(string(data)) {
			switch {
			case u.Object == "":
				problems = append(problems, fmt.Sprintf(
					"%s:%d creates a tag with NO object, which is HEAD — %q\n  %s",
					path, u.Line, u.Text, tagRemedy))
			case movingRefs[u.Object] != "":
				problems = append(problems, fmt.Sprintf(
					"%s:%d tags the moving ref %q (%s) — %q\n  %s",
					path, u.Line, u.Object, movingRefs[u.Object], u.Text, tagRemedy))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(problems)
	return problems, nil
}

// TestShippedPromptsNeverTagAMovingRef is the standing guard. There are zero
// violations today and there is no grandfathering inventory: unlike the
// --body="..." ratchet there was never a population to sweep down, only the one
// instruction, and a tag on the wrong commit cannot be unpublished.
func TestShippedPromptsNeverTagAMovingRef(t *testing.T) {
	problems, err := checkTagTargets(promptTreeFS(t))
	if err != nil {
		t.Fatalf("walking prompts: %v", err)
	}
	for _, p := range problems {
		t.Errorf("%s", p)
	}
}

// TestTagScannerFiresOnTheOriginalDefect is the refutation control: the scanner
// must flag the exact line that shipped, and must not flag the line that
// replaced it. A guard nobody has watched fail is a guard in name only, and this
// one's whole claim is that it would have caught mg-7537.
func TestTagScannerFiresOnTheOriginalDefect(t *testing.T) {
	const shipped = "  git fetch origin main && git tag -a vX.Y.Z -m \"Release vX.Y.Z\" origin/main && git push origin vX.Y.Z"
	uses := scanTagUses(shipped)
	if len(uses) != 1 {
		t.Fatalf("scanner found %d tag uses in the shipped line, want 1: %+v", len(uses), uses)
	}
	if uses[0].Object != "origin/main" {
		t.Errorf("object = %q, want %q — the -m value must not be read as the tagname", uses[0].Object, "origin/main")
	}

	// The same line as a \n-escaped fragment of a printf format string, which is
	// where it actually lived. A scanner that only works on physical lines does
	// not cover the file this ticket is about.
	const inPrintf = `printf 'Step 2: tag the merged sha:\n  git tag -a vX.Y.Z -m "Release vX.Y.Z" origin/main\nThen confirm.\n'`
	if got := scanTagUses(inPrintf); len(got) != 1 || got[0].Object != "origin/main" {
		t.Errorf("scanner missed the tag inside a printf body: %+v", got)
	}

	// The replacement must be clean.
	const fixed = `  git fetch origin main && git tag -a vX.Y.Z -m "Release vX.Y.Z" "$MERGED" && git push origin vX.Y.Z`
	for _, u := range scanTagUses(fixed) {
		if u.Object == "" || movingRefs[u.Object] != "" {
			t.Errorf("the fixed line is flagged (object=%q); the scanner rejects its own remedy", u.Object)
		}
	}

	// A no-object create is HEAD and must be caught.
	if got := scanTagUses(`git tag -a v1.2.3 -m "Release v1.2.3"`); len(got) != 1 || got[0].Object != "" {
		t.Errorf("no-object create not reported as such: %+v", got)
	}

	// Read-only forms create nothing and must not be flagged. `git tag -l` in
	// particular is prescribed in this corpus as the thing NOT to trust, and
	// punishing that prose is how a check trains its readers to route around it.
	for _, ro := range []string{"git tag -l", "git tag --list 'v*'", "git tag -d v1.2.3", "git tag"} {
		if got := scanTagUses(ro); len(got) != 0 {
			t.Errorf("read-only %q flagged: %+v", ro, got)
		}
	}
}

// TestTagRatchetFiresOnTheREALCorpus re-introduces the defect into a copy of the
// shipped tree and requires the ratchet to name it. The control above works on
// hand-written strings, which only shows the scanner can parse a line someone
// wrote for it; this one shows it fires on the file it guards, at the position
// it actually occupied — inside a printf format string, three thousand
// characters into one physical line.
func TestTagRatchetFiresOnTheREALCorpus(t *testing.T) {
	dir := copyPromptTree(t)
	rel := filepath.Join("pm", "pm-template.md")
	path := filepath.Join(dir, rel)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	const fixed = `git tag -a vX.Y.Z -m "Release vX.Y.Z" "$MERGED"`
	const shipped = `git tag -a vX.Y.Z -m "Release vX.Y.Z" origin/main`
	if !strings.Contains(string(data), fixed) {
		t.Fatalf("%s no longer holds the fixed tag command; this control is testing nothing", rel)
	}
	reverted := strings.Replace(string(data), fixed, shipped, 1)
	if err := os.WriteFile(path, []byte(reverted), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}

	problems, err := checkTagTargets(os.DirFS(dir))
	if err != nil {
		t.Fatalf("walking perturbed tree: %v", err)
	}
	if len(problems) == 0 {
		t.Fatal("the ratchet passed a tree carrying the exact command mg-7537 removed")
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "origin/main") || !strings.Contains(joined, "pm-template.md") {
		t.Errorf("the failure does not name the ref or the file:\n%s", joined)
	}
	// The remedy has to travel with the finding. A ratchet that says "no" and
	// not "instead" gets routed around.
	if !strings.Contains(joined, "--post-merge-tag") {
		t.Errorf("the failure message carries no remedy:\n%s", joined)
	}
}

// TestReleaseCutBodyTagsTheMergedSha pins what the release-cut ticket body
// actually says. The ratchet above proves no command names a moving ref; these
// assertions are the other half — that the body still tells its reader where the
// right sha comes from, and which acceptance check catches which failure.
func TestReleaseCutBodyTagsTheMergedSha(t *testing.T) {
	body := pmTemplateBody(t)

	for _, want := range []string{
		// The refinery is the preferred actor: it is the only one that both
		// sees the merged SHA and outlives the author.
		"--post-merge-tag=vX.Y.Z",
		// The by-hand fallback reads the sha from a record of what merged.
		`MERGED=$(pogo refinery show <mr-id> --json | jq -r .merged_sha)`,
		// The sidecar path is one level down. `.merged_sha` renders null at
		// exit 0 and would silently tag nothing.
		".result.merged_sha",
		// The prohibition, stated where the command is.
		"DO NOT tag origin/main",
		// Both acceptance checks, each labelled with the failure it catches —
		// the whole point of mg-7537's second ask.
		"# DRIFT",
		"# DANGLE",
		"BLIND TO DRIFT",
		// And the two that say whether anything was published at all.
		"# PUSHED",
		"# FIRED",
		// The quoting of ^{} is load-bearing under zsh, so it is stated.
		`git rev-parse "vX.Y.Z^{}"`,
		// The acceptance block has to stand on its own. On the
		// --post-merge-tag path nothing above it fetched the tag or set
		// MERGED, and an unfetched tag makes rev-parse print nothing — which
		// the DRIFT check would then report as drift. That is a check whose
		// answer depends on state its own instruction never established: this
		// section's defect, one level down, inside the acceptance criteria.
		`git fetch --tags origin main`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pm/pm-template.md: release-cut body is missing %q", want)
		}
	}

	// The printf format is SINGLE-quoted, so a double quote inside it needs no
	// escape — and must not have one. zsh's printf leaves `\"` in the output
	// verbatim while bash's strips it, so an escaped quote here emits a
	// DIFFERENT ticket body depending on which shell the PM's sweep ran under.
	// The version of this fix that first shipped had exactly that, on the
	// acceptance line: `git rev-parse \"vX.Y.Z^{}\"` reaches git as a ref name
	// with literal backslashes under zsh, which is the fleet's shell.
	line := releaseCutPrintf(t, body)
	if strings.Contains(line, `\"`) {
		t.Errorf(`pm/pm-template.md: the release-cut printf format contains \" — inside a single-quoted format that escape is unnecessary, and zsh keeps it while bash removes it, so the emitted body differs by shell`)
	}

	// The exact command that shipped must not come back.
	const banned = `git tag -a vX.Y.Z -m "Release vX.Y.Z" origin/main`
	if strings.Contains(body, banned) {
		t.Errorf("pm/pm-template.md: %q is back — the prose says the merged sha and this command does not.\n  %s", banned, tagRemedy)
	}
}

// TestMayorDerivesTheLogPathItWasToldToDerive covers the second instance the
// mg-7537 sweep found, in a different file and about a different subject. The
// prose says a literal log path is the claim that rots and to ask the service
// manager instead; the command underneath it grepped
// ~/Library/Logs/pogo/pogod.log anyway. Same shape: two halves, each correct
// today, disagreeing the moment anyone moves the log.
func TestMayorDerivesTheLogPathItWasToldToDerive(t *testing.T) {
	body := mayorPromptBody(t)

	for _, want := range []string{
		`log=$(grep -A1 StandardOutPath "$plist"`,
		`grep refinery: "$log" | grep <mr-id>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("prompts/mayor.md: expected %q — the refinery-log recipe must read the path it derived, not one spelled out here", want)
		}
	}
	const banned = "grep refinery: ~/Library/Logs/pogo/pogod.log"
	if strings.Contains(body, banned) {
		t.Errorf("prompts/mayor.md: %q is back, one line under prose saying not to assume a path", banned)
	}
}

// TestStageTransitionRewriteIsGuarded covers the fourth instance. The carrier
// `stage:` bullet said to update it with `mg edit <id> --body="..."` and, in the
// same sentence, to "preserve the rest of the body when rewriting" — an
// obligation that command cannot discharge, since --body replaces the whole
// thing and getting it wrong is silent at exit 0 (the mg-f326 shape). An append
// genuinely cannot serve here: the stage line lives INSIDE the leading carrier
// block. So the fix is the guarded rewrite, not a different verb.
func TestStageTransitionRewriteIsGuarded(t *testing.T) {
	body := mayorPromptBody(t)

	const anchor = "`stage:` is the state-machine position"
	i := strings.Index(body, anchor)
	if i < 0 {
		t.Fatalf("prompts/mayor.md: carrier stage bullet not found (missing %q)", anchor)
	}
	// Bound the read to this bullet: the file prescribes --if-unchanged
	// elsewhere too, and matching that would pass this test on the wrong text.
	section := body[i:]
	if j := strings.Index(section, "- `gh:` ties a ticket to its issue"); j > 0 {
		section = section[:j]
	}

	for _, want := range []string{
		`HASH=$(mg show <id> --body-hash)`,
		`mg edit <id> --if-unchanged="$HASH" --body-file`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("prompts/mayor.md stage bullet: expected %q — a rewrite that must preserve the rest of the body has to be guarded, not merely asked to be careful", want)
		}
	}
	// The ban is on PRESCRIBING it, not on naming it. The bullet quotes the old
	// command verbatim while explaining why it went, and a predicate that could
	// not tell those apart would punish the prose that does the fixing — the
	// same boundary bodyRatchet draws between example lines and prose.
	const banned = "Update it with `mg edit <id> --body="
	if strings.Contains(section, banned) {
		t.Errorf("prompts/mayor.md stage bullet: %q is back; it clobbers unconditionally and exits 0", banned)
	}
}

// TestQACheckoutNamesOrigin covers the third instance. `git checkout
// <source-branch>` resolves to a LOCAL branch of that name when one exists, and
// a polecat worktree shares its .git with the source repo, so a stale local
// branch is reachable. The prose promises "the branch that contains the
// implementation you are verifying" and the command can hand you a different
// commit, silently.
func TestQACheckoutNamesOrigin(t *testing.T) {
	body := promptFile(t, "prompts/templates/polecat-qa.md")

	for _, want := range []string{
		`git checkout -B <source-branch> --no-track "origin/<source-branch>"`,
		"git log --oneline origin/main..<source-branch>",
		"git diff origin/main...<source-branch>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("polecat-qa.md: expected %q — the checkout and the diff base must name origin", want)
		}
	}
	for _, banned := range []string{
		"\ngit checkout <source-branch>\n",
		"git log --oneline main..<source-branch>",
		"git diff main...<source-branch>",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("polecat-qa.md: %q is back; it can resolve to a commit that is not the implementation", banned)
		}
	}
}

// releaseCutPrintf returns the single physical line holding the release-cut
// ticket body's printf format. The body is composed with printf rather than a
// heredoc because it interpolates $tag/$days/$ahead, and that is what puts the
// whole instruction on one line where shell-escaping rules apply to it.
func releaseCutPrintf(t *testing.T, body string) string {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "printf 'Latest release") {
			return l
		}
	}
	t.Fatalf("pm/pm-template.md: release-cut printf not found")
	return ""
}

// promptFile returns one embedded prompt — the artifact that SHIPS, not the
// installed copy under ~/.pogo/agents, which a local edit can make disagree.
func promptFile(t *testing.T, name string) string {
	t.Helper()
	data, err := defaultPrompts.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded %s: %v", name, err)
	}
	return string(data)
}

func pmTemplateBody(t *testing.T) string {
	t.Helper()
	return promptFile(t, "prompts/pm/pm-template.md")
}

// promptTreeFS is the embedded tree rooted at prompts/, so walked paths read as
// `pm/pm-template.md` rather than `prompts/pm/pm-template.md`.
func promptTreeFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(defaultPrompts, "prompts")
	if err != nil {
		t.Fatalf("sub prompts: %v", err)
	}
	return sub
}
