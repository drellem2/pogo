package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/testsandbox"
)

// mg-a367. The control these tests defend is a PROMPT, so what has to be pinned
// is not "a function returned true" but "the text reached the rendered prompt
// file the worker is started with, above the dispatcher's brief and without
// touching it". The three properties the ticket names are asserted separately
// because they can fail separately: injected-not-composed, additive, and
// not-a-refusal.

// remainderStore builds a store fixture holding one item with the given tags.
func remainderStore(t *testing.T, id, tags string) string {
	t.Helper()
	root := t.TempDir()
	avail := filepath.Join(root, "work", "available")
	if err := os.MkdirAll(avail, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nid: " + id + "\ntype: task\ntags: [" + tags + "]\n---\n# " + id + "\n"
	if err := os.WriteFile(filepath.Join(avail, id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMGRemainderDeclarerReadsTheTag(t *testing.T) {
	cases := []struct {
		name string
		tags string
		want bool
	}{
		{"the tag alone", "declares-remainder", true},
		{"among others", "pogo, dispatch, declares-remainder, polecat-lifecycle", true},
		// mg writes lower-case, but a hand-edited item is a real input and the
		// cost of missing one is the whole defect.
		{"case and space insensitive", "pogo, Declares-Remainder ", true},
		{"no tags at all", "", false},
		{"other tags only", "pogo, dispatch", false},
		// The substring trap: a tag that CONTAINS the name is a different tag.
		{"a tag that merely contains it", "no-declares-remainder-here", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := MGRemainderDeclarer{Root: remainderStore(t, "mg-r001", tc.tags)}
			if got := d.DeclaresRemainder("mg-r001"); got != tc.want {
				t.Errorf("DeclaresRemainder(tags=%q) = %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
}

// The quiet directions. None of them refuses anything — what is lost is a
// paragraph — so all three must answer false rather than panic or block.
func TestMGRemainderDeclarerIsQuietWhenItCannotAnswer(t *testing.T) {
	root := remainderStore(t, "mg-r002", "declares-remainder")
	d := MGRemainderDeclarer{Root: root}

	if d.DeclaresRemainder("") {
		t.Error("an empty work item id must not produce a warning")
	}
	if d.DeclaresRemainder("mg-nope") {
		t.Error("an item absent from the store must not produce a warning")
	}
	if (MGRemainderDeclarer{Root: filepath.Join(root, "does-not-exist")}).DeclaresRemainder("mg-r002") {
		t.Error("an unreadable store must not produce a warning")
	}
}

// The default is FUNCTIONAL, not a no-op. This is the property the whole change
// rests on: the control it replaces failed by depending on a step somebody had
// to remember, and a declarer that engages only once wired is the same defect
// with a different name.
func TestRemainderDeclarerDefaultIsInstalledWithoutWiring(t *testing.T) {
	reg := newDrainTestRegistry(t)
	if _, ok := reg.getRemainderDeclarer().(MGRemainderDeclarer); !ok {
		t.Fatalf("an unwired registry's declarer = %T, want MGRemainderDeclarer", reg.getRemainderDeclarer())
	}
	reg.SetRemainderDeclarer(nil)
	if _, ok := reg.getRemainderDeclarer().(MGRemainderDeclarer); !ok {
		t.Error("SetRemainderDeclarer(nil) disabled the warning; it must restore the default")
	}
}

// The block has to carry the things a worker cannot act without: which item, the
// refusal it will hit, the way out that files a successor, and the way out for
// work that genuinely leaves nothing behind. The last is load-bearing — without
// it the block reads as "invent a successor", which is the one thing `mg done
// --help` says not to do.
func TestRemainderPreludeNamesTheItemAndBothWaysOut(t *testing.T) {
	block := remainderPreludeFor("mg-r003")
	for _, want := range []string{
		"mg-r003",
		DeclaresRemainderTag,
		"mg done mg-r003 --successor=",
		"mg edit mg-r003 --rm-tags=" + DeclaresRemainderTag,
		"declares a remainder",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the injected block is missing %q:\n%s", want, block)
		}
	}
	if !strings.HasSuffix(block, "\n\n") {
		t.Errorf("the block must end in a blank line so it cannot run into the prompt below it, got %q",
			block[len(block)-8:])
	}
}

// THE INSTRUCTION THAT MUST NOT DEGRADE TO "file the successor" (mg-27c0).
//
// On the merge path the worker never runs its own close: it submits and is
// stopped, and pogod closes the item by asking the store which item names it as
// its `predecessor`. `mg new` does not write that edge — measured 2026-08-19,
// a freshly filed item's `predecessor` field is `[]` — so a block that said only
// "file the successor" would produce exactly the state that cost 5 of 5 declared
// items on 2026-08-13: a successor filed, and a close that still refuses.
//
// This asserts the block names the linking edit and says why one command is not
// enough. It is a separate test from the one above because the two fail for
// different reasons: that one catches a block that lost a remedy, this one
// catches a block that kept a remedy and quietly made it insufficient.
func TestRemainderPreludeNamesThePredecessorLink(t *testing.T) {
	block := remainderPreludeFor("mg-r009")
	for _, want := range []string{
		"mg edit <the new id> --add-tags=predecessor:mg-r009",
		"predecessor",
		"mg new",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the injected block is missing %q — without it a worker files a successor "+
				"that pogod cannot find at merge (mg-27c0):\n%s", want, block)
		}
	}
	// And it must say the filing alone is not enough, or the second command
	// reads as optional decoration.
	if !strings.Contains(block, "Both commands, not just the first") {
		t.Errorf("the block does not say the filing alone is insufficient:\n%s", block)
	}
}

// The prelude is prepended AFTER the template pass and must never go through it:
// text that happens to contain template syntax has to arrive literally.
func TestExpandTemplatePreludeIsLiteralAndLeads(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "preluded", "+++\nworktree = false\n+++\nbody for {{.Id}}\n")
	path := filepath.Join(TemplateDir(), "preluded.md")

	out, err := ExpandTemplate(path, TemplateVars{Id: "mg-r004", Prelude: "WARN {{.Id}} {{\n"})
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if !strings.HasPrefix(out, "WARN {{.Id}} {{\n") {
		t.Fatalf("prelude must lead the render, literally; got:\n%s", out)
	}
	if !strings.HasSuffix(out, "body for mg-r004\n") {
		t.Fatalf("the template's own render must follow the prelude unchanged; got:\n%s", out)
	}
}

// A dispatch with no prelude renders byte-identically to one from before this
// existed. Without this, "additive" is an untested claim about the common path.
func TestExpandTemplateWithoutPreludeIsUnchanged(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "plainrender", "+++\nworktree = false\n+++\nbody for {{.Id}}\n")
	path := filepath.Join(TemplateDir(), "plainrender.md")

	out, err := ExpandTemplate(path, TemplateVars{Id: "mg-r005"})
	if err != nil {
		t.Fatalf("ExpandTemplate: %v", err)
	}
	if out != "body for mg-r005\n" {
		t.Fatalf("render with no prelude = %q, want the template's own output verbatim", out)
	}
}

// remainderSpawnRegistry returns a registry that spawns `cat` and reads its
// declarations out of the given store.
func remainderSpawnRegistry(t *testing.T, storeRoot string) *Registry {
	t.Helper()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	reg.SetCommandConfig(catCommandConfig{})
	reg.SetRemainderDeclarer(MGRemainderDeclarer{Root: storeRoot})
	return reg
}

// THE HEADLINE TEST. It goes all the way to the file the worker's harness is
// started with, because every layer short of that has been "wired" before and
// silently done nothing.
//
// It also asserts the two properties the ticket calls out by name:
//
//   - INJECTED, NOT COMPOSED — the dispatch below passes a Body with no warning
//     in it, exactly as the coordinator's forgotten dispatches did, and the
//     warning is there anyway.
//   - ADDITIVE — the dispatcher's brief survives VERBATIM and the warning is
//     ABOVE it. A brief frequently carries load-bearing rescue instructions, so
//     a "fix" that rewrote or reordered it would be a worse bug than the one
//     being fixed.
func TestSpawnPolecatPrependsTheRemainderWarning(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "remwarn", "+++\nworktree = false\n+++\n# Polecat\n\nDetails:\n\n{{.Body}}\n")
	reg := remainderSpawnRegistry(t, remainderStore(t, "mg-r006", "pogo, declares-remainder"))

	const brief = "REBASE BY HAND onto main; do not force-push."
	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-remwarn", Template: "remwarn", Id: "mg-r006", Body: brief,
	})

	raw, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatalf("read expanded prompt %s: %v", a.PromptFile, err)
	}
	prompt := string(raw)

	if !strings.Contains(prompt, "mg-r006 --successor=") {
		t.Fatalf("the rendered prompt carries no successor warning — this is the mg-a367 defect, "+
			"the dispatcher's brief said nothing and neither did pogod:\n%s", prompt)
	}
	if !strings.Contains(prompt, brief) {
		t.Fatalf("the dispatcher's brief did not survive into the prompt:\n%s", prompt)
	}
	warnAt := strings.Index(prompt, "DECLARES A REMAINDER")
	briefAt := strings.Index(prompt, brief)
	headingAt := strings.Index(prompt, "# Polecat")
	if warnAt < 0 || warnAt > headingAt || warnAt > briefAt {
		t.Errorf("the warning must PRECEDE the template body and the brief (warn=%d heading=%d brief=%d):\n%s",
			warnAt, headingAt, briefAt, prompt)
	}
	if headingAt > briefAt {
		t.Errorf("the template's own ordering was disturbed (heading=%d brief=%d)", headingAt, briefAt)
	}
}

