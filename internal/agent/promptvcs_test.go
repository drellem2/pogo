package agent

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs slice 11 (docs/design/enth-vcs.md §12, mg-7c936): TemplateVars.VCS
// and the enth blocks in the six polecat templates.

// embeddedPolecatTemplates is every shipped polecat template, by name.
func embeddedPolecatTemplates(t *testing.T) map[string]string {
	t.Helper()
	fsys := DefaultPromptsFS()
	names, err := fs.Glob(fsys, "templates/*.md")
	if err != nil || len(names) == 0 {
		t.Fatalf("no embedded templates: %v", err)
	}
	out := map[string]string{}
	for _, n := range names {
		raw, err := fs.ReadFile(fsys, n)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(n)] = templateBody(t, raw)
	}
	return out
}

// templateBody strips the frontmatter exactly as the spawn path does.
func templateBody(t *testing.T, raw []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.md")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, body, err := ParsePromptFrontmatter(p)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// varMatrix covers every field the templates branch on today, so a git
// render is compared in each of the shapes a dispatch can take.
func varMatrix() map[string]TemplateVars {
	out := map[string]TemplateVars{}
	for _, br := range []string{"", "feat/x"} {
		for _, nw := range []bool{false, true} {
			for _, sme := range []string{"", "pm-x"} {
				for _, cores := range []int{0, 2} {
					for _, rs := range []string{"", "mg reclaim mg-1"} {
						for _, prov := range []string{"claude", "codex"} {
							k := fmt.Sprintf("branch=%q noworktree=%v sme=%q cores=%d restamp=%q provider=%s", br, nw, sme, cores, rs, prov)
							out[k] = TemplateVars{Task: "T", Body: "B", Id: "mg-1", Repo: "/r", Branch: br, WorktreeDir: "/w",
								NoWorktree: nw, SME: sme, WorkerCores: cores, HostCores: 10, ClaimRestampCmd: rs,
								Coordinator: "mayor", Worker: "polecat", Provider: prov}
						}
					}
				}
			}
		}
	}
	return out
}

func render(t *testing.T, name, body string, v TemplateVars) string {
	t.Helper()
	tm, err := template.New(name).Parse(body)
	if err != nil {
		t.Fatalf("%s: parse: %v", name, err)
	}
	var b bytes.Buffer
	if err := tm.Execute(&b, withDefaults(v)); err != nil {
		t.Fatalf("%s: execute: %v", name, err)
	}
	return b.String()
}

// Every shipped polecat template branches on .VCS — otherwise the spawn
// refusal below would refuse it for every enth repo.
func TestEveryEmbeddedTemplateBranchesOnVCS(t *testing.T) {
	tmpls := embeddedPolecatTemplates(t)
	for _, want := range []string{"polecat.md", "polecat-architect.md", "polecat-review.md", "polecat-qa.md", "polecat-triage.md", "polecat-build-pr.md"} {
		if _, ok := tmpls[want]; !ok {
			t.Errorf("control: %s is not among the embedded templates", want)
		}
	}
	for name, body := range tmpls {
		ok, err := TemplateBranchesOnVCS(body)
		if err != nil || !ok {
			t.Errorf("%s: TemplateBranchesOnVCS = %v, %v; want true", name, ok, err)
		}
	}
}

// An unset VCS is git: the render is the same as an explicit "git", in every
// shape of the matrix. (The byte-identity against the templates as they were
// before the enth blocks is TestGitRenderUnchangedSinceBase.)
func TestEmptyVCSRendersAsGit(t *testing.T) {
	for name, body := range embeddedPolecatTemplates(t) {
		for k, v := range varMatrix() {
			empty := render(t, name, body, v)
			v.VCS = TemplateVCSGit
			if git := render(t, name, body, v); git != empty {
				t.Fatalf("%s [%s]: VCS=\"\" and VCS=\"git\" render differently", name, k)
			}
			v.VCS = TemplateVCSEnth
			if enth := render(t, name, body, v); enth == empty {
				t.Errorf("control: %s [%s] renders the same for enth as for git", name, k)
			}
		}
	}
}

// An enth render carries the enth protocol and none of git's. The forbidden
// strings are the git steps that fail, or mislead, in a directory with no
// .git; each appears in at least one git render (checked first, so a renamed
// command cannot make this pass by matching nothing).
func TestEnthRenderHasNoGitProtocol(t *testing.T) {
	forbidden := []string{
		"BRANCH=$(git rev-parse", "git push origin", "git commit -m", "git add <files>",
		"pogo refinery submit \"$BRANCH\"", "git checkout -B", "git fetch origin",
		"gh pr comment", "git diff origin/main", "git diff main...", "is a git worktree",
	}
	tmpls := embeddedPolecatTemplates(t)
	seen := map[string]bool{}
	for name, body := range tmpls {
		for _, v := range varMatrix() {
			git := render(t, name, body, v)
			for _, f := range forbidden {
				if strings.Contains(git, f) {
					seen[f] = true
				}
			}
			v.VCS, v.EnthRemote, v.EnthRepo = TemplateVCSEnth, "https://enth.example", "o/r"
			enth := render(t, name, body, v)
			for _, f := range forbidden {
				if strings.Contains(enth, f) {
					t.Errorf("%s (branch=%q): the enth render still says %q", name, v.Branch, f)
				}
			}
			if !strings.Contains(enth, "o/r") {
				t.Errorf("%s: the enth render does not name the enth repo", name)
			}
		}
	}
	for _, f := range forbidden {
		if !seen[f] {
			t.Errorf("control: %q appears in no git render, so its absence from enth proves nothing", f)
		}
	}
}

// The load-bearing commands of the build and architect protocols (§12, P2–P5)
// are rendered whole, with the item filled in, so a worker copies a complete
// command rather than composing one (§5, guard 2).
func TestEnthRenderCarriesTheEnthProtocol(t *testing.T) {
	tmpls := embeddedPolecatTemplates(t)
	for _, name := range []string{"polecat.md", "polecat-architect.md"} {
		for _, br := range []string{"", "release-1"} {
			v := TemplateVars{Task: "T", Id: "mg-9", Repo: "/r", WorktreeDir: "/w", Branch: br,
				VCS: TemplateVCSEnth, EnthRemote: "https://e", EnthRepo: "o/r"}
			out := render(t, name, tmpls[name], v)
			target := "main"
			if br != "" {
				target = br
			}
			ref, tgt, gref := "", "", ""
			if br != "" {
				ref, tgt, gref = "--ref refs/heads/"+br+" ", " --target="+br, "--ref=refs/heads/"+br+" "
			}
			for _, want := range []string{
				`--trailer Pogo-Item=mg-9 --trailer Pogo-Worker="$POGO_AGENT_NAME" /w`,
				// The self-land of enth-vcs §6 (mg-5b904), whole.
				`ids=$(pogo presubmit --item="$item" --print-stack) || return 1`,
				// Arrays, so zsh passes one argv entry per id and per flag (mg-c07a5).
				// --print-stack prints ONE line, space-separated: split on any whitespace (mg-88ece).
				`stack=(); while read -r id; do [ -n "$id" ] && stack+=("$id"); done <<< "$(printf '%s' "$ids" | tr -s '[:space:]' '\n')"`,
				`lreq=(); [ -n "$req" ] && lreq=(--require "$job")`,
				// The gate is the repo's declared one (mg-c07a5).
				`gate=$(pogo refinery gate --repo=/r --worktree="$PWD")`,
				`echo "GATE COMMAND: $gate"`,
				// The land is gated on the merged tree (mg-64f2e): the gate, then a
				// land bound to the tree it passed.
				`enth-land-gate --job="$job" --gate="$gate" ` + gref + `"${stack[@]}"`,
				`enth land ` + ref + `"${lreq[@]}" "${stack[@]}" --request-id "$item/$n"`,
				`lreq=(); echo "TEST-ONLY (mg-64f2e): tree $t1 passed, unattested (no attest token) — landing without --require" ;;`,
				`{ echo "GATE NOT INSTALLED — landing untested (mg-64f2e)"; req=; }`,
				`job=ci/selfland/build`,
				`unattested_tree|stale_tip) rm -f "$st.pending"; n=$((n+1));`,
				`pogo land --item="$item" --repo=/r` + tgt + ` --landed-file="$st.out"`,
				`--verdict-file=/tmp/mg-9-verdict.json "$@"; rc=$?`,
				`[ "$rc" -eq 0 ] && return 0`,
				// Slice 12 (mg-ae4ac): self_land --post-merge-tag=… reaches pogo
				// land, and its exit 6 (recorded, tag not made) is not "NOT RECORDED".
				`[ "$rc" -eq 6 ] && { echo "LANDED and RECORDED, but the tag was NOT created`,
				"   self_land\n",
				`--trailer Pogo-Supersedes="$OLD"`,
				"enth checkout " + target + ` "$REMAKE"`,
				`--result="{\"item\": \"mg-9\"`,
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%s (branch=%q): enth render lacks %q", name, br, want)
				}
			}
			// The refinery does not land enth work: no enth render submits to it.
			for _, no := range []string{"pogo refinery submit", "Poll the refinery", "pogo refinery show <id> --json | jq -r .status"} {
				if strings.Contains(out, no) {
					t.Errorf("%s (branch=%q): enth render still says %q", name, br, no)
				}
			}
			// No PR flow on enth (§11): the PR steps are git's alone.
			for _, no := range []string{"gh pr create", "gh repo view"} {
				if strings.Contains(out, no) {
					t.Errorf("%s (branch=%q): enth render carries the PR-flow step %q", name, br, no)
				}
			}
		}
	}
	for _, name := range []string{"polecat-review.md", "polecat-qa.md"} {
		out := render(t, name, tmpls[name], TemplateVars{Id: "mg-9", WorktreeDir: "/w", VCS: TemplateVCSEnth, EnthRepo: "o/r"})
		for _, want := range []string{"enth changes --unlanded --trailer Pogo-Item=\"$SRC\"", "enth checkout \"$TOP\""} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: enth render lacks %q", name, want)
			}
		}
	}
	if out := render(t, "polecat-build-pr.md", tmpls["polecat-build-pr.md"], TemplateVars{Id: "mg-9", VCS: TemplateVCSEnth, EnthRepo: "o/r"}); !strings.Contains(out, "enth repos have no pull-request track") {
		t.Error("polecat-build-pr.md: the enth render does not stop the worker")
	}
}

// The self_land function is one function: the architect's is the build
// protocol's, re-indented, so a fix to one cannot silently miss the other.
func TestSelfLandIsTheSameInBothTemplates(t *testing.T) {
	tmpls := embeddedPolecatTemplates(t)
	fn := func(name string) string {
		body := tmpls[name]
		i := strings.Index(body, "self_land() {")
		j := strings.Index(body[i:], "\n")
		if i < 0 || j < 0 {
			t.Fatalf("%s: no self_land function", name)
		}
		end := strings.Index(body[i:], "\n   }\n")
		if name == "polecat-architect.md" {
			end = strings.Index(body[i:], "\n     }\n")
		}
		if end < 0 {
			t.Fatalf("%s: self_land has no closing brace", name)
		}
		var out []string
		for _, l := range strings.Split(body[i:i+end], "\n") {
			out = append(out, strings.TrimSpace(l))
		}
		return strings.Join(out, "\n")
	}
	if a, b := fn("polecat.md"), fn("polecat-architect.md"); a != b {
		t.Errorf("self_land differs between polecat.md and polecat-architect.md:\n%s\n---\n%s", a, b)
	}
}

// The stack listing the review and QA blocks hand the worker picks the top of
// one stack, and prints several ids for several stacks (which the prose tells
// the worker to stop on). Run with jq on the listing's real field names.
func TestEnthStackTopJq(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("no jq")
	}
	body := embeddedPolecatTemplates(t)["polecat-qa.md"]
	i := strings.Index(body, "TOP=$(jq -rs '")
	if i < 0 {
		t.Fatal("control: the QA template has no TOP=$(jq ...) line")
	}
	line := body[i:]
	line = line[:strings.Index(line, "\n")]
	prog := strings.TrimPrefix(line, "TOP=$(jq -rs '")
	prog = prog[:strings.Index(prog, "'")]
	run := func(listing string) string {
		c := exec.Command("jq", "-rs", prog)
		c.Stdin = strings.NewReader(listing)
		out, err := c.Output()
		if err != nil {
			t.Fatalf("jq %q: %v", prog, err)
		}
		return strings.TrimSpace(string(out))
	}
	stack := `{"type":"change","id":"a","base":"t","landed":null}
{"type":"change","id":"b","base":"a","landed":null}
{"type":"change","id":"c","base":"b","landed":null}
`
	if got := run(stack); got != "c" {
		t.Errorf("one stack: top = %q, want c", got)
	}
	if got := run(`{"type":"change","id":"a","base":"t"}` + "\n" + `{"type":"change","id":"b","base":"t"}` + "\n"); got != "a\nb" {
		t.Errorf("two stacks: %q, want both tops", got)
	}
	if got := run(""); got != "" {
		t.Errorf("no Changes: %q, want nothing", got)
	}
}