// The negative control, and the one that keeps the block from becoming noise: an
// item WITHOUT the tag gets a prompt byte-identical to the template's own render.
func TestSpawnPolecatWithoutTheTagRendersPromptUnchanged(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "remquiet", "+++\nworktree = false\n+++\n# Polecat\n\n{{.Body}}\n")
	reg := remainderSpawnRegistry(t, remainderStore(t, "mg-r007", "pogo, dispatch"))

	a := spawnPolecatViaAPI(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-remquiet", Template: "remquiet", Id: "mg-r007", Body: "ordinary brief",
	})
	raw, err := os.ReadFile(a.PromptFile)
	if err != nil {
		t.Fatalf("read expanded prompt %s: %v", a.PromptFile, err)
	}
	if got, want := string(raw), "# Polecat\n\nordinary brief\n"; got != want {
		t.Errorf("an undeclared item's prompt = %q, want %q — the warning leaked onto an item "+
			"that does not carry the tag", got, want)
	}
}

// NOT A REFUSAL. The ticket says so in as many words: the tag means "this work
// has a known remainder", not "this is unsafe to start", and blocking it would
// be wrong. This is the assertion that stops a later reader from promoting the
// warning into a sixth dispatch gate.
func TestRemainderWarningDoesNotRefuseTheDispatch(t *testing.T) {
	testsandbox.Isolate(t)
	writeTemplate(t, "remok", "+++\nworktree = false\n+++\nbody\n")
	reg := remainderSpawnRegistry(t, remainderStore(t, "mg-r008", "declares-remainder"))

	rr := spawnPolecatStatus(t, reg, SpawnPolecatAPIRequest{
		Name: "pc-remok", Template: "remok", Id: "mg-r008",
	})
	if rr.Code != 201 {
		t.Fatalf("spawn on a declares-remainder item: status = %d, want 201 — the warning must "+
			"not refuse the dispatch; body=%s", rr.Code, rr.Body.String())
	}
}