func TestTemplateBranchesOnVCS(t *testing.T) {
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{{if eq .VCS "enth"}}a{{else}}b{{end}}`, true},
		{`x {{.VCS}} y`, true},
		{`{{if .Branch}}{{if eq .VCS "enth"}}e{{end}}{{end}}`, true},
		{`{{with .Id}}{{$.VCS}}{{end}}`, true},
		{`plain text naming .VCS in prose`, false},
		{`task {{.Id}} {{if .Branch}}b{{end}}`, false},
	} {
		got, err := TemplateBranchesOnVCS(c.body)
		if err != nil || got != c.want {
			t.Errorf("TemplateBranchesOnVCS(%q) = %v, %v; want %v", c.body, got, err, c.want)
		}
	}
	if _, err := TemplateBranchesOnVCS(`{{if}}`); err == nil {
		t.Error("an unparseable template was not an error")
	}
}

// The spawn refusal (§12): an enth repo with a template that never references
// .VCS is refused through the real handler, before any side effect; the same
// template on a git repo is not refused by it (the control).
func TestSpawnRefusesAnEnthRepoWhoseTemplateDoesNotBranch(t *testing.T) {
	installPolecatTemplate(t) // "task {{.Id}}": no .VCS
	enthRepo, gitRepo := t.TempDir(), t.TempDir()
	t.Cleanup(func() { SetRepoVCS(nil) })
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == enthRepo {
			return vcs.Enth, &vcs.EnthRepo{Remote: "https://e.invalid", Repo: "o/r"}
		}
		return vcs.Git, nil
	})

	rr := spawnPolecat(t, newDrainTestRegistry(t), SpawnPolecatAPIRequest{
		Name: "q7c936e", Id: "mg-7c93e", Repo: enthRepo, Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("enth spawn with a non-branching template: status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	for _, want := range []string{"refusing to spawn onto enth repo o/r", "never references .VCS", "pogo agent prompt install", "Nothing was dispatched"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("refusal does not say %q: %s", want, rr.Body.String())
		}
	}
	polecatsDir := filepath.Join(os.Getenv("POGO_HOME"), "polecats", "q7c936e")
	if _, err := os.Stat(polecatsDir); !os.IsNotExist(err) {
		t.Errorf("the refused spawn left %s behind (%v)", polecatsDir, err)
	}

	rr = spawnPolecat(t, newDrainTestRegistry(t), SpawnPolecatAPIRequest{
		Name: "q7c936g", Id: "mg-7c93g", Repo: gitRepo, Template: BuildWorkerTemplate,
	})
	if strings.Contains(rr.Body.String(), "never references .VCS") {
		t.Errorf("control: a git spawn was refused by the enth template gate: %s", rr.Body.String())
	}
}

func TestEnthTemplateRefusalPassesABranchingTemplate(t *testing.T) {
	home := installPolecatTemplate(t)
	p := filepath.Join(home, "agents", "templates", "polecat.md")
	e := vcs.EnthRepo{Remote: "https://e", Repo: "o/r"}
	if got := enthTemplateRefusal(p, e); got == "" {
		t.Fatal("control: the non-branching template was not refused")
	}
	if err := os.WriteFile(p, []byte("task {{.Id}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A drop-in is part of what the worker is rendered from, so a branch in
	// one counts.
	dropDir := filepath.Join(home, "agents", "dropins", "polecat")
	os.MkdirAll(dropDir, 0o755)
	os.WriteFile(filepath.Join(dropDir, "vcs.md"), []byte(`{{if eq .VCS "enth"}}enth{{end}}`+"\n"), 0o644)
	if got := enthTemplateRefusal(p, e); got != "" {
		t.Errorf("a template whose drop-in branches on .VCS was refused: %s", got)
	}
	// And every shipped template passes.
	for name, body := range embeddedPolecatTemplates(t) {
		os.RemoveAll(dropDir)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := enthTemplateRefusal(p, e); got != "" {
			t.Errorf("shipped %s was refused: %s", name, got)
		}
	}
}

// The pre-submit check is one line on both VCSs (enth-vcs §12, mg-71014): a
// git render that submits runs `pogo presubmit --item=<id> --target=<target>`
// on the line before `pogo refinery submit`, chained with &&, and no render of
// any template still carries the presubmit_check shell it replaced. The verb is
// held to that shell by cmd/pogo's TestPresubmitGitEqualsTheTemplateShell.
func TestGitSubmitRunsPogoPresubmit(t *testing.T) {
	submitters := 0
	for name, body := range embeddedPolecatTemplates(t) {
		for k, v := range varMatrix() {
			for _, vcsName := range []string{TemplateVCSGit, TemplateVCSEnth} {
				v.VCS = vcsName
				out := render(t, name, body, v)
				if strings.Contains(out, "presubmit_check") {
					t.Errorf("%s [%s vcs=%s]: still carries the presubmit_check shell", name, k, vcsName)
				}
				if vcsName != TemplateVCSGit {
					continue
				}
				lines := strings.Split(out, "\n")
				for i, l := range lines {
					if !strings.Contains(l, "pogo refinery submit \"$BRANCH\"") {
						continue
					}
					submitters++
					target := "main"
					if v.Branch != "" {
						target = v.Branch
					}
					want := "pogo presubmit --item=" + v.Id + " --target=" + target + " &&"
					if i == 0 || strings.TrimSpace(lines[i-1]) != want {
						prev := ""
						if i > 0 {
							prev = lines[i-1]
						}
						t.Errorf("%s [%s]: the line before the submit is %q; want %q", name, k, prev, want)
					}
				}
			}
		}
	}
	// Control: polecat.md and polecat-architect.md submit in every shape.
	if submitters < 2*len(varMatrix()) {
		t.Errorf("control: only %d git renders submit; want at least %d", submitters, 2*len(varMatrix()))
	}
}

// TestGitRenderUnchangedSinceBase is §16 row 11's proof: render every
// embedded template with VCS="" and compare it byte for byte with the same
// template at POGO_TEMPLATE_BASE (a git revision), across the whole var
// matrix. Opt-in, because any later edit to a template's git text is a
// legitimate change this would report: run it with the revision before the
// change under test, e.g.
//
//	POGO_TEMPLATE_BASE=a085a530 go test ./internal/agent -run TestGitRenderUnchangedSinceBase
func TestGitRenderUnchangedSinceBase(t *testing.T) {
	base := os.Getenv("POGO_TEMPLATE_BASE")
	if base == "" {
		t.Skip("set POGO_TEMPLATE_BASE=<git rev> to compare git renders against that revision")
	}
	compared := 0
	for name, body := range embeddedPolecatTemplates(t) {
		old, err := exec.Command("git", "show", base+":./prompts/templates/"+name).Output()
		if err != nil {
			t.Errorf("%s at %s: %v", name, base, err)
			continue
		}
		oldBody := templateBody(t, old)
		for k, v := range varMatrix() {
			if got, want := render(t, name, body, v), render(t, name, oldBody, v); got != want {
				t.Errorf("%s [%s]: the git render differs from %s", name, k, base)
			}
			compared++
		}
	}
	t.Logf("%d renders compared byte for byte against %s", compared, base)
}
